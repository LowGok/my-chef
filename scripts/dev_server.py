"""Start the Go development app; --fixture uses disposable SQLite and no paid APIs."""
import argparse
from contextlib import nullcontext
import os
from pathlib import Path
import signal
import subprocess
import tempfile

from e2e_server import fixture_email, fixture_imports
from rewrite import CACHE, ROOT, environment


def configure(values: dict[str, str], *, fixture: str | None = None) -> dict[str, str]:
    env = values.copy()
    env.setdefault("MYCHEF_ADDR", "127.0.0.1:8080")
    env.setdefault("MYCHEF_ORIGIN", "http://127.0.0.1:8080")
    env["MYCHEF_DEV"] = "1"
    if fixture is not None:
        env.update(MYCHEF_DB=str(Path(fixture) / "mychef.db"),
                   MYCHEF_LIFECYCLE_DB=str(Path(fixture) / "mychef.db.lifecycle"),
                   MYCHEF_PROVIDER="fixture", MYCHEF_E2E="1")
        for key in list(env):
            if key.startswith("MYCHEF_IMPORT_"):
                env.pop(key)
        for key in ("MYCHEF_CREDENTIALS", "GEMINI_API_KEY", "CLOUDINARY_CLOUD_NAME",
                    "CLOUDINARY_API_KEY", "CLOUDINARY_API_SECRET"):
            env.pop(key, None)
    else:
        if not env.get("MYCHEF_CREDENTIALS"):
            raise ValueError("Set MYCHEF_CREDENTIALS to provider JSON, or use --fixture for a disposable preview")
        env.setdefault("MYCHEF_DB", str(ROOT / "data/mychef.db"))
        env["MYCHEF_PROVIDER"] = "gemini"
        env.pop("MYCHEF_E2E", None)
    return env


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--fixture", action="store_true")
    args = parser.parse_args()
    os.umask(0o077)
    with tempfile.TemporaryDirectory(prefix="mychef-dev-") if args.fixture else nullcontext(None) as directory:
        env = configure(environment(), fixture=directory)
        if directory is not None:
            email, _ = fixture_email(Path(directory))
            env.update(email)
            print(f"Private fixture inbox capability file: {email['MYCHEF_FIXTURE_INBOX_FILE']}", flush=True)
        imports_context = fixture_imports(ROOT, Path(directory), env) if directory is not None else nullcontext({})
        with imports_context as import_env:
            env.update(import_env)
            Path(env["MYCHEF_DB"]).parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            subprocess.run([str(CACHE / "mychef-migrate"), "up", "--db", env["MYCHEF_DB"]], env=env, check=True)
            print("Disposable fixture preview (no paid APIs)." if args.fixture else "Local Go development app.", flush=True)
            server = subprocess.Popen([str(CACHE / "mychef")], cwd=ROOT, env=env)
            def stop(_signal, _frame):
                server.terminate()
            signal.signal(signal.SIGTERM, stop)
            signal.signal(signal.SIGINT, stop)
            try:
                raise SystemExit(server.wait())
            finally:
                if server.poll() is None:
                    server.terminate()
                    try:
                        server.wait(timeout=20)
                    except subprocess.TimeoutExpired:
                        server.kill()
                        server.wait()


if __name__ == "__main__":
    main()
