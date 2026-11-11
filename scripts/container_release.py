"""Validate explicit container releases and verify their immutable results."""

import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time

import httpx


IMAGE = "ghcr.io/lowgok/my-chef"
REPOSITORY = "LowGok/my-chef"


def validate_version(version):
    if not re.fullmatch(r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)", version):
        raise ValueError("Use a new exact stable version: MAJOR.MINOR.PATCH")
    return version


def validate_context(ref, repository):
    if ref != "refs/heads/main" or repository != REPOSITORY:
        raise ValueError("Container publication is restricted to this repository's main branch")


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
    parser.add_argument("command", choices=("preflight", "sign", "verify", "record"))
    args = parser.parse_args()
    validate_context(os.environ["GITHUB_REF"], os.environ["GITHUB_REPOSITORY"])
    version = validate_version(os.environ["MYCHEF_RELEASE_VERSION"])
    if args.command == "preflight":
        if not tag_matches_commit(version, os.environ["GITHUB_SHA"]):
            with Path(os.environ["GITHUB_OUTPUT"]).open("a") as output:
                output.write("publish=false\n")
            print("No new software version: its existing tag identifies an earlier commit")
            return
        with httpx.Client(timeout=20) as client:
            confirm_version_available(client, version, os.environ["GH_TOKEN"])
        with Path(os.environ["GITHUB_OUTPUT"]).open("a") as output:
            output.write("publish=true\n")
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
