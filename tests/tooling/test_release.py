"""Offline checks for release authorization, version immutability and registry failures."""

import importlib.util
import json
import subprocess
from pathlib import Path

import httpx
import pytest


spec = importlib.util.spec_from_file_location("container_release",
    Path(__file__).resolve().parents[2] / "scripts/container_release.py")
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


@pytest.mark.parametrize("version", ["latest", "v1.2.3", "01.2.3", "1.2", "1.2.3-rc1", "1.2.3\n", "1.2.3; echo bad"])
def test_release_rejects_non_exact_versions(version):
    with pytest.raises(ValueError):
        release.validate_version(version)


def test_only_original_repository_main_can_publish():
    release.validate_context("refs/heads/main", "LowGok/my-chef")
    for ref, repo in [("refs/heads/feature", "LowGok/my-chef"),
                      ("refs/tags/1.2.3", "LowGok/my-chef"),
                      ("refs/heads/main", "other/myChef_v0")]:
        with pytest.raises(ValueError):
            release.validate_context(ref, repo)


@pytest.mark.parametrize("status,body,allowed", [
    (404, {"errors": [{"code": "MANIFEST_UNKNOWN"}]}, True),
    (404, {"errors": [{"code": "NAME_UNKNOWN"}]}, True),
    (404, {}, False),
    (401, {"errors": [{"code": "UNAUTHORIZED"}]}, False),
    (403, {}, False),
    (429, {}, False),
    (500, {}, False),
    (200, {}, False),
])
def test_only_confirmed_missing_registry_versions_allow_publication(status, body, allowed):
    def handler(request):
        if request.url.path == "/token":
            return httpx.Response(200, json={"token": "synthetic-registry-token"})
        assert request.headers["Authorization"] == "Bearer synthetic-registry-token"
        return httpx.Response(status, json=body)
    with httpx.Client(transport=httpx.MockTransport(handler)) as client:
        if allowed:
            release.confirm_version_available(client, "1.2.3", "synthetic-password")
        else:
            with pytest.raises(RuntimeError):
                release.confirm_version_available(client, "1.2.3", "synthetic-password")


def test_registry_authentication_failure_is_not_a_missing_version():
    with httpx.Client(transport=httpx.MockTransport(lambda _: httpx.Response(403))) as client:
        with pytest.raises(RuntimeError, match="authorization failed"):
            release.confirm_version_available(client, "1.2.3", "synthetic-password")


def test_missing_final_digest_cannot_be_recorded_as_a_release():
    for value in ("", "latest", "sha256:abc"):
        with pytest.raises(ValueError):
            release.image_reference(value)
    digest = "sha256:" + "a" * 64
    assert release.image_reference(digest) == "ghcr.io/lowgok/my-chef@" + digest


def test_signature_verification_remains_enabled_without_github_attestations(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    (tmp_path / "sbom.spdx.json").write_text(json.dumps({
        "spdxVersion": "SPDX-2.3", "SPDXID": "SPDXRef-DOCUMENT"}))
    calls = []
    monkeypatch.setenv("GITHUB_SHA", "b" * 40)
    monkeypatch.setattr(release.subprocess, "run", lambda command, **_: calls.append(command))
    reference = release.image_reference("sha256:" + "a" * 64)
    release.verify(reference)
    assert len(calls) == 1
    assert calls[0][:3] == ["cosign", "verify", reference]
    assert calls[0][calls[0].index("--certificate-github-workflow-sha") + 1] == "b" * 40


def test_existing_tag_must_identify_the_exact_release_commit(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    def git(*arguments):
        return subprocess.check_output(["git", *arguments], text=True, stderr=subprocess.DEVNULL).strip()
    git("init", "-b", "main")
    git("config", "user.name", "Synthetic release test")
    git("config", "user.email", "release@example.test")
    git("config", "commit.gpgsign", "false")
    (tmp_path / "file").write_text("initial")
    git("add", ".")
    git("commit", "-m", "feat: initial")
    git("tag", "1.2.3")
    assert release.tag_matches_commit("1.2.3", git("rev-parse", "HEAD"))
    (tmp_path / "file").write_text("documentation change")
    git("commit", "-am", "docs: describe usage")
    assert not release.tag_matches_commit("1.2.3", git("rev-parse", "HEAD"))
    with pytest.raises(subprocess.CalledProcessError):
        release.tag_matches_commit("9.9.9", git("rev-parse", "HEAD"))


def signing_process(monkeypatch, outcomes):
    calls, pauses = [], []
    def run(command, **options):
        calls.append(command)
        outcome = outcomes.pop(0)
        return subprocess.CompletedProcess(command, outcome[0], stdout="", stderr=outcome[1])
    monkeypatch.setattr(release.subprocess, "run", run)
    monkeypatch.setattr(release.time, "sleep", pauses.append)
    return calls, pauses


CONFLICT = '[POST /api/v1/log/entries][409] createLogEntryConflict'


def test_signing_retries_rekor_conflict_in_fresh_process_then_succeeds(monkeypatch):
    calls, pauses = signing_process(monkeypatch, [(1, CONFLICT), (0, "")])
    reference = release.image_reference("sha256:" + "a" * 64)
    release.sign(reference)
    assert calls == [["cosign", "sign", "--yes", reference]] * 2
    assert pauses == [5]


def test_signing_conflicts_fail_after_three_attempts(monkeypatch, capsys):
    calls, pauses = signing_process(monkeypatch, [(1, CONFLICT)] * 3)
    with pytest.raises(subprocess.CalledProcessError):
        release.sign(release.image_reference("sha256:" + "a" * 64))
    assert len(calls) == 3
    assert pauses == [5, 5]
    assert CONFLICT in capsys.readouterr().err


@pytest.mark.parametrize("error", ["unauthorized", "certificate expired", "HTTP 409 other conflict", "timeout"])
def test_other_signing_failures_are_not_retried(monkeypatch, capsys, error):
    calls, pauses = signing_process(monkeypatch, [(1, error)])
    with pytest.raises(subprocess.CalledProcessError):
        release.sign(release.image_reference("sha256:" + "a" * 64))
    assert len(calls) == 1
    assert pauses == []
    assert error in capsys.readouterr().err


def test_successful_signing_needs_no_retry(monkeypatch):
    calls, pauses = signing_process(monkeypatch, [(0, "")])
    release.sign(release.image_reference("sha256:" + "a" * 64))
    assert len(calls) == 1
    assert pauses == []
