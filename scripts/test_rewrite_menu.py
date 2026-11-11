#!/usr/bin/env python3
"""Explicit real-provider browser test, or deterministic restart rehearsal."""
from __future__ import annotations

import argparse
from contextlib import closing
import json
import os
import re
from pathlib import Path
import secrets
import sqlite3
import subprocess
import tempfile
import time
import urllib.request

from e2e_server import fixture_email
from rewrite import CACHE, ROOT, environment


def configure_provider(env: dict[str, str], *, live: bool) -> None:
    """Keep fixture runs credential-free and use the app's JSON contract for live runs."""
    for key in ("GEMINI_API_KEY", "MYCHEF_RECIPE_MODEL", "MYCHEF_IMAGE_MODEL",
                "CLOUDINARY_CLOUD_NAME", "CLOUDINARY_API_KEY", "CLOUDINARY_API_SECRET"):
        env.pop(key, None)
    if not live:
        env.pop("MYCHEF_CREDENTIALS", None)
        env.update(MYCHEF_PROVIDER="fixture", MYCHEF_E2E="1")
        return
    path = env.get("MYCHEF_CREDENTIALS")
    if not path:
        raise ValueError("Set MYCHEF_CREDENTIALS to the existing provider JSON file for --live")
    try:
        source = Path(path).resolve()
        credentials = json.loads(source.read_text())
        fields = ("gemini_api_key", "gemini_recipe_model", "gemini_image_model",
                  "cloudinary_cloud_name", "cloudinary_api_key", "cloudinary_api_secret")
        if (not isinstance(credentials, dict)
                or not isinstance(credentials.get("gemini_api_key"), str)
                or not credentials["gemini_api_key"].strip()
                or any(key in credentials and not isinstance(credentials[key], str) for key in fields)):
            raise ValueError()
    except (OSError, UnicodeError, ValueError):
        raise ValueError("MYCHEF_CREDENTIALS must be readable provider JSON with a nonempty Gemini key") from None
    env.pop("MYCHEF_E2E", None)
    env.update(MYCHEF_PROVIDER="gemini", MYCHEF_CREDENTIALS=str(source))


def audit_credential_artifacts(directory: Path, state: Path, database: Path, password: str, capability: str = "") -> None:
    """Scan retained evidence while the disposable credential canaries still exist."""
    canaries = [value.encode() for value in (password, capability, "isolated-live@example.test") if value]
    proof_link = re.compile(rb"/app/(?:signup|verify-email|change-email)#[A-Za-z0-9_-]{43}")
    session = state / "session.json"
    if session.is_file():
        cookies = json.loads(session.read_text())["cookies"]
        canaries.extend(cookie["value"].encode() for cookie in cookies
                        if cookie["name"] == "mychef_session" and cookie["value"])
    if database.is_file():
        with closing(sqlite3.connect(database.as_uri() + "?mode=ro", uri=True)) as db:
            if db.execute("SELECT 1 FROM sqlite_master WHERE name = 'users' AND type = 'table'").fetchone():
                canaries.extend(value.encode() for (value,) in db.execute("SELECT password FROM users"))
    for file in directory.rglob("*"):
        if not file.is_file():
            continue
        name = file.name.lower()
        if name in ("session.json", "trace.zip", "error-context.md") or name.endswith((".db", ".db-wal", ".db-shm", ".sqlite", ".sqlite3", ".webm")):
            raise RuntimeError("private browser or database state entered retained evidence")
        content = file.read_bytes()
        if content.startswith(b"SQLite format 3\x00") or proof_link.search(content) or any(value in content for value in canaries):
            raise RuntimeError("credential canary found in retained evidence")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--live", action="store_true", help="make one real paid menu request using local service credentials")
    args = parser.parse_args()
    os.umask(0o077)
    directory = ROOT / "test-results" / ("rewrite-live" if args.live else "rewrite-restart") / time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    directory.mkdir(parents=True, mode=0o700)
    with tempfile.TemporaryDirectory(prefix="mychef-restart-state-") as private:
        exercise(directory, Path(private), live=args.live)


def exercise(directory: Path, state: Path, *, live: bool) -> None:
    env = environment()
    env.update(MYCHEF_ADDR="127.0.0.1:18138", MYCHEF_ORIGIN="http://127.0.0.1:18138", MYCHEF_DEV="1",
               MYCHEF_DB=str(state / "test.db"), MYCHEF_RUN_REWRITE_E2E="1",
               MYCHEF_LIVE_ARTIFACTS=str(directory), MYCHEF_LIVE_STATE=str(state),
               MYCHEF_TEST_PASSWORD=secrets.token_urlsafe(24), MYCHEF_EMAIL_MODE="fake",
               MYCHEF_PROVIDER="gemini" if live else "fixture", MYCHEF_TRACE_DIR=str(directory / "provider"))
    env.pop("MYCHEF_TEST_ACCOUNT_READY", None)
    email, capability = fixture_email(state)
    configure_provider(env, live=live)
    env["MYCHEF_EMAIL_PROOF_KEYS_FILE"] = email["MYCHEF_EMAIL_PROOF_KEYS_FILE"]
    env.pop("MYCHEF_FIXTURE_INBOX_FILE", None)
    env.pop("MYCHEF_TEST_INBOX_CAPABILITY", None)
    fixture = env.copy()
    configure_provider(fixture, live=False)
    fixture.update(email)
    subprocess.run([str(CACHE / "mychef-migrate"), "up", "--db", env["MYCHEF_DB"]], env=env, check=True)
    log = (directory / "server.log").open("w")
    server = None
    def stop() -> None:
        if server is not None and server.poll() is None:
            server.terminate()
            try:
                server.wait(timeout=20)
            except subprocess.TimeoutExpired:
                server.kill()
                server.wait()
                raise RuntimeError("server did not shut down gracefully")
    def start(settings: dict[str, str]) -> subprocess.Popen:
        process = subprocess.Popen([str(CACHE / "mychef")], env=settings, stdout=log, stderr=log)
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            if process.poll() is not None:
                raise RuntimeError("server exited; inspect private server.log")
            try:
                with urllib.request.urlopen(env["MYCHEF_ORIGIN"] + "/readyz", timeout=1) as response:
                    if response.status == 200:
                        return process
            except OSError:
                time.sleep(0.1)
        process.terminate()
        process.wait(timeout=20)
        raise RuntimeError("server readiness timed out")
    try:
        server = start(fixture)
        command = ["bun", "run", "e2e", "--config", "playwright.live.config.ts", "--grep"]
        browser = dict(env, MYCHEF_TEST_INBOX_CAPABILITY=capability)
        if live:
            subprocess.run(command + ["prepare isolated provider account"], env=browser, cwd=ROOT / "web", check=True)
            stop()
            server = start(env)
            browser = dict(env, MYCHEF_TEST_ACCOUNT_READY="1")
        subprocess.run(command + ["generate and inspect"], env=browser, cwd=ROOT / "web", check=True)
        stop()
        server = start(env)
        subprocess.run(command + ["restart retains"], env=env, cwd=ROOT / "web", check=True)
        stop()
        with closing(sqlite3.connect(env["MYCHEF_DB"])) as db:
            assert db.execute("SELECT COUNT(*) FROM generations").fetchone()[0] == 1
            saved = json.loads(db.execute("SELECT recipe_json FROM meals").fetchone()[0])
            menu = json.loads((directory / "menu.json").read_text())
            for key, value in saved.items():
                if key not in ("ingredients", "isMeatOrFish"):
                    assert menu["recipes"][0][key] == value, key
            quantities = db.execute("SELECT quantity FROM ingredients ORDER BY pk_ingredients").fetchall()
            assert [q[0] for q in quantities] == [i["quantity"] for i in saved["ingredients"]]
            assert [q[0] for q in quantities] == [i["quantity"] for i in menu["recipes"][0]["ingredients"]]
        model = "fixture"
        if live:
            traces = list((directory / "provider").glob("generation-*"))
            assert len(traces) == 1, "restart or browser replayed a paid call"
            request = json.loads((traces[0] / "request.json").read_text())
            response = json.loads((traces[0] / "response.json").read_text())
            assert response["status"] == 200
            raw = json.loads(response["response"])
            text = "".join(p.get("text", "") for p in raw["candidates"][0]["content"]["parts"] if not p.get("thought"))
            assert json.loads(text)["recipes"][0] == saved, "provider result differs from persisted recipe"
            snapshot = json.loads(request["request"]["contents"][0]["parts"][0]["text"])
            assert snapshot["preferences"]["diet"] == ["Vegetarian"]
            assert snapshot["preferences"]["dislikes"] == ["Mushrooms"]
            assert len(snapshot["dates"]) == 1
            model = request["model"]
        result = {"outcome": "passed", "model": model, "recipes": 1, "imageStatus": menu["recipes"][0]["imageStatus"],
                  "checks": ["browser generation", "rendered instructions and quantities", "provider/persisted equivalence", "reload", "process restart", "one operation"]}
        (directory / "result.json").write_text(json.dumps(result, indent=2))
        print(json.dumps(result))
        print(f"Private evidence: {directory}")
    finally:
        try:
            stop()
        finally:
            log.close()
            audit_credential_artifacts(directory, state, state / "test.db", env["MYCHEF_TEST_PASSWORD"], capability)


if __name__ == "__main__":
    main()
