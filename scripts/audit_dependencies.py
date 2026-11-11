"""Audit the hash-pinned runtime and development packages exported from uv.lock."""

from pathlib import Path
import os
import subprocess
import sys
import tempfile


ROOT = Path(__file__).resolve().parents[1]


def main():
    with tempfile.TemporaryDirectory(prefix="mychef-audit-") as temporary:
        requirements = Path(temporary) / "requirements.txt"
        subprocess.run([os.environ.get("UV", "uv"), "export", "--locked", "--all-groups",
                        "--no-emit-project", "--output-file", str(requirements), "--quiet"],
                       cwd=ROOT, check=True)
        subprocess.run([sys.executable, "-m", "pip_audit", "--disable-pip",
                        "--require-hashes", "--strict", "-r", str(requirements)],
                       cwd=ROOT, check=True)


if __name__ == "__main__":
    main()
