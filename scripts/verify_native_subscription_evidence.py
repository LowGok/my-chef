"""Create the only public artifact from a successful native subscription fixture."""
import hashlib
import json
import os
from pathlib import Path
import re

COMPILED = {
    "android": "mychef-compile.apk",
    "ios-simulator": "mychef-compile-simulator.app.zip",
}
SMOKE = ["install and launch", "native session over verified HTTPS", "refresh after process relaunch"]
BILLING = {
    "android": ["Android large text and Play purchase controls are accessible",
                "Play restores the lost simulated result before completion"],
    "ios-simulator": ["enlarged price controls", "verified simulated Apple purchase", "simulated Apple restore"],
}
PRIVATE = re.compile(
    r"Bearer\s+|fixture\.[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}|"
    r"[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}|"
    r"https?://|\.ts\.net\b|/(?:Users|home)/|"
    r"-----BEGIN [^\n-]*PRIVATE KEY-----|AIza[A-Za-z0-9_-]{35}|"
    r"(?:github_pat_|gh[pousr]_|re_|sk_live_|sk_test_)[A-Za-z0-9_-]{20,}",
    re.IGNORECASE,
)


def require(valid):
    if not valid:
        raise ValueError("Subscription fixture evidence was incomplete, mismatched, or private.")


def unique_object(pairs):
    result = {}
    for name, value in pairs:
        require(name not in result)
        result[name] = value
    return result


def load(root, name):
    path = root / name
    require(path.is_file() and not path.is_symlink() and 0 < path.stat().st_size <= 16384)
    value = json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=unique_object)
    require(isinstance(value, dict) and PRIVATE.search(json.dumps(value, ensure_ascii=False)) is None)
    return value


def evidence(root, platform, sha):
    require(platform in COMPILED and isinstance(sha, str) and re.fullmatch(r"[a-f0-9]{40}", sha))
    build = load(root, "compilation.json")
    smoke = load(root, "installed-smoke.json")
    journey = load(root, "installed-journeys.json")
    source = build.get("candidate")
    require(isinstance(source, dict) and source.get("sourceCommit") == sha
            and source.get("appId") == "com.gkldevelopment.mychef.preview"
            and source.get("environment") == "preview" and source.get("nativeResolutionRecorded") is True
            and isinstance(source.get("version"), str) and re.fullmatch(r"[0-9]{1,9}(?:\.[0-9]{1,9}){2}", source["version"])
            and type(source.get("build")) is int and 0 < source["build"] <= 2147483647)
    for name in ("bundleSha256", "dependenciesSha256"):
        require(isinstance(source.get(name), str) and re.fullmatch(r"[a-f0-9]{64}", source[name]))
    require(build.get("platform") == platform and build.get("artifact") == COMPILED[platform]
            and build.get("compilation") == "passed" and build.get("dependencyVerification") == "locked"
            and build.get("distribution") == "not authorized")
    artifact = root / COMPILED[platform]
    require(artifact.is_file() and not artifact.is_symlink() and 0 < artifact.stat().st_size <= 64 * 1024 * 1024)
    digest = hashlib.sha256()
    with artifact.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    checksum = digest.hexdigest()
    require(build.get("artifactSha256") == checksum and smoke.get("platform") == platform)
    for run in (smoke, journey):
        device = run.get("device")
        require(run.get("source") == source and run.get("artifactSha256") == checksum and isinstance(device, dict))
        if platform == "android":
            require(device.get("api") == "33")
        else:
            require(device.get("model") == "iPhone 16"
                    and device.get("runtime") == "com.apple.CoreSimulator.SimRuntime.iOS-18-6")
    require(smoke.get("passed") == SMOKE and journey.get("passed") == BILLING[platform]
            and journey.get("groups") == {"billing": BILLING[platform]})
    return {
        "kind": "synthetic native subscription fixture",
        "platform": platform, "sourceCommit": sha, "artifactSha256": checksum,
        "bundleSha256": source["bundleSha256"], "dependenciesSha256": source["dependenciesSha256"],
        "version": source["version"], "build": source["build"],
        "smoke": SMOKE, "billing": BILLING[platform],
    }


def main():
    root = Path("mobile/.generated/artifacts")
    try:
        result = evidence(root, os.environ.get("NATIVE_PLATFORM"), os.environ.get("NATIVE_SUBSCRIPTION_SOURCE"))
        with (root / "subscription-evidence.json").open("x", encoding="utf-8") as sink:
            json.dump(result, sink, indent=2)
            sink.write("\n")
    except (OSError, ValueError):
        raise SystemExit("Subscription fixture evidence did not pass the privacy and exact-source check.") from None
    print("Verified exact-source synthetic subscription evidence. Private runner metadata was omitted.")


if __name__ == "__main__":
    main()
