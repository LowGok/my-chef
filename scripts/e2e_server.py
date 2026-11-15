"""Start the candidate against an isolated SQLite database for browser tests."""
import base64
from contextlib import contextmanager
import http.client
import socket
import time
from datetime import datetime, timedelta, timezone
import json
import os
from pathlib import Path
import secrets
import signal
import subprocess
import tempfile

def fixture_email(directory: Path, capability: str | None = None) -> tuple[dict[str, str], str]:
    """Create an isolated, expiring fake inbox and an external proof keyring."""
    capability = capability or secrets.token_urlsafe(32)
    inbox = directory / "fixture-inbox.json"
    keys = directory / "proof-keys.json"
    for path, document in (
        (inbox, {"capability": capability,
                 "expires": (datetime.now(timezone.utc) + timedelta(minutes=29)).isoformat()}),
        (keys, {"active": "fixture", "keys": {"fixture": base64.b64encode(secrets.token_bytes(32)).decode()},
                "rate_key": base64.b64encode(secrets.token_bytes(32)).decode()}),
    ):
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w") as destination:
            json.dump(document, destination)
    return {"MYCHEF_FIXTURE_INBOX_FILE": str(inbox), "MYCHEF_EMAIL_PROOF_KEYS_FILE": str(keys),
            "MYCHEF_FIXTURE_PHOTOS_DIR": str(directory / "fixture-photos"),
            "MYCHEF_EMAIL_MODE": "fake"}, capability


@contextmanager
def fixture_imports(root: Path, directory: Path, app_env: dict[str, str]):
    """Run the current private worker with expiring synthetic services only."""
    from rewrite import environment
    installed = environment()
    allowed = {"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "TZ",
               "CGO_ENABLED", "GOCACHE", "GOMODCACHE", "GOFLAGS", "GOTMPDIR"}
    worker_env = {key: value for key, value in installed.items() if key in allowed}
    worker_env["MYCHEF_E2E"] = "1"
    state = directory / "import-worker-state"
    channel = directory / "import-channel"
    state.mkdir(mode=0o700)
    channel.mkdir(mode=0o700)
    binary = state / "worker"
    ledger_key, channel_key = secrets.token_bytes(32), secrets.token_bytes(32)
    ledger_key_path, channel_key_path = state / "ledger.key", channel / "channel.key"
    for path, value in ((ledger_key_path, ledger_key), (channel_key_path, channel_key)):
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "wb") as destination:
            destination.write(value)
    deadline = json.loads(Path(app_env["MYCHEF_FIXTURE_INBOX_FILE"]).read_text())["expires"]
    policy_path = state / "policy.json"
    ledger_path = state / "admission.db"
    socket_path = channel / "import-worker.sock"

    def command(arguments: list[str]) -> str:
        result = subprocess.run(arguments, cwd=root, env=worker_env,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        if result.returncode:
            raise RuntimeError("Synthetic import worker setup failed.")
        return result.stdout

    command(["go", "build", "-trimpath", "-o", str(binary), "./cmd/mychef-import-worker"])
    policy = command([str(binary), "fixture-policy", "--fixture-until", deadline])
    fd = os.open(policy_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as destination:
        destination.write(policy)
    identity = json.loads(command([str(binary), "init-ledger", "--ledger", str(ledger_path),
                                   "--ledger-key", str(ledger_key_path)]))["ledgerId"]
    process = subprocess.Popen([str(binary), "serve", "--provider", "fixture",
        "--fixture-until", deadline, "--ledger", str(ledger_path), "--ledger-id", identity,
        "--ledger-key", str(ledger_key_path), "--channel-key", str(channel_key_path),
        "--policy", str(policy_path), "--socket", str(socket_path), "--app-uid", str(os.geteuid())],
        env=worker_env, cwd=root, stdin=subprocess.DEVNULL,
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        ready = False
        until = time.monotonic() + 10
        while time.monotonic() < until and process.poll() is None:
            connection = http.client.HTTPConnection("import-worker", timeout=0.5)
            try:
                connection.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
                connection.sock.settimeout(0.5)
                connection.sock.connect(str(socket_path))
                connection.request("GET", "/readyz", headers={"Authorization": "Bearer " +
                    base64.urlsafe_b64encode(channel_key).decode().rstrip("=")})
                response = connection.getresponse()
                value = json.loads(response.read(4096))
                ready = response.status == 200 and value.get("ready") is True and value.get("fixture") is True and value.get("aiAvailable") is True
                if ready:
                    break
            except (OSError, ValueError, http.client.HTTPException):
                pass
            finally:
                connection.close()
            time.sleep(0.05)
        if not ready:
            raise RuntimeError("Synthetic import worker did not become ready.")
        yield {"MYCHEF_IMPORT_WORKER_SOCKET": str(socket_path),
               "MYCHEF_IMPORT_CHANNEL_KEY_FILE": str(channel_key_path),
               "MYCHEF_IMPORT_TEMP_DIR": str(directory / "import-pages")}
    finally:
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()


def main():
    root = Path(__file__).resolve().parent.parent
    os.umask(0o077)
    with tempfile.TemporaryDirectory(prefix="mychef-e2e-") as directory:
        env = {
            key: value for key, value in os.environ.items()
            if not key.startswith(("MYCHEF_ARTIFACT_", "CLOUDINARY_", "GEMINI_", "RESEND_"))
            and key != "MYCHEF_CREDENTIALS"
        }
        capability = env.pop("MYCHEF_TEST_INBOX_CAPABILITY", None)
        if not capability:
            raise SystemExit("Start this fixture through Playwright.")
        email, _ = fixture_email(Path(directory), capability)
        env.update(email)
        env.update(MYCHEF_ADDR="127.0.0.1:18137", MYCHEF_ORIGIN="http://127.0.0.1:18137",
                   MYCHEF_DB=str(Path(directory) / "test.db"),
                   MYCHEF_LIFECYCLE_DB=str(Path(directory) / "test.db.lifecycle"), MYCHEF_DEV="1",
                   MYCHEF_PROVIDER="fixture", MYCHEF_E2E="1", MYCHEF_PUSH_FIXTURE="1",
                   MYCHEF_FEEDBACK_TO="operator@example.test")
        tools = root / ".test-artifacts/rewrite-tools"
        subprocess.run([str(tools / "mychef-migrate"), "up", "--db", env["MYCHEF_DB"]], check=True)
        with fixture_imports(root, Path(directory), env) as import_env:
            env.update(import_env)
            server = subprocess.Popen([str(tools / "mychef")], env=env, cwd=root)
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
                        server.wait(timeout=15)
                    except subprocess.TimeoutExpired:
                        server.kill()
                        server.wait()


if __name__ == "__main__":
    main()
