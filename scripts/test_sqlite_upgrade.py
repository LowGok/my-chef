"""Rehearse a source-version upgrade with synthetic data and no external providers."""
from contextlib import contextmanager
from datetime import datetime, timedelta, timezone
from http.cookiejar import CookieJar
import json
import os
import re
from pathlib import Path
import secrets
import socket
import sqlite3
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

from legacy_snapshot import legacy_snapshot
from e2e_server import fixture_email
from rewrite import CACHE, ROOT, environment


class Client:
    def __init__(self, origin):
        self.origin, self.csrf = origin, ""
        self.http = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(CookieJar()))

    def register(self, profile, password, capability):
        self.call("POST", "/api/v1/register", profile, status=202)
        proof = ""
        for _ in range(150):
            request = urllib.request.Request(self.origin + "/api/v1/fixture/inbox", method="POST",
                headers={"Origin": self.origin, "Authorization": "Bearer " + capability, "Content-Type": "application/json"},
                data=json.dumps({"recipient": profile["email"]}).encode())
            with self.http.open(request, timeout=10) as response:
                if response.status == 200:
                    match = re.search(r"/app/signup#([A-Za-z0-9_-]{43})", json.load(response)["text"])
                    if match:
                        proof = match[1]
                        break
            time.sleep(.1)
        assert proof, "fixture signup message was not delivered"
        self.call("POST", "/api/v1/signup/complete", {"proof": proof, "password": password,
                  **{key: profile[key] for key in ("firstName", "lastName", "username")}}, status=201)
        self.call("POST", "/api/v1/login", {"email": profile["email"], "password": password})

    def verify_legacy(self, recipient, capability):
        self.call("GET", "/api/v1/profile", status=403)
        self.call("POST", "/api/v1/email/verification/resend", {}, status=202)
        for _ in range(100):
            request = urllib.request.Request(self.origin + "/api/v1/fixture/inbox", method="POST",
                headers={"Origin": self.origin, "Authorization": "Bearer " + capability, "Content-Type": "application/json"},
                data=json.dumps({"recipient": recipient}).encode())
            with self.http.open(request, timeout=10) as response:
                if response.status == 200:
                    match = re.search(r"/app/verify-email#([A-Za-z0-9_-]{43})", json.load(response)["text"])
                    if match:
                        result = self.call("POST", "/api/v1/email/verification/confirm", {"proof": match[1]})
                        assert result["user"] and not result.get("verificationRequired")
                        return
            time.sleep(.1)
        raise AssertionError("fixture verification message was not available")

    def call(self, method, path, body=None, *, status=200):
        headers = {"Origin": self.origin, "Content-Type": "application/json", "X-CSRF-Token": self.csrf}
        if path in ("/api/v1/generations", "/api/v1/account/deletion"):
            headers["Idempotency-Key"] = secrets.token_hex(16)
        request = urllib.request.Request(self.origin + path, method=method, headers=headers,
                                         data=None if body is None else json.dumps(body).encode())
        try:
            response = self.http.open(request, timeout=10)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            if response.status != status:
                raise AssertionError(f"{method} {path}: expected {status}, got {response.status}")
            value = json.load(response)
        if isinstance(value, dict) and "csrfToken" in value:
            self.csrf = value["csrfToken"]
        return value


@contextmanager
def server(binary, database, origin, env, log, lifecycle=None):
    settings = env.copy()
    settings.update(MYCHEF_LIFECYCLE_DB=str(lifecycle or str(database) + ".lifecycle"), MYCHEF_DB=str(database), MYCHEF_ADDR=origin.removeprefix("http://"),
                    MYCHEF_ORIGIN=origin, MYCHEF_DEV="1", MYCHEF_PROVIDER="fixture", MYCHEF_E2E="1")
    for key in ("MYCHEF_CREDENTIALS", "MYCHEF_POSTGRES_URL", "MYCHEF_TEST_POSTGRES_URL", "GEMINI_API_KEY",
                "CLOUDINARY_CLOUD_NAME", "CLOUDINARY_API_KEY", "CLOUDINARY_API_SECRET"):
        settings.pop(key, None)
    process = subprocess.Popen([str(binary)], env=settings, stdout=log, stderr=log, cwd=ROOT)
    try:
        for _ in range(200):
            if process.poll() is not None:
                raise RuntimeError("fixture server exited before readiness")
            try:
                with urllib.request.urlopen(origin + "/readyz", timeout=1) as response:
                    if response.status == 200:
                        break
            except OSError:
                time.sleep(.1)
        else:
            raise RuntimeError("fixture server readiness timed out")
        yield
    finally:
        if process.poll() is None:
            process.terminate()
            try:
                code = process.wait(timeout=20)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
                raise RuntimeError("fixture server did not stop gracefully") from None
            if code != 0:
                raise RuntimeError("fixture server shutdown failed")


def main():
    os.umask(0o077)
    env = environment()
    with tempfile.TemporaryDirectory(prefix="mychef-upgrade-") as temporary:
        root = Path(temporary)
        seed_binary, seed_migrate = CACHE / "mychef", CACHE / "mychef-migrate"
        seed_email, seed_capability = fixture_email(root)
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            origin = f"http://127.0.0.1:{sock.getsockname()[1]}"
        client, stranger = Client(origin), Client(origin)
        database, snapshot, upgraded, backup, restored = [root / name for name in
            ("original.db", "before-upgrade.db", "candidate.db", "candidate-backup.db", "restored.db")]

        def migrate(binary, *command):
            subprocess.run([str(binary), *map(str, command)], env=env, check=True, timeout=30,
                           stdout=subprocess.DEVNULL)

        migrate(seed_migrate, "up", "--db", database)
        with (root / "server.log").open("w") as log:
            with server(seed_binary, database, origin, dict(env, **seed_email), log):
                client.call("GET", "/api/v1/session")
                password = secrets.token_urlsafe(24)
                client.register({"email": "upgrade@example.test", "username": "upgrade", "firstName": "Upgrade", "lastName": "Fixture"}, password, seed_capability)
                profile = client.call("GET", "/api/v1/profile")
                profile = {key: profile[key] for key in ("username", "firstName", "lastName", "birthdate", "sex", "preferences")}
                profile["preferences"].update(diet=["Vegetarian"], dislikes=["Mushrooms"])
                client.call("PUT", "/api/v1/profile", profile)
                operation = client.call("POST", "/api/v1/generations", {"dates": ["2026-10-01"], "adults": 2,
                                        "children": 0, "maxMinutes": 30, "budget": "low", "region": "Belgium",
                                        "creativity": 2}, status=202)
                for _ in range(200):
                    operation = client.call("GET", "/api/v1/generations/" + operation["id"])
                    if operation["status"] == "failed":
                        raise AssertionError("fixture generation failed")
                    if operation["status"] == "succeeded":
                        menu = client.call("GET", "/api/v1/menus/" + operation["menuId"])
                        if menu["recipes"][0]["imageStatus"] != "pending":
                            break
                    time.sleep(.1)
                else:
                    raise RuntimeError("fixture generation timed out")
                recipe = menu["recipes"][0]
                assert recipe["ingredients"][0]["quantity"] == "0.12345678901234567890123456789"
                client.call("PUT", f"/api/v1/recipes/{recipe['id']}/favorite", {})
                today = datetime.now(timezone.utc).date()
                week = (today - timedelta(days=today.weekday()) + timedelta(days=7)).isoformat()
                client.call("POST", f"/api/v1/favorites/{recipe['id']}/queue", {"week": week, "today": today.isoformat()})
                shopping = client.call("GET", "/api/v1/shopping/2026-09-28")
                shopping["items"][0]["state"] = "basket"
                client.call("PUT", "/api/v1/shopping/2026-09-28", shopping)
                stranger.call("GET", "/api/v1/session")
                stranger.register({"email": "other-upgrade@example.test", "username": "other-upgrade", "firstName": "Other", "lastName": "Fixture"}, password, seed_capability)
                paths = ["/api/v1/session", "/api/v1/profile", "/api/v1/menus/" + operation["menuId"],
                         "/api/v1/favorites", "/api/v1/favorite-weeks/" + week, "/api/v1/shopping/2026-09-28"]
                expected = [client.call("GET", path) for path in paths]
                expected[1].pop("accountInstance", None)
            legacy_snapshot(database, snapshot)
            # Restore cannot invent account identities for a pre-incarnation backup.
            refused = subprocess.run([str(CACHE / "mychef-migrate"), "restore", "--db",
                                      str(upgraded), "--file", str(snapshot)], env=env,
                                     stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30)
            assert refused.returncode != 0 and not upgraded.exists()
            # Rehearse the explicit first migration on a disposable copy of the
            # original database, before any lifecycle journal has ever existed.
            with sqlite3.connect(snapshot) as original, sqlite3.connect(upgraded) as candidate:
                original.backup(candidate)
            migrate(CACHE / "mychef-migrate", "up", "--db", upgraded)
            with sqlite3.connect(upgraded) as candidate:
                assert candidate.execute("SELECT version,dirty FROM schema_migrations").fetchone() == (16, 0)
                assert {"personal_recipes", "personal_recipe_versions", "personal_photo_assets", "personal_recipe_queue"}.issubset(
                    {row[0] for row in candidate.execute("SELECT name FROM sqlite_master WHERE type='table'")})
            with sqlite3.connect(str(upgraded) + ".lifecycle") as journal:
                assert journal.execute("SELECT version FROM lifecycle_journal_meta").fetchone()[0] == 2
            future = root / "future-schema.db"
            with sqlite3.connect(upgraded) as current, sqlite3.connect(future) as copy:
                current.backup(copy)
                copy.execute("UPDATE schema_migrations SET version=999")
            refused_future = subprocess.run([str(CACHE / "mychef-migrate"), "up", "--db", str(future)],
                                            env=env, stdout=log, stderr=log, timeout=30)
            assert refused_future.returncode != 0, "future schema was accepted"

            incarnation = None
            def account_data():
                nonlocal incarnation
                values = [client.call("GET", path) for path in paths[1:]]
                # Preserve exact legacy data comparisons and separately prove
                # that the newly added cache identity survives every restart.
                current = values[0].pop("accountInstance", None)
                assert isinstance(current, str) and re.fullmatch("[0-9a-f]{32}", current)
                if incarnation is None:
                    incarnation = current
                assert current == incarnation, "account incarnation changed"
                return values

            def verify():
                assert account_data() == expected[1:], "saved application data changed"
                stranger.call("GET", f"/api/v1/recipes/{recipe['id']}", status=404)
                with sqlite3.connect(upgraded) as db:
                    assert db.execute("SELECT COUNT(*) FROM generations").fetchone()[0] == 1

            email, capability = seed_email, seed_capability
            with server(CACHE / "mychef", upgraded, origin, dict(env, **email), log):
                for account, address in ((client, "upgrade@example.test"), (stranger, "other-upgrade@example.test")):
                    session = account.call("GET", "/api/v1/session")
                    assert session["user"] is None and session["verificationRequired"]
                    account.verify_legacy(address, capability)
                verify()
                migrate(CACHE / "mychef-migrate", "backup", "--db", upgraded, "--file", backup)
            with server(CACHE / "mychef", upgraded, origin, env, log):
                verify()
            migrate(CACHE / "mychef-migrate", "restore", "--db", restored, "--file", backup,
                    "--lifecycle-db", str(upgraded) + ".lifecycle")
            with server(CACHE / "mychef", restored, origin, env, log, str(upgraded) + ".lifecycle"):
                assert account_data() == expected[1:]
                stranger.call("GET", f"/api/v1/recipes/{recipe['id']}", status=404)
                login = Client(origin)
                login.call("GET", "/api/v1/session")
                assert login.call("POST", "/api/v1/login", {"email": "upgrade@example.test", "password": password})["user"]["id"] == expected[0]["user"]["id"]
            with sqlite3.connect(restored) as db:
                assert db.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
                assert db.execute("PRAGMA foreign_key_check").fetchall() == []
                assert db.execute("SELECT COUNT(*) FROM generations").fetchone()[0] == 1
            # Delete through the real API, then restore the earlier snapshot
            # with the same independent journal and test the live endpoints.
            with server(CACHE / "mychef", restored, origin, dict(env, **email), log, str(upgraded) + ".lifecycle"):
                deleting = Client(origin)
                deleting.call("GET", "/api/v1/session")
                deleting.call("POST", "/api/v1/login", {"email": "upgrade@example.test", "password": password})
                deleting.call("POST", "/api/v1/account/deletion", {"currentPassword": password, "confirmation": "DELETE"})
                deleting.call("GET", "/api/v1/session")
                deleting.call("POST", "/api/v1/login", {"email": "upgrade@example.test", "password": password}, status=401)
            replayed = root / "deleted-replayed.db"
            migrate(CACHE / "mychef-migrate", "restore", "--db", replayed, "--file", backup,
                    "--lifecycle-db", str(upgraded) + ".lifecycle")
            with server(CACHE / "mychef", replayed, origin, dict(env, **email), log, str(upgraded) + ".lifecycle"):
                deleted = Client(origin)
                deleted.call("GET", "/api/v1/session")
                deleted.call("POST", "/api/v1/login", {"email": "upgrade@example.test", "password": password}, status=401)
                deleted.call("POST", "/api/v1/password-recovery/request", {"email": "upgrade@example.test"}, status=202)
                for _ in range(100):
                    with sqlite3.connect(replayed) as db:
                        pending = db.execute("SELECT COUNT(*) FROM recovery_jobs").fetchone()[0]
                    if pending == 0:
                        break
                    time.sleep(.1)
                else:
                    raise AssertionError("restored account recovery request did not finish")
                request = urllib.request.Request(origin + "/api/v1/fixture/inbox", method="POST",
                    headers={"Origin": origin, "Authorization": "Bearer " + capability, "Content-Type": "application/json"},
                    data=json.dumps({"recipient": "upgrade@example.test"}).encode())
                with urllib.request.urlopen(request, timeout=10) as response:
                    assert response.status == 204, "deleted account received a recovery message"
                other = Client(origin)
                other.call("GET", "/api/v1/session")
                assert other.call("POST", "/api/v1/login", {"email": "other-upgrade@example.test", "password": password})["user"]
                with sqlite3.connect(replayed) as db:
                    assert db.execute("SELECT COUNT(*) FROM users WHERE email_key='upgrade@example.test'").fetchone()[0] == 0
                    assert db.execute("SELECT COUNT(*) FROM meals").fetchone()[0] == 0
                    assert db.execute("PRAGMA foreign_key_check").fetchall() == []
        print(json.dumps({"outcome": "passed", "baselineSchema": 3, "checks": ["accounts and password login",
            "restricted legacy sessions and mailbox verification", "durable verified sessions", "exact recipe quantities", "favorites and next-week queue", "shopping basket",
            "owner isolation", "candidate upgrade", "pre-incarnation restore rejection", "process restart", "online backup and fresh restore",
            "SQLite integrity and foreign keys", "stable account incarnations", "deleted-account restore blocks login and recovery", "unrelated account survives deletion replay", "no repeated generation", "no PostgreSQL or paid APIs"]}))


if __name__ == "__main__":
    main()
