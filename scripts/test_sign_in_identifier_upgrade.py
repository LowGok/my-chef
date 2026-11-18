#!/usr/bin/env python3
"""Verify schema 27 to 28 and rollback with explicitly built app binaries."""
import argparse
from contextlib import closing
import os
from pathlib import Path
import secrets
import socket
import sqlite3
import subprocess
import tempfile

from e2e_server import fixture_email
from test_sqlite_upgrade import Client, server


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--prior-app", required=True, type=Path)
    parser.add_argument("--prior-migrate", required=True, type=Path)
    parser.add_argument("--candidate-app", required=True, type=Path)
    parser.add_argument("--candidate-migrate", required=True, type=Path)
    args = parser.parse_args()
    os.umask(0o077)
    env = {key: val for key, val in os.environ.items()
           if key in {"PATH", "LANG", "LC_ALL", "TZ", "TMPDIR", "SSL_CERT_FILE"}}
    with tempfile.TemporaryDirectory(prefix="mychef-sign-in-upgrade-") as temporary:
        root = Path(temporary)
        original, backup, candidate, restored, journal = (
            root / name for name in ("prior.db", "backup.db", "candidate.db", "restored.db", "current.lifecycle")
        )
        email, capability = fixture_email(root)
        env.update(email)
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            origin = f"http://127.0.0.1:{sock.getsockname()[1]}"

        def migrate(binary, action, path, *extra):
            subprocess.run([str(binary.resolve()), action, "--db", str(path), "--lifecycle-db", str(journal), *extra],
                           env=env, check=True, stdout=subprocess.DEVNULL, timeout=30)

        def snapshot(path):
            with closing(sqlite3.connect(path)) as db:
                version = db.execute("SELECT version,dirty FROM schema_migrations").fetchone()
                users = db.execute("SELECT user_id,username,email,password,email_verified_at FROM users ORDER BY user_id").fetchall()
                sessions = db.execute("SELECT hash,user_id,expires_unix FROM sessions ORDER BY hash").fetchall()
                return version, users, sessions

        def verify_login(identifier, email_address, name, expected_id):
            client = Client(origin)
            client.call("GET", "/api/v1/session")
            client.call("POST", "/api/v1/login", {"email": identifier, "password": password})
            profile = client.call("GET", "/api/v1/profile")
            assert (profile["id"], profile["username"], profile["email"].casefold()) == (expected_id, name, email_address)

        password = secrets.token_urlsafe(24)
        address = "upgrade-owner@example.test"
        existing = Client(origin)
        with (root / "app.log").open("w") as log:
            migrate(args.prior_migrate, "up", original)
            with server(args.prior_app.resolve(), original, origin, env, log, journal):
                existing.call("GET", "/api/v1/session")
                existing.register({"email": address, "username": "Étage Straße",
                                   "firstName": "Upgrade", "lastName": "Fixture"}, password, capability)
                expected = existing.call("GET", "/api/v1/profile")
            before = snapshot(original)
            assert before[0] == (27, 0) and before[1] and before[2]
            migrate(args.prior_migrate, "backup", original, "--file", str(backup))
            with closing(sqlite3.connect(backup)) as source, closing(sqlite3.connect(candidate)) as dest:
                source.backup(dest)
            migrate(args.candidate_migrate, "up", candidate)
            after = snapshot(candidate)
            assert after[0] == (28, 0) and after[1:] == before[1:]
            with server(args.candidate_app.resolve(), candidate, origin, env, log, journal):
                assert existing.call("GET", "/api/v1/profile") == expected
                verify_login("  ÉTAGE STRASSE  ", address, "Étage Straße", expected["id"])
            migrate(args.prior_migrate, "restore", restored, "--file", str(backup))
            assert snapshot(restored) == before
            with server(args.prior_app.resolve(), restored, origin, env, log, journal):
                assert existing.call("GET", "/api/v1/profile") == expected
                verify_login(address, address, "Étage Straße", expected["id"])
    print("Verified retained account and session across SQLite 27→28 and prior-binary rollback with the current lifecycle journal.")


if __name__ == "__main__":
    main()
