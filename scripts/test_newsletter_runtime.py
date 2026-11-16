#!/usr/bin/env python3
"""Rehearse the actual newsletter executable using disposable SQL and fake mail.

Seed proof authority is created by a test-only helper before startup. The HTTP
capture, worker dispatch, restart, operator commands and restore use real binaries.
No fixture HTTP surface or real provider/recipient is used. Prints counts only.
"""
from contextlib import contextmanager, closing
import base64
import hashlib
import json
import os
from pathlib import Path
import secrets
import socket
import sqlite3
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

from rewrite import CACHE, ROOT, environment


def main():
    os.umask(0o077)
    installed = environment()
    allowed = {"PATH", "HOME", "USER", "LANG", "LC_ALL", "TZ", "TMPDIR", "GOTMPDIR",
               "GOCACHE", "GOMODCACHE", "GOTOOLCHAIN", "GOPROXY", "CGO_ENABLED"}
    env = {key: value for key, value in installed.items() if key in allowed}
    binary, migrate = CACHE / "mychef", CACHE / "mychef-migrate"
    assert binary.is_file() and migrate.is_file(), "run scripts/rewrite.py build first"
    checks = []
    http = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with tempfile.TemporaryDirectory(prefix="mychef-newsletter-runtime-") as directory:
        state = Path(directory)
        db, journal = state / "app.db", state / "journal.db"
        token = secrets.token_urlsafe(48)
        key = lambda: base64.b64encode(secrets.token_bytes(32)).decode()
        credentials, keys = state / "credentials.json", state / "keys.json"
        credentials.write_text(json.dumps({"newsletter_service_token": token,
            "newsletter_email_from": "MyChef <newsletter@mychef.dev>",
            "newsletter_email_reply_to": "feedback@gkldevelopment.com"}))
        keys.write_text(json.dumps({"active": "fixture", "keys": {"fixture": key()}, "rate_key": key()}))
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            address = f"127.0.0.1:{listener.getsockname()[1]}"
        env.update(MYCHEF_ADDR=address, MYCHEF_DB=str(db), MYCHEF_LIFECYCLE_DB=str(journal),
            MYCHEF_ORIGIN="https://mychef.dev", MYCHEF_DEV="1", MYCHEF_E2E="1", MYCHEF_PROVIDER="fixture",
            MYCHEF_EMAIL_MODE="fake", MYCHEF_CREDENTIALS=str(credentials), MYCHEF_EMAIL_PROOF_KEYS_FILE=str(keys),
            MYCHEF_NEWSLETTER_MODE="fixture", MYCHEF_NEWSLETTER_ORIGIN="https://mychef.dev",
            MYCHEF_NEWSLETTER_CONSENT_VERSION="newsletter-runtime-rehearsal", MYCHEF_NEWSLETTER_CONTACT="feedback@gkldevelopment.com",
            MYCHEF_NEWSLETTER_PENDING_DAYS="none", MYCHEF_NEWSLETTER_WITHDRAWN_DAYS="none",
            MYCHEF_NEWSLETTER_OPERATIONS_READY="1", MYCHEF_NEWSLETTER_COLLECT="0")
        origin = "http://" + address

        def run(*args, data=None, ok=True, settings=None):
            result = subprocess.run([str(arg) for arg in args], env=settings or env, cwd=ROOT,
                input=data, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=60)
            assert (result.returncode == 0) == ok, "unexpected executable exit status"
            return result.stdout

        def call(path, body=None, expected=200, authorized=True, method=None):
            headers = {"Origin": "https://mychef.dev", "Content-Type": "application/json",
                       "X-MyChef-Newsletter-Source": "192.0.2.144"}
            if authorized:
                headers["Authorization"] = "Bearer " + token
            request = urllib.request.Request(origin + path, headers=headers,
                data=None if body is None else json.dumps(body).encode(), method=method)
            try:
                response = http.open(request, timeout=3)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                assert response.status == expected, f"{path}: expected {expected}, got {response.status}"
                raw = response.read()
                assert token.encode() not in raw and b"@runtime.example.test" not in raw, "private response"
                return json.loads(raw) if raw else None

        @contextmanager
        def server(database=db, collect="0"):
            settings = dict(env, MYCHEF_DB=str(database), MYCHEF_NEWSLETTER_COLLECT=collect)
            log = state / "process.log"
            with log.open("ab") as output:
                process = subprocess.Popen([str(binary), "newsletter", "serve"], env=settings, cwd=ROOT,
                                           stdout=output, stderr=output)
                try:
                    for _ in range(200):
                        assert process.poll() is None, "runtime exited before operational readiness"
                        try:
                            call("/readyz")
                            break
                        except OSError:
                            time.sleep(.05)
                    else:
                        raise AssertionError("runtime did not become operational")
                    yield settings
                finally:
                    if process.poll() is None:
                        process.terminate()
                        try:
                            assert process.wait(timeout=15) == 0, "unclean shutdown"
                        except subprocess.TimeoutExpired:
                            process.kill()
                            process.wait()
                            raise AssertionError("shutdown deadline exceeded")

        def sql(statement, parameters=(), database=db):
            with closing(sqlite3.connect(database, timeout=5)) as connection:
                result = connection.execute(statement, parameters).fetchall()
                connection.commit()
                return result

        # Build a genuine schema-23 disposable predecessor from immutable inputs.
        predecessor = state / "schema23.db"
        migration_files = sorted((ROOT / "migrations").glob("[0-9][0-9][0-9]_*.sql"))[:23]
        assert len(migration_files) == 23
        schema = "".join(path.read_text() for path in migration_files)
        with closing(sqlite3.connect(predecessor)) as connection:
            connection.executescript(schema)
            connection.execute("CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,checksum TEXT NOT NULL,dirty BOOLEAN NOT NULL)")
            connection.execute("INSERT INTO schema_migrations VALUES(23,?,0)", (hashlib.sha256(schema.encode()).hexdigest(),))
            connection.commit()
        run(migrate, "up", "--db", predecessor, "--lifecycle-db", state / "schema23.journal")
        version = sql("SELECT version FROM schema_migrations", database=predecessor)[0][0]
        assert version == len(list((ROOT / "migrations").glob("[0-9][0-9][0-9]_*.sql")))
        run(migrate, "up", "--db", db, "--lifecycle-db", journal)
        checks.append("explicit fresh initialization and schema-23 upgrade")
        seed_env = dict(env, MYCHEF_NEWSLETTER_REHEARSAL_DIR=directory)
        run("go", "test", "./cmd/mychef", "-run", "^TestNewsletterRehearsalSeed$", "-count=1", settings=seed_env)
        authority = json.loads((state / "authority.json").read_text())
        original_keys = keys.read_bytes()
        changed_keys = json.loads(original_keys)
        changed_keys["rate_key"] = key()
        replaced = state / "changed-keys.json"
        replaced.write_text(json.dumps(changed_keys))
        run(binary, "newsletter", "serve", settings=dict(env, MYCHEF_EMAIL_PROOF_KEYS_FILE=str(replaced)), ok=False)
        checks.append("changed stable key refuses startup without rebinding")

        with server() as settings:
            report = call("/internal/newsletter/readiness")
            assert report["role"] == "newsletter" and report["mode"] == "fixture" and not report["ready"]
            assert all(value == (name != "collectionEnabled") for name, value in report["checks"].items())
            call("/internal/newsletter/readiness", expected=401, authorized=False)
            call("/api/v1/newsletter/subscribe", {}, expected=503)
            call("/api/v1/newsletter/confirm", {"proof": authority["pending"]["confirm"]})
            run(binary, "newsletter", "serve", settings=settings, ok=False)
            for path in ("/app/", "/api/v1/login", "/api/native/v1/login", "/fixture/inbox", "/api/v1/webhooks/email"):
                call(path, expected=404)
        checks.append("paused vector, protected operator route, proof confirmation and process locks")
        # Preserve genuinely issued-but-expired account work in the old copy.
        # The restore command itself must remove it before any server can sweep.
        expired_canary = "expired-signup-runtime-canary@example.test"
        sql("INSERT INTO signup_proofs VALUES('restore-expired','restore-expired-hash','restore-expired-tag',2,NULL,?)", (expired_canary,))
        sql("INSERT INTO email_outbox(key,recipient,template,data,state,next_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)",
            ("signup/restore-expired", expired_canary, "signup-proof/v1", '{"expires":2}', "accepted", 1, 1, 1))
        snapshot = state / "before-erasure.db"
        run(migrate, "backup", "--db", db, "--lifecycle-db", journal, "--file", snapshot)
        assert expired_canary.encode() in snapshot.read_bytes()
        snapshot_digest = hashlib.sha256(snapshot.read_bytes()).digest()
        with server(collect="1"):
            assert call("/internal/newsletter/readiness")["ready"]
            assert call("/api/v1/newsletter/subscribe", {"email": "captured@runtime.example.test", "locale": "nl-BE",
                "consentVersion": "newsletter-runtime-rehearsal"}, expected=202) == {"status": "pending-confirmation"}
            for _ in range(200):
                rows = sql("SELECT state,attempts FROM email_outbox WHERE recipient='captured@runtime.example.test'")
                if rows and rows[0] == ("accepted", 1):
                    break
                time.sleep(.05)
            else:
                raise AssertionError("HTTP capture did not reach fake provider")
            captured = sql("SELECT user_id FROM users WHERE email_key='captured@runtime.example.test'")[0][0]
        with server():
            assert sql("SELECT user_id FROM users WHERE email_key='captured@runtime.example.test'")[0][0] == captured
            assert sql("SELECT attempts FROM email_outbox WHERE recipient='captured@runtime.example.test'") == [(1,)]
            call("/api/v1/newsletter/unsubscribe", {"proof": authority["pending"]["unsubscribe"]})
            # Prove the actual one-minute scheduler, rather than a direct sweep
            # helper. The still-authorized captured contact is the control.
            deadline = time.monotonic() + 80
            while time.monotonic() < deadline:
                reduced = sql("SELECT newsletter_minimised,email,email_key,username FROM users WHERE user_id=?",
                              (authority["pending"]["userID"],))
                flags = sql("SELECT physical_cleanup_pending,legacy_vacuum_pending FROM newsletter_identity_namespace")
                if reduced == [(1, None, None, None)] and flags == [(0, 0)]:
                    break
                time.sleep(.25)
            else:
                raise AssertionError("one-minute inactive cleanup did not complete")
            for suffix in ("", "-wal", "-shm"):
                active = Path(str(db) + suffix)
                assert not active.exists() or b"pending@runtime.example.test" not in active.read_bytes(), "obsolete plaintext in active files"
            assert sql("SELECT user_id FROM users WHERE email_key='captured@runtime.example.test'") == [(captured,)]
        checks.append("actual one-minute cleanup, active-file removal and authorized control")
        checks.append("backend-only activation, fake dispatch, stable ID/restart, pause and withdrawal")
        target = authority["erased"]
        actor = json.dumps(target["authority"]).encode()
        flags = ["--db", db, "--lifecycle-db", journal, "--user-id", str(target["userID"])]
        export = state / "contact-export.json"
        run(binary, "newsletter", "export", *flags, "--output", export, data=actor)
        assert export.stat().st_mode & 0o777 == 0o600
        run(binary, "newsletter", "erase", *flags, data=actor)
        restored = state / "restored.db"
        run(migrate, "restore", "--db", restored, "--lifecycle-db", journal, "--file", snapshot)
        assert hashlib.sha256(snapshot.read_bytes()).digest() == snapshot_digest, "retained source changed"
        assert sql("SELECT count(*) FROM signup_proofs WHERE id='restore-expired'", database=restored) == [(0,)]
        assert sql("SELECT count(*) FROM email_outbox WHERE key='signup/restore-expired'", database=restored) == [(0,)]
        for suffix in ("", "-wal", "-shm"):
            active = Path(str(restored) + suffix)
            assert not active.exists() or expired_canary.encode() not in active.read_bytes(), "restore published expired account plaintext"
        checks.append("restore expires issued account proof/mail before publication; source unchanged")
        with server(restored):
            assert sql("SELECT count(*) FROM users WHERE user_id=?", (target["userID"],), restored) == [(0,)]
            for name in ("pending", "withdrawn"):
                item = authority[name]
                assert sql("SELECT status FROM newsletter_subscriptions WHERE user_id=?", (item["userID"],), restored) == [("unsubscribed",)]
                for action in ("confirm", "unsubscribe"):
                    # Consumed withdrawal is an idempotent acknowledgement; it
                    # must have no authority to mutate the restored subscription.
                    call("/api/v1/newsletter/" + action, {"proof": item[action]},
                         expected=400 if action == "confirm" else 200)
                    assert sql("SELECT status FROM newsletter_subscriptions WHERE user_id=?",
                               (item["userID"],), restored) == [("unsubscribed",)]
            assert sql("SELECT count(*) FROM email_outbox WHERE state IN ('queued','sending')", database=restored) == [(0,)]
        checks.append("protected export/erase, authoritative restore replay and old-link suppression")
        # A wrong-purpose queue row stops the actual role worker and capture.
        with server(restored):
            now = int(time.time())
            sql("INSERT INTO email_outbox(key,recipient,template,data,state,next_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)",
                ("wrong-purpose", "synthetic@runtime.example.test", "provider-smoke-test/v1", "{}", "queued", now, now, now), database=restored)
            for _ in range(200):
                report = call("/internal/newsletter/readiness")
                if not report["checks"]["worker"]:
                    break
                time.sleep(.05)
            else:
                raise AssertionError("wrong-purpose dispatch did not fail readiness")
            call("/api/v1/newsletter/subscribe", {}, expected=503)
            assert sql("SELECT state,reason,attempts FROM email_outbox WHERE key='wrong-purpose'", database=restored) == [("failed", "newsletter_role_template", 0)]
        with server(restored):
            assert not call("/internal/newsletter/readiness")["ready"]
        checks.append("failed worker fences capture and compatible paused restart recovers")
        assert token.encode() not in (state / "process.log").read_bytes(), "credential in diagnostics"
    print(json.dumps({"role": "newsletter", "synthetic": True, "checks": checks,
                      "realDeliveryVerified": False, "productionChanged": False}))


if __name__ == "__main__":
    main()
