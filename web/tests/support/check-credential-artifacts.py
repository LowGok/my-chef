"""Check that an intentional credential test failure leaves no private state."""

import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[3]


def main() -> None:
    canary = secrets.token_urlsafe(24)
    capability = secrets.token_urlsafe(32)
    email = f"recovery-{secrets.token_hex(8)}@example.test"
    proof_link = re.compile(rb"/app/(?:signup|verify-email|change-email|reset-password)#[A-Za-z0-9_-]{43}")
    password_hash = re.compile(rb"\$argon2id\$v=\d+\$|\$2[aby]\$\d\d\$[A-Za-z0-9./]{53}")

    with tempfile.TemporaryDirectory(prefix="mychef-credential-probe-") as scratch:
        root = Path(scratch)
        temporary = root / "tmp"
        temporary.mkdir(mode=0o700)
        results = root / "results"
        manifest = root / "recovery-proof"
        env = {
            **os.environ,
            "TMPDIR": str(temporary),
            "MYCHEF_ARTIFACT_FAILURE": "1",
            "MYCHEF_ARTIFACT_TEST_PASSWORD": canary,
            "MYCHEF_ARTIFACT_TEST_EMAIL": email,
            "MYCHEF_ARTIFACT_PROOF_FILE": str(manifest),
            "MYCHEF_EMAIL_MODE": "fake",
            "MYCHEF_TEST_INBOX_CAPABILITY": capability,
        }
        result = subprocess.run(
            [sys.executable, "scripts/rewrite.py", "e2e", "password-change.spec.ts",
             "email-verification.spec.ts", "password-recovery.spec.ts", "--grep",
             "credential (artifact|transport|recovery) failure probe", "--output", str(results)],
            cwd=ROOT, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            check=False, timeout=150,
        )
        proof = manifest.read_bytes() if manifest.is_file() else b""
        if not re.fullmatch(rb"[A-Za-z0-9_-]{43}", proof):
            raise SystemExit("The browser did not capture the synthetic recovery proof.")
        manifest.unlink()
        forbidden = [canary.encode(), capability.encode(), email.encode(), proof]
        def contains_secret(data: bytes) -> bool:
            return bool(proof_link.search(data) or password_hash.search(data)) or any(value in data for value in forbidden)
        if contains_secret(result.stdout):
            raise SystemExit("A synthetic credential appeared in browser test output.")
        expected = (b"Injected failure after credential submission", b"The isolated fixture request failed.",
                    b"Injected recovery failure after reset")
        if not result.returncode or any(message not in result.stdout for message in expected):
            raise SystemExit("The browser did not reach all expected credential failures.")
        if any(temporary.glob("mychef-e2e-*")):
            raise SystemExit("The temporary test database survived browser shutdown.")
        for path in results.rglob("*"):
            if not path.is_file():
                continue
            if path != results / ".last-run.json" or contains_secret(path.read_bytes()):
                raise SystemExit("The failing browser test retained unexpected artifacts.")
            if json.loads(path.read_text()).get("status") != "failed":
                raise SystemExit("Browser run metadata did not record the intended failure.")
    print("The expected browser failure left no credential artifacts or temporary SQLite state.")


if __name__ == "__main__":
    main()
