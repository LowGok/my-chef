"""Validate explicit container releases and verify their immutable results."""

import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time

IMAGE = "ghcr.io/lowgok/my-chef-app"
REPOSITORY = "LowGok/my-chef"
OWNER = "LowGok"


def validate_version(version):
    if not re.fullmatch(r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)", version):
        raise ValueError("Use a new exact stable version: MAJOR.MINOR.PATCH")
    return version


def validate_context(ref, repository):
    if ref != "refs/heads/main" or repository != REPOSITORY:
        raise ValueError("Container publication is restricted to this repository's main branch")


def plan_package_operation(*, event, actor, triggering_actor, sha, state,
                           request, expected_sha, approved_sha, attempt):
    if event == "push":
        return "release" if state == "ready" else "hold"
    if event != "workflow_dispatch" or actor != OWNER or triggering_actor != OWNER:
        raise ValueError("Manual package operations require owner dispatch")
    if state != "initializing" or not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise ValueError("Manual package operations require initialization at an exact commit")
    if sha != expected_sha or sha != approved_sha:
        raise ValueError("The dispatch must identify the approved main commit")
    if request not in {"initialize", "release"} or attempt != "1":
        raise ValueError("Use a new authorized dispatch for this package operation")
    return request


def create_marker_context(runner_temp, sha):
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise ValueError("The package marker requires an exact source commit")
    context = runner_temp / "mychef-package-marker"
    context.mkdir()
    (context / "Dockerfile").write_text(
        "FROM scratch\n"
        f'LABEL org.opencontainers.image.source="https://github.com/{REPOSITORY}"\n'
        f'LABEL org.opencontainers.image.revision="{sha}"\n'
        'LABEL org.opencontainers.image.description="MyChef package initialization marker"\n',
    )
    return context


def write_outputs(**values):
    with Path(os.environ["GITHUB_OUTPUT"]).open("a") as output:
        for key, value in values.items():
            output.write(f"{key}={value}\n")


def confirm_version_available(client, version, password):
    """Only explicit registry missing-manifest responses permit publication."""
    name = IMAGE.removeprefix("ghcr.io/")
    token_response = client.get("https://ghcr.io/token", auth=("lowgok", password),
                                params={"service": "ghcr.io", "scope": f"repository:{name}:pull"})
    if token_response.status_code != 200:
        raise RuntimeError(f"Registry authorization failed: HTTP {token_response.status_code}")
    token = token_response.json().get("token")
    if not isinstance(token, str) or not token:
        raise RuntimeError("Registry did not return a usable token")
    manifest = client.get(f"https://ghcr.io/v2/{name}/manifests/{version}", headers={
        "Authorization": "Bearer " + token,
        "Accept": "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json",
    })
    if manifest.status_code == 200:
        raise RuntimeError("This image version already exists; choose a new version")
    if manifest.status_code == 404:
        errors = manifest.json().get("errors", [])
        if errors and all(error.get("code") in {"MANIFEST_UNKNOWN", "NAME_UNKNOWN"} for error in errors):
            return
    raise RuntimeError(f"Cannot establish that this version is unused: HTTP {manifest.status_code}")


def image_reference(digest):
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
        raise ValueError("The shared action did not return a valid final release digest")
    return IMAGE + "@" + digest


def tag_matches_commit(version, commit):
    target = subprocess.check_output(["git", "rev-parse", f"refs/tags/{version}^{{commit}}"], text=True).strip()
    return target == commit


def sign(reference):
    """Retry only Rekor's duplicate-entry error, never accept it as a signature."""
    for attempt in range(3):
        result = subprocess.run(["cosign", "sign", "--yes", reference],
                                capture_output=True, text=True, timeout=180)
        if result.returncode == 0:
            print("Keyless image signing succeeded")
            return
        duplicate = "[POST /api/v1/log/entries][409] createLogEntryConflict" in result.stderr
        if not duplicate or attempt == 2:
            print(result.stderr, file=sys.stderr, end="")
            raise subprocess.CalledProcessError(result.returncode, result.args,
                                                output=result.stdout, stderr=result.stderr)
        print("Rekor reported a duplicate entry; retrying signing with a fresh process")
        time.sleep(5)


def verify(reference):
    identity = f"https://github.com/{REPOSITORY}/.github/workflows/delivery.yml@refs/heads/main"
    sbom = json.loads(Path("sbom.spdx.json").read_text())
    version = re.fullmatch(r"SPDX-([0-9]+\.[0-9]+)", sbom.get("spdxVersion", ""))
    if not version or not sbom.get("SPDXID"):
        raise ValueError("The shared action did not produce a valid SPDX SBOM")
    subprocess.run(["cosign", "verify", reference, "--certificate-identity", identity,
                    "--certificate-oidc-issuer", "https://token.actions.githubusercontent.com",
                    "--certificate-github-workflow-sha", os.environ["GITHUB_SHA"]], check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("plan", "marker", "preflight", "sign", "verify", "record"))
    args = parser.parse_args()
    validate_context(os.environ["GITHUB_REF"], os.environ["GITHUB_REPOSITORY"])
    if args.command == "plan":
        operation = plan_package_operation(
            event=os.environ["GITHUB_EVENT_NAME"], actor=os.environ["GITHUB_ACTOR"],
            triggering_actor=os.environ["GITHUB_TRIGGERING_ACTOR"], sha=os.environ["GITHUB_SHA"],
            state=os.environ.get("PACKAGE_STATE", ""), request=os.environ.get("PACKAGE_OPERATION", ""),
            expected_sha=os.environ.get("PACKAGE_EXPECTED_SHA", ""),
            approved_sha=os.environ.get("PACKAGE_APPROVED_SHA", ""), attempt=os.environ["GITHUB_RUN_ATTEMPT"],
        )
        write_outputs(operation=operation, project=IMAGE.rsplit("/", 1)[1], image=IMAGE)
        print(f"Package operation: {operation}")
        return
    if args.command == "marker":
        context = create_marker_context(Path(os.environ["RUNNER_TEMP"]), os.environ["GITHUB_SHA"])
        write_outputs(context=context)
        return
    version = validate_version(os.environ["MYCHEF_RELEASE_VERSION"])
    if args.command == "preflight":
        if not tag_matches_commit(version, os.environ["GITHUB_SHA"]):
            write_outputs(publish="false")
            print("No new software version: its existing tag identifies an earlier commit")
            return
        import httpx

        with httpx.Client(timeout=20) as client:
            confirm_version_available(client, version, os.environ["GH_TOKEN"])
        write_outputs(publish="true")
        print(f"Release preflight passed for {IMAGE}:{version}")
        return
    reference = image_reference(os.environ["MYCHEF_RELEASE_DIGEST"])
    if args.command == "sign":
        sign(reference)
        return
    if args.command == "verify":
        verify(reference)
        return
    result = {"version": version, "image": reference, "commit": os.environ["GITHUB_SHA"]}
    (Path(os.environ["RUNNER_TEMP"]) / "container-release.json").write_text(json.dumps(result, indent=2))
    with Path(os.environ["GITHUB_STEP_SUMMARY"]).open("a") as summary:
        summary.write(f"\nVerified container {version}: `{reference}`\n")
    print(reference)


if __name__ == "__main__":
    main()
