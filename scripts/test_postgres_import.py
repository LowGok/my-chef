#!/usr/bin/env python3
"""Exercise the importer against a disposable, loopback-only PostgreSQL server."""
from __future__ import annotations

import os
import secrets
import subprocess
import time
from urllib.parse import quote

from rewrite import ROOT, environment

IMAGE = "postgres:18.6-alpine@sha256:3b0ef735388b2cb097fc29b8bb421d86d57d0ddbe7aa1fbd6493e06d65028503"


def main() -> None:
    os.umask(0o077)
    env = environment()
    for key in ("DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"):
        env.pop(key, None)
    docker = ["docker", "--host=unix:///var/run/docker.sock"]
    env["POSTGRES_PASSWORD"] = secrets.token_urlsafe(32)
    container = subprocess.check_output(
        docker + ["run", "--rm", "--detach", "--memory=512m", "--cpus=2",
                  "--publish", "127.0.0.1::5432", "--tmpfs", "/var/lib/postgresql:rw,noexec,nosuid,size=256m",
                  "--env", "POSTGRES_PASSWORD", "--env", "POSTGRES_DB=mychef_test", IMAGE],
        env=env, text=True,
    ).strip()
    try:
        deadline = time.monotonic() + 45
        while subprocess.run(docker + ["exec", container, "pg_isready", "-U", "postgres", "-d", "mychef_test"],
                             env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode:
            if time.monotonic() >= deadline:
                raise SystemExit("Disposable PostgreSQL did not become ready")
            time.sleep(0.25)
        port = subprocess.check_output(docker + ["port", container, "5432/tcp"], env=env, text=True).strip().rsplit(":", 1)[1]
        env["MYCHEF_TEST_POSTGRES"] = "1"
        env["MYCHEF_TEST_POSTGRES_URL"] = f"postgres://postgres:{quote(env['POSTGRES_PASSWORD'])}@127.0.0.1:{port}/mychef_test?sslmode=disable"
        subprocess.run(["go", "test", "./internal/importer", "-count=1", "-v"], cwd=ROOT, env=env, check=True)
    finally:
        subprocess.run(docker + ["rm", "--force", container], env=env, stdout=subprocess.DEVNULL, check=True)


if __name__ == "__main__":
    main()
