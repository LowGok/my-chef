#!/usr/bin/env python3
"""Rehearse schema 28 to 31 on disposable builds and verify the Preview fallback."""
import argparse
from contextlib import closing
import json
import os
from pathlib import Path
import secrets
import socket
import sqlite3
import subprocess
import tempfile
import urllib.error
import urllib.request

from e2e_server import fixture_email
from test_sqlite_upgrade import Client, server


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for flag in ("prior-app", "prior-migrate", "candidate-app", "candidate-migrate"):
        parser.add_argument("--" + flag, required=True, type=Path)
    args = parser.parse_args()
    os.umask(0o077)
    env = {key: value for key, value in os.environ.items()
           if key in {"PATH", "LANG", "LC_ALL", "TZ", "TMPDIR", "SSL_CERT_FILE"}}
    with tempfile.TemporaryDirectory(prefix="mychef-identity-upgrade-") as temporary:
        root = Path(temporary)
        db, journal = root / "preview.db", root / "preview.lifecycle"
        email, capability = fixture_email(root)
        env.update(email)
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            origin = f"http://127.0.0.1:{sock.getsockname()[1]}"
        browser = Client(origin)
        password = secrets.token_urlsafe(24)
        address = "identity-upgrade@example.test"
        week = "/shopping/2026-11-16"

        def native(lane, method, path, body=None, token=None, status=200):
            prefix = "/api/native/app/v1" if lane == "app" else "/api/native/v1"
            headers = {"Content-Type": "application/json"}
            if token:
                headers["Authorization"] = "Bearer " + token
            request = urllib.request.Request(origin + prefix + path, method=method, headers=headers,
                                             data=None if body is None else json.dumps(body).encode())
            try:
                response = urllib.request.urlopen(request, timeout=10)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                assert response.status == status, f"{lane} {method} {path}: expected {status}, got {response.status}"
                return json.load(response) if status < 400 else None

        def sign_in(lane):
            start = native(lane, "POST", "/session", {"platform": "ios"}, status=201)["native"]
            return native(lane, "POST", "/login", {"email": address, "password": password},
                          token=start["accessToken"])["native"]["accessToken"]

        def migrate(binary):
            return subprocess.run([str(binary.resolve()), "up", "--db", str(db), "--lifecycle-db", str(journal)],
                                  env=env, timeout=30, capture_output=True, text=True)

        with (root / "fixture.log").open("w") as log:
            migrate(args.prior_migrate).check_returncode()
            with server(args.prior_app.resolve(), db, origin, env, log, journal):
                browser.call("GET", "/api/v1/session")
                browser.register({"email": address, "username": "identity-upgrade", "firstName": "Identity",
                                  "lastName": "Fixture"}, password, capability)
                expected = browser.call("GET", "/api/v1/profile")
                preview = sign_in("preview")
                shopping = native("preview", "GET", week, token=preview)
                shopping["items"].append({"id": "fixture-oats", "name": "Fixture oats", "state": "buy",
                                          "manual": True, "contributions": []})
                saved = native("preview", "PUT", week, shopping, preview)
            with closing(sqlite3.connect(db)) as sql:
                assert sql.execute("SELECT version,dirty FROM schema_migrations").fetchone() == (28, 0)
                sql.executescript("""CREATE TRIGGER reject_identity_rehearsal BEFORE UPDATE ON schema_migrations
                    WHEN NEW.version=29 BEGIN SELECT RAISE(ABORT,'synthetic native identity interruption'); END;""")
            failure = migrate(args.candidate_migrate)
            assert failure.returncode != 0 and "Migration failed" in failure.stderr
            with server(args.prior_app.resolve(), db, origin, env, log, journal):
                assert browser.call("GET", "/api/v1/profile") == expected
                assert native("preview", "GET", week, token=preview) == saved
            print("Injected upgrade failure left the prior binary, account, session and shopping usable.")
            with closing(sqlite3.connect(db)) as sql:
                assert sql.execute("SELECT version,dirty FROM schema_migrations").fetchone() == (28, 0)
                sql.executescript("DROP TRIGGER reject_identity_rehearsal;")
            migrate(args.candidate_migrate).check_returncode()
            with closing(sqlite3.connect(db)) as sql:
                assert sql.execute("SELECT version,dirty FROM schema_migrations").fetchone() == (31, 0)
                assert sql.execute("SELECT DISTINCT identity_lane FROM native_families").fetchall() == [("preview",)]
            with server(args.candidate_app.resolve(), db, origin, env, log, journal):
                native("app", "POST", "/session", {"platform": "ios"}, status=404)
                assert browser.call("GET", "/api/v1/profile") == expected
                assert native("preview", "GET", week, token=preview) == saved
            control = root / "native-identity.sock"
            control_env = dict(env, MYCHEF_NATIVE_APP_INGRESS="1",
                               MYCHEF_NATIVE_IDENTITY_CONTROL_SOCKET=str(control))

            def policy(change=None):
                command = ["curl", "--fail", "--silent", "--show-error", "--noproxy", "*",
                           "--max-time", "5", "--unix-socket", str(control)]
                if change is not None:
                    command.extend(["-X", "PUT", "--json", json.dumps(change)])
                return json.loads(subprocess.check_output([*command, "http://localhost/policy"],
                                                          env=env, timeout=10))

            with server(args.candidate_app.resolve(), db, origin, control_env, log, journal):
                assert not policy()["Active"]
                foreign = urllib.request.Request(origin + "/policy", method="PUT",
                                                 data=b'{"expectedRevision":0,"active":true}')
                try:
                    with urllib.request.urlopen(foreign, timeout=5) as response:
                        raise AssertionError(f"Operator policy exposed over TCP: {response.status}")
                except urllib.error.HTTPError as error:
                    assert error.code in (404, 405)
                assert policy()["Revision"] == 0
                assert policy({"expectedRevision": 0, "active": True})["Active"]
                app = sign_in("app")
                assert preview != app
                new_profile = native("app", "GET", "/profile", token=app)
                old_profile = native("preview", "GET", "/profile", token=preview)
                assert new_profile["id"] == old_profile["id"] == expected["id"]
                assert native("app", "GET", week, token=app) == saved
                native("preview", "GET", "/profile", token=app, status=401)
                withdrawn = policy({"expectedRevision": 1, "active": False})
                assert not withdrawn["Active"] and withdrawn["Revision"] == 2
                native("app", "GET", "/profile", token=app, status=503)
                assert native("preview", "GET", week, token=preview) == saved
            with server(args.candidate_app.resolve(), db, origin, control_env, log, journal):
                assert not policy()["Active"] and policy()["Revision"] == 2
                native("app", "GET", "/profile", token=app, status=503)
                assert native("preview", "GET", week, token=preview) == saved
            with closing(sqlite3.connect(journal)) as sql:
                assert sql.execute("PRAGMA quick_check").fetchone() == ("ok",)
    print("Explicit upgrade and operator withdrawal preserved the journal, account, Preview session and synced shopping.")


if __name__ == "__main__":
    main()
