#!/usr/bin/env python3
"""Check the final image, private build context, durable storage and rollback."""
from __future__ import annotations

import argparse
from contextlib import closing
import json
import io
import os
from pathlib import Path
import secrets
import sqlite3
import subprocess
import tarfile
import tempfile
import time
import urllib.request

from e2e_server import fixture_email
from rewrite import ROOT, environment
from test_rewrite_menu import audit_credential_artifacts


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--dockerfile", default="Dockerfile")
    parser.add_argument("--scan", action="store_true")
    args = parser.parse_args()
    os.umask(0o077)
    env = environment()
    for key in ("DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"):
        env.pop(key, None)
    docker = ["docker", "--host=unix:///var/run/docker.sock"]
    def output(*command: str) -> str:
        return subprocess.check_output(docker + list(command), env=env, text=True).strip()
    def run(*command: str, **kwargs) -> subprocess.CompletedProcess:
        return subprocess.run(docker + list(command), env=env, check=True, **kwargs)
    suffix = secrets.token_hex(6)
    image = env.get("MYCHEF_CONTAINER_IMAGE") or f"mychef:rewrite-test-{suffix}"
    expected_version = env.get("MYCHEF_RELEASE_VERSION", "0.0.0-test")
    artifacts = ROOT / "test-results/rewrite-container" / suffix
    artifacts.mkdir(parents=True, mode=0o700)
    volumes: list[str] = []
    containers: list[str] = []
    canaries = [ROOT / name for name in (".mychef-context-canary", "web/src/secret.local.json", "internal/store/secret.local.json")]
    created_canaries: list[Path] = []
    private = tempfile.TemporaryDirectory(prefix="mychef-container-state-")
    state = Path(private.name)
    password = secrets.token_urlsafe(24)
    capability = ""
    try:
        email, capability = fixture_email(state)
        for setting in ("MYCHEF_FIXTURE_INBOX_FILE", "MYCHEF_EMAIL_PROOF_KEYS_FILE"):
            # Docker binds these individual files; their parent is still private
            # on the host and the unprivileged container can only read the bind.
            Path(email[setting]).chmod(0o444)
        for file in canaries:
            with file.open("x") as canary:
                canary.write("MYCHEF_PRIVATE_CONTEXT_CANARY")
            created_canaries.append(file)
        with tempfile.TemporaryDirectory(prefix="mychef-context-") as context_dir:
            run("build", "--file", "-", "--output", f"type=local,dest={context_dir}", ".",
                input="FROM scratch\nCOPY . /context\n", text=True, cwd=ROOT)
            paths = [p.relative_to(Path(context_dir) / "context").as_posix() for p in (Path(context_dir) / "context").rglob("*") if p.is_file()]
            assert "go.mod" in paths and "web/src/App.tsx" in paths
            assert "internal/email/service.go" in paths, "email service was excluded from the production build"
            required_password_inputs = {
                "internal/auth/blocklist/10k-most-common.txt", "internal/auth/blocklist/LICENSE",
                "internal/email/mailqueue/queue.go", "internal/email/mailqueue/template.go",
                "internal/email/mailqueue/protected.go", "migrations/009_email_change.sql",
                "internal/config/email_proof.go", "web/src/EmailVerification.tsx",
            }
            assert required_password_inputs.issubset(paths), "required password inputs were excluded from the production build"
            assert "web/public/sw.js" in paths, "offline service worker was excluded from the production build"
            assert {"internal/providers/personal_photo_files.go", "internal/providers/personal_photo_process.go",
                    "internal/http/personal_recipes.go", "web/src/PersonalRecipes.tsx",
                    "migrations/013_personal_recipes.sql", "migrations/014_personal_photos.sql",
                    "migrations/015_personal_planning.sql"}.issubset(paths), "personal recipe inputs were excluded"
            assert {"cmd/mychef-import-worker/main.go", "internal/imports/worker.go",
                    "internal/imports/policy.go", "internal/imports/admission.go",
                    "migrations/016_recipe_imports.sql"}.issubset(paths), "required import worker inputs were excluded"
            assert not any(any(word in p for word in ("secret", "canary", "node_modules", "test-results", ".streamlit", ".git/", "mychefApp", "_test.go")) for p in paths), "private or generated file entered the build context"
        if "MYCHEF_CONTAINER_IMAGE" not in env:
            run("build", "--provenance=false", "--file", args.dockerfile, "--build-arg", f"VERSION={expected_version}", "--tag", image, ".", cwd=ROOT)
        metadata = json.loads(output("image", "inspect", image))[0]
        assert metadata["Config"]["User"] == "nonroot:nonroot"
        assert metadata["Config"]["Entrypoint"] == ["/app/mychef"]
        assert metadata["Config"]["Healthcheck"]["Test"] == ["CMD", "/app/mychef", "healthcheck"]
        filesystem_container = output("create", image)
        containers.append(filesystem_container)
        archive = state / "filesystem.tar"
        run("export", "--output", str(archive), filesystem_container)
        with tarfile.open(archive) as source:
            names = source.getnames()
            assert not any(Path(n).name in ("sh", "bash", "python", "python3", "bun", "go", "apt", "apt-get", "apk", "pip", "uv") for n in names), "unexpected runtime tooling"
            assert not any("credentials" in n or "node_modules" in n or "mychefApp" in n for n in names)
            assert source.getmember("data").uid == 65532
            assert source.getmember("data").mode & 0o777 == 0o700
            assert source.getmember("app/mychef-import-worker").mode & 0o111
            for directory, mode in (("import-state", 0o700), ("import-policy", 0o700), ("import-channel", 0o1770)):
                entry = source.getmember(directory)
                assert entry.uid == 65531 and entry.gid == 65532 and entry.mode & 0o7777 == mode, "worker directory isolation is missing"
        archive.unlink()
        options = ["--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges:true", "--tmpfs", "/tmp:rw,noexec,nosuid,size=64m,uid=65532,gid=65532"]
        run("run", "--rm", *options, "--network=none", "--env", "MYCHEF_DEV=1",
            "--env", "MYCHEF_EMAIL_MODE=fake", image, "email-smoke", "--tracking-off-verified")
        volume = output("volume", "create", f"mychef-rewrite-{suffix}")
        volumes.append(volume)
        mount = ["--mount", f"type=volume,src={volume},dst=/data"]
        run("run", "--rm", *options, *mount, "--entrypoint", "/app/mychef-migrate", image, "up", "--db", "/data/mychef.db")
        worker_volumes = {}
        for name, target in (("state", "/import-state"), ("policy", "/import-policy"), ("channel", "/import-channel")):
            name_value = output("volume", "create", f"mychef-import-{name}-{suffix}")
            volumes.append(name_value)
            worker_volumes[name] = ["--mount", f"type=volume,src={name_value},dst={target}"]
        def copy_private_files(mounts: list[str], target: str, files: dict[str, bytes], uid: int) -> None:
            holder = output("create", "--network=none", "--user", f"{uid}:65532", *mounts, "--entrypoint", "/app/mychef-import-worker", image, "--version")
            containers.append(holder)
            stream = io.BytesIO()
            with tarfile.open(fileobj=stream, mode="w") as archive:
                for name, data in files.items():
                    entry = tarfile.TarInfo(name)
                    entry.size, entry.mode, entry.uid, entry.gid = len(data), 0o400, uid, 65532
                    archive.addfile(entry, io.BytesIO(data))
            run("cp", "--archive", "-", f"{holder}:{target}", input=stream.getvalue())
            copied = subprocess.check_output(docker + ["cp", f"{holder}:{target}", "-"], env=env)
            with tarfile.open(fileobj=io.BytesIO(copied)) as archive:
                for name in files:
                    entry = archive.getmember(target.strip("/") + "/" + name)
                    assert entry.uid == uid and entry.gid == 65532 and entry.mode & 0o777 == 0o400, "private fixture file ownership changed during Docker copy"
        import_worker = "/app/mychef-import-worker"
        worker_options = ["--read-only", "--user", "65531:65532", "--cap-drop=ALL",
                          "--security-opt=no-new-privileges:true", "--network=none", "--no-healthcheck",
                          "--tmpfs", "/tmp:rw,noexec,nosuid,size=16m,uid=65531,gid=65532"]
        policy_mount = ["--mount", worker_volumes["policy"][1] + ",readonly"]
        protected_mounts = [*worker_volumes["state"], *policy_mount, *worker_volumes["channel"]]
        deadline = json.loads(Path(email["MYCHEF_FIXTURE_INBOX_FILE"]).read_text())["expires"]
        policy = output("run", "--rm", *worker_options, "--env", "MYCHEF_E2E=1", "--entrypoint", import_worker,
                        image, "fixture-policy", "--fixture-until", deadline)
        ledger_key, channel_key = secrets.token_bytes(32), secrets.token_bytes(32)
        copy_private_files(worker_volumes["policy"], "/import-policy",
                           {"ledger.key": ledger_key, "channel.key": channel_key, "policy.json": policy.encode()}, 65531)
        ledger_id = json.loads(output("run", "--rm", *worker_options, *protected_mounts, "--entrypoint", import_worker,
                                      image, "init-ledger", "--ledger", "/import-state/admission.db",
                                      "--ledger-key", "/import-policy/ledger.key"))["ledgerId"]
        def start_worker() -> str:
            worker = output("run", "--detach", *worker_options, *protected_mounts, "--env", "MYCHEF_E2E=1",
                "--entrypoint", import_worker, image, "serve", "--provider", "fixture", "--fixture-until", deadline,
                "--ledger", "/import-state/admission.db", "--ledger-id", ledger_id,
                "--ledger-key", "/import-policy/ledger.key", "--channel-key", "/import-policy/channel.key",
                "--policy", "/import-policy/policy.json", "--socket", "/import-channel/import-worker.sock", "--app-uid", "65532")
            containers.append(worker)
            metadata = json.loads(output("inspect", worker))[0]
            assert metadata["Config"]["User"] == "65531:65532" and not metadata["HostConfig"].get("PortBindings")
            assert metadata["HostConfig"]["NetworkMode"] == "none"
            assert not any(mount["Destination"] == "/data" for mount in metadata["Mounts"])
            return worker
        worker = start_worker()
        # Even with deliberate read-only mounts for these probes, the web UID
        # cannot read the worker's service key or open its admission database.
        probe_mounts = ["--mount", worker_volumes["state"][1] + ",readonly", *policy_mount]
        for entrypoint, arguments in (
            (import_worker, ["init-ledger", "--ledger", "/tmp/forbidden.db", "--ledger-key", "/import-policy/ledger.key"]),
            ("/app/mychef-migrate", ["backup", "--db", "/import-state/admission.db", "--file", "/tmp/forbidden.db"]),
        ):
            denied = subprocess.run(docker + ["run", "--rm", *options, "--network=none", *probe_mounts,
                                    "--entrypoint", entrypoint, image, *arguments], env=env,
                                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            assert denied.returncode != 0, "web UID could access protected worker state"
        def start(storage: str, *, inbox: bool = False, lifecycle_volume: str | None = None) -> str:
            settings = ("MYCHEF_EMAIL_PROOF_KEYS_FILE", "MYCHEF_FIXTURE_INBOX_FILE") if inbox else ("MYCHEF_EMAIL_PROOF_KEYS_FILE",)
            email_mounts = []
            for setting in settings:
                host_file = Path(email[setting])
                target = "/tmp/" + host_file.name
                email_mounts.extend(["--mount", f"type=bind,src={host_file},dst={target},readonly", "--env", f"{setting}={target}"])
            lifecycle_mounts = [] if lifecycle_volume is None else [
                "--mount", f"type=volume,src={lifecycle_volume},dst=/journal",
                "--env", "MYCHEF_LIFECYCLE_DB=/journal/mychef.db.lifecycle",
            ]
            photo_settings = [] if not inbox else ["--env", "MYCHEF_FIXTURE_PHOTOS_DIR=" +
                ("/journal/fixture-photos" if lifecycle_volume else "/data/fixture-photos")]
            copy_private_files(["--mount", f"type=volume,src={storage},dst=/data"], "/data", {"import-channel.key": channel_key}, 65532)
            import_mounts = [*worker_volumes["channel"], "--env", "MYCHEF_IMPORT_WORKER_SOCKET=/import-channel/import-worker.sock",
                             "--env", "MYCHEF_IMPORT_CHANNEL_KEY_FILE=/data/import-channel.key",
                             "--env", "MYCHEF_IMPORT_TEMP_DIR=/tmp/import-pages"]
            container = output("run", "--detach", *options, *import_mounts, *lifecycle_mounts, "--network=host", "--mount", f"type=volume,src={storage},dst=/data",
                               "--env", "MYCHEF_ADDR=127.0.0.1:18138", "--env", "MYCHEF_ORIGIN=http://127.0.0.1:18138",
                               "--env", "MYCHEF_DEV=1", "--env", "MYCHEF_EMAIL_MODE=fake",
                               "--env", "MYCHEF_E2E=1", "--env", "MYCHEF_PROVIDER=fixture", *email_mounts, *photo_settings, image)
            containers.append(container)
            mounts = json.loads(output("inspect", container))[0]["Mounts"]
            assert not any(mount["Destination"] in ("/import-state", "/import-policy") for mount in mounts)
            deadline = time.monotonic() + 25
            while time.monotonic() < deadline:
                try:
                    with urllib.request.urlopen("http://127.0.0.1:18138/readyz", timeout=1) as response:
                        if response.status == 200:
                            break
                except OSError:
                    time.sleep(0.1)
            else:
                raise RuntimeError("candidate readiness timed out")
            run("exec", container, "/app/mychef", "healthcheck")
            with urllib.request.urlopen("http://127.0.0.1:18138/api/v1/version", timeout=2) as response:
                assert json.load(response)["version"] == expected_version
            with urllib.request.urlopen("http://127.0.0.1:18138/app/sw.js", timeout=2) as response:
                assert response.status == 200
                assert "javascript" in response.headers.get("Content-Type", "")
                assert "self.addEventListener" in response.read().decode()
            return container
        def stop(container: str) -> None:
            run("stop", "--time", "15", container, stdout=subprocess.DEVNULL)
            assert json.loads(output("inspect", container))[0]["State"]["ExitCode"] == 0, "SIGTERM was not graceful"
        browser_env = env.copy()
        browser_env.pop("MYCHEF_TEST_ACCOUNT_READY", None)
        browser_env.update(MYCHEF_LIVE_ARTIFACTS=str(artifacts), MYCHEF_LIVE_STATE=str(state),
                           MYCHEF_RUN_REWRITE_E2E="1", MYCHEF_TEST_PASSWORD=password,
                           MYCHEF_TEST_INBOX_CAPABILITY=capability)
        command = ["bun", "run", "e2e", "--config", "playwright.live.config.ts", "--grep"]
        personal_env = dict(browser_env, MYCHEF_CONTAINER_FIXTURE="1")
        personal_command = ["bun", "run", "e2e", "container/personal.spec.ts", "--grep"]
        def personal(phase: str) -> None:
            subprocess.run(personal_command + [phase], env=personal_env, cwd=ROOT / "web", check=True)
        import_command = ["bun", "run", "e2e", "container/imports.spec.ts", "--grep"]
        def imported(phase: str) -> None:
            subprocess.run(import_command + [phase], env=personal_env, cwd=ROOT / "web", check=True)
        first = start(volume, inbox=True)
        assert subprocess.run(docker + ["exec", first, "/app/mychef-migrate", "up", "--db", "/forbidden.db"], env=env,
                              stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode != 0
        run("exec", first, "/app/mychef-migrate", "up", "--db", "/tmp/scratch.db", stdout=subprocess.DEVNULL)
        subprocess.run(command + ["generate and inspect"], env=browser_env, cwd=ROOT / "web", check=True)
        personal("personal create")
        imported("imports create")
        # docker cp omits tmpfs. Inspect it through the target's PID namespace
        # with the already pinned build image, the same UID, no network/caps
        # and a read-only helper; no tooling enters the application image.
        inspector = next(line.split()[1] for line in (ROOT / args.dockerfile).read_text().splitlines()
                         if line.startswith("FROM golang:") and line.endswith(" AS build"))
        source_files = output("run", "--rm", "--read-only", "--network=none", "--cap-drop=ALL",
                              "--security-opt=no-new-privileges:true", "--user", "65532:65532",
                              "--pid", f"container:{first}", "--entrypoint", "/usr/bin/find", inspector,
                              "/proc/1/root/tmp/import-pages", "-mindepth", "1", "!", "-name", ".lock", "-print")
        assert not source_files, "recipe source files survived completed transcription"
        run("exec", first, "/app/mychef-migrate", "backup", "--db", "/data/mychef.db", "--file", "/data/online-backup.db")
        stop(first)
        stop(worker)
        worker = start_worker()
        second = start(volume, inbox=True)
        subprocess.run(command + ["restart retains"], env=browser_env, cwd=ROOT / "web", check=True)
        personal("personal restart")
        imported("imports restart")
        stop(second)
        fresh = output("volume", "create", f"mychef-restore-{suffix}")
        volumes.append(fresh)
        run("run", "--rm", *options, "--mount", f"type=volume,src={volume},dst=/backup,readonly",
            "--mount", f"type=volume,src={fresh},dst=/data",
            "--mount", f"type=volume,src={volume},dst=/journal",
            "--entrypoint", "/app/mychef-migrate", image,
            "restore", "--db", "/data/mychef.db", "--file", "/backup/online-backup.db",
            "--lifecycle-db", "/journal/mychef.db.lifecycle")
        restored = start(fresh, inbox=True, lifecycle_volume=volume)
        subprocess.run(command + ["restart retains"], env=browser_env, cwd=ROOT / "web", check=True)
        personal("personal restart")
        imported("imports restart")
        imported("imports account deletion")
        personal("personal account deletion")
        stop(restored)
        run("cp", f"{restored}:/data/mychef.db", str(state / "restored.db"))
        with closing(sqlite3.connect(state / "restored.db")) as database:
            assert database.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
            assert database.execute("PRAGMA foreign_key_check").fetchall() == []
            assert database.execute("SELECT COUNT(*) FROM generations").fetchone()[0] == 1
            assert database.execute("SELECT quantity FROM ingredients").fetchone()[0] == "0.12345678901234567890123456789"
            for table in ("personal_recipes", "personal_recipe_versions", "personal_recipe_queue", "personal_photo_assets", "personal_photo_attempts", "personal_photo_requests", "recipe_import_operations", "personal_recipe_import_sources"):
                assert database.execute(f"SELECT COUNT(*) FROM {table}").fetchone()[0] == 0, "deleted personal data remained"
        replay = output("volume", "create", f"mychef-deleted-restore-{suffix}")
        volumes.append(replay)
        run("run", "--rm", *options, "--mount", f"type=volume,src={volume},dst=/backup,readonly",
            "--mount", f"type=volume,src={replay},dst=/data", "--mount", f"type=volume,src={volume},dst=/journal",
            "--entrypoint", "/app/mychef-migrate", image, "restore", "--db", "/data/mychef.db", "--file", "/backup/online-backup.db",
            "--lifecycle-db", "/journal/mychef.db.lifecycle")
        after_deletion = start(replay, lifecycle_volume=volume)
        personal("personal deletion replay|normal runtime")
        imported("imports deletion replay")
        subprocess.run(command + ["restart retains"], env=browser_env, cwd=ROOT / "web", check=True)
        stop(after_deletion)
        stop(worker)
        ledger_copy = state / "admission.db"
        run("cp", f"{worker}:/import-state/admission.db", str(ledger_copy))
        import_canaries = [value.encode() for value in json.loads((state / "import-state.json").read_text())["canaries"]]
        assert not any(value in ledger_copy.read_bytes() for value in import_canaries), "source content reached the durable admission ledger"
        with closing(sqlite3.connect(ledger_copy)) as admission:
            assert admission.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
            assert admission.execute("SELECT COUNT(*) FROM import_admissions WHERE kind='extract'").fetchone()[0] == 1, "worker restart or app restore changed inference admission"
        if args.scan:
            run("run", "--rm", "--mount", "type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock,readonly",
                "--mount", "type=volume,src=mychef-trivy-cache,dst=/root/.cache/trivy",
                "aquasec/trivy:0.76.1@sha256:790fd66d1697395d8693d1acaa1ea702406a20f109b6fc7bac8db623eca6c06c",
                "image", "--exit-code", "1", "--severity", "HIGH,CRITICAL", "--scanners", "vuln", image)
        result = {"outcome":"passed", "image":image, "imageId":metadata["Id"], "version":expected_version,
                  "checks":["context canaries", "offline worker served", "nonroot shell-free runtime", "read-only root", "writable tmpfs", "health", "browser menu", "personal versions/photos/export", "volume restart", "online backup/fresh-volume restore", "personal deletion replay", "normal runtime fails closed", "separate worker UID", "protected ledger/policy", "ephemeral import sources", "import save/restart/restore/deletion", "independent admission replay", "SIGTERM"], "scan":args.scan}
        (artifacts / "result.json").write_text(json.dumps(result, indent=2))
        print(json.dumps(result))
    finally:
        try:
            personal_state = state / "personal-state.json"
            if personal_state.is_file():
                personal_canaries = [value.encode() for value in json.loads(personal_state.read_text())["canaries"]]
                for file in artifacts.rglob("*"):
                    if file.is_file() and any(value in file.read_bytes() for value in personal_canaries):
                        raise RuntimeError("private personal content entered retained container evidence")
            import_state = state / "import-state.json"
            if import_state.is_file():
                markers = [value.encode() for value in json.loads(import_state.read_text()).get("canaries", [])]
                for file in artifacts.rglob("*"):
                    if file.is_file() and any(value in file.read_bytes() for value in markers):
                        raise RuntimeError("private import content entered retained container evidence")
            audit_credential_artifacts(artifacts, state, state / "restored.db", password, capability)
        finally:
            private.cleanup()
            for file in created_canaries:
                if file.exists() and file.read_text() == "MYCHEF_PRIVATE_CONTEXT_CANARY":
                    file.unlink()
            failed = False
            for container in containers:
                cleaned = subprocess.run(docker + ["rm", "--force", container], env=env, stdout=subprocess.DEVNULL, check=False)
                failed = failed or cleaned.returncode != 0
            for volume in volumes:
                cleaned = subprocess.run(docker + ["volume", "rm", volume], env=env, stdout=subprocess.DEVNULL, check=False)
                failed = failed or cleaned.returncode != 0
            if failed:
                raise RuntimeError("could not remove isolated container test state")


if __name__ == "__main__":
    main()
