#!/usr/bin/env python3
"""Run the pinned Go/Bun toolchain without changing the host installation."""
from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import zipfile

ROOT = Path(__file__).resolve().parent.parent
CACHE = ROOT / ".test-artifacts/rewrite-tools"
LOCK = json.loads((ROOT / "scripts/rewrite-tools.json").read_text())
BINARIES = {
    "go": CACHE / "go/bin/go",
    "bun": CACHE / "bun-linux-x64/bun",
    "golangci-lint": CACHE / f"golangci-lint-{LOCK['golangci-lint']['version']}-linux-amd64/golangci-lint",
}


def bootstrap() -> None:
    CACHE.mkdir(parents=True, exist_ok=True)
    for name, binary in BINARIES.items():
        if binary.exists():
            continue
        spec = LOCK[name]
        archive = CACHE / (name + (".zip" if name == "bun" else ".tar.gz"))
        subprocess.run(["curl", "-4", "--fail", "--location", "--retry", "2", "--max-time", "180", spec["url"], "-o", str(archive)], check=True)
        if hashlib.sha256(archive.read_bytes()).hexdigest() != spec["sha256"]:
            raise SystemExit(f"{name}: download checksum mismatch")
        if name == "bun":
            with zipfile.ZipFile(archive) as source:
                for member in source.infolist():
                    target = (CACHE / member.filename).resolve()
                    if not target.is_relative_to(CACHE.resolve()):
                        raise SystemExit("unsafe tool archive")
                source.extractall(CACHE)
            binary.chmod(0o755)
        else:
            with tarfile.open(archive) as source:
                source.extractall(CACHE, filter="data")


def environment() -> dict[str, str]:
    env = os.environ.copy()
    paths = []
    for name, binary in BINARIES.items():
        selected = binary if binary.exists() else Path(shutil.which(name) or "/missing")
        if not selected.exists():
            raise SystemExit(f"{name} unavailable; run python3 scripts/rewrite.py bootstrap")
        args = [str(selected), "version"] if name != "bun" else [str(selected), "--version"]
        value = subprocess.check_output(args, text=True)
        if LOCK[name]["version"] not in value.split() and (name != "go" or "go" + LOCK[name]["version"] not in value.split()):
            raise SystemExit(f"{name}: expected {LOCK[name]['version']}, got {value.strip()}")
        paths.append(str(selected.parent))
    env["PATH"] = os.pathsep.join(paths + [env.get("PATH", "")])
    env["GOCACHE"] = str(CACHE / "go-cache")
    env["GOMODCACHE"] = str(CACHE / "go-mod")
    env["GOLANGCI_LINT_CACHE"] = str(CACHE / "lint-cache")
    env["BUN_INSTALL_CACHE_DIR"] = str(CACHE / "bun-cache")
    env["CGO_ENABLED"] = "0"
    return env


def main() -> None:
    os.umask(0o077)
    command = sys.argv[1] if len(sys.argv) > 1 else "test"
    if command == "bootstrap":
        bootstrap()
        environment()
        return
    env = environment()

    def run(*args: str, web: bool = False) -> None:
        subprocess.run(args, cwd=ROOT / "web" if web else ROOT, env=env, check=True)

    if command == "build":
        run("bun", "install", "--frozen-lockfile", web=True)
        run("bun", "run", "build", web=True)
        run("go", "build", "-trimpath", "-buildvcs=false", "-o", str(CACHE / "mychef"), "./cmd/mychef")
        run("go", "build", "-trimpath", "-buildvcs=false", "-o", str(CACHE / "mychef-migrate"), "./cmd/mychef-migrate")
    elif command == "test":
        run("python3", "scripts/test_native_verification.py")
        run("go", "test", "./...")
        run("bun", "--bun", "./node_modules/typescript/bin/tsc", "--noEmit",
            "--module", "preserve", "--moduleResolution", "bundler", "--target", "ESNext",
            "--types", "bun-types", "--typeRoots", "./node_modules", "--skipLibCheck",
            "../scripts/audit-web.ts", "../scripts/audit-web.test.ts",
            "../scripts/mobile-build.ts", "../scripts/mobile-build.test.ts",
            "../scripts/mobile-java.test.ts", "../scripts/mobile-delivery.ts",
            "../scripts/mobile-delivery.test.ts", "../scripts/mobile-hosted.ts",
            "../scripts/mobile-hosted.test.ts",
            "../scripts/native-android-journeys.ts",
            "../scripts/native-android-report.ts", "../scripts/native-android-report.test.ts",
            "generate.ts", "../scripts/generate-api.test.ts", web=True)
        run("bun", "test", "src", "../scripts/audit-web.test.ts",
            "../scripts/generate-api.test.ts", "../scripts/mobile-build.test.ts",
            "../scripts/mobile-java.test.ts", "../scripts/mobile-delivery.test.ts",
            "../scripts/mobile-hosted.test.ts", "../scripts/native-android-report.test.ts", web=True)
    elif command == "lint":
        run("go", "vet", "./...")
        run("golangci-lint", "config", "verify")
        run("golangci-lint", "run")
    elif command == "e2e":
        arguments = sys.argv[2:]
        if arguments:
            run("bun", "run", "e2e", *arguments, web=True)
        else:
            # Each Playwright invocation owns a fresh fixture server/database.
            # Separate specs must not consume each other's account rate limits.
            specs = sorted((ROOT / "web/tests").glob("*.spec.ts"))
            if not specs:
                raise SystemExit("No browser specs found")
            for spec in specs:
                run("bun", "run", "e2e", spec.name, web=True)
    elif command == "generate":
        run("bun", "run", "generate", web=True)
    elif command == "audit":
        run("go", "mod", "verify")
        run("go", "run", "golang.org/x/vuln/cmd/govulncheck@v1.9.0", "./...")
        run("bun", "run", "../scripts/audit-web.ts", web=True)
    else:
        raise SystemExit(f"unknown command: {command}")


if __name__ == "__main__":
    main()
