"""Run TalkBack on exact public MyChef source with a disposable local backend."""
from contextlib import ExitStack
from pathlib import Path
import hashlib
import json
import os
import re
import signal
import subprocess
import sys
import tempfile
import time

from prepare_probe import stage as probe
from prepare_journey import stage as journey

SOURCE = "0f3e4a8764669be1a1d6ca02d6a93612f6f1f053"
BUNDLE = "65f9286044cb000028fbdbbd09c3624781084748ef9be2ac0c20b9417e22dfe3"
SERVICE = "com.google.android.marvin.talkback/.TalkBackService"

if len(sys.argv) != 6:
    raise SystemExit("Expected app checkout, verified capture and recognition tools, model and report directory")
root, scrcpy, recognizer, model, report = (Path(x).resolve() for x in sys.argv[1:])
event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
if (os.environ.get("GITHUB_ACTIONS") != "true" or os.environ["GITHUB_EVENT_NAME"] != "push"
        or os.environ["GITHUB_REPOSITORY"] != "LowGok/my-chef" or event["repository"]["id"] != 1454481532
        or event["repository"]["private"] is not False or event["sender"]["login"] != "LowGok"
        or event["ref"] != "refs/heads/ci/reminder-talkback-preflight-0f3e4a8"
        or subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip() != SOURCE
        or subprocess.check_output(["git", "status", "--porcelain"], cwd=root, text=True).strip()):
    raise SystemExit("Unexpected public CI context or application source")
report.mkdir(mode=0o700, exist_ok=False)
os.umask(0o077)
sys.path.insert(0, str(root / "scripts"))
os.environ["NATIVE_PLATFORM"] = "android"
from native_android_probe import build
from native_fixture_account import seed

# Use the existing fixture implementation, with audio enabled only on this
# disposable hosted emulator. Product source and package stay unchanged.
smoke = root / "scripts/test_native_installed.py"
base = smoke.read_text()
flag = '"-no-window", "-no-audio",'
if base.count(flag) != 1:
    raise SystemExit("Unexpected hosted Android harness audio configuration")
import types
fixture_module = types.ModuleType("talkback_fixture")
fixture_module.__file__ = str(smoke)
avd = 'command(avdmanager, "create", "avd", "--name", "mychef-ci", "--package", image, data="no\\n")'
if base.count(avd) != 1:
    raise SystemExit("Unexpected fixture virtual device creation")
base = base.replace(flag, '"-no-window",', 1).replace(avd,
    'command(avdmanager, "create", "avd", "--name", "mychef-ci", "--package", image, '
    '"--device", "pixel_6", "--path", str(Path(ENV["ANDROID_AVD_HOME"]) / "mychef-ci.avd"))', 1)
exec(compile(base, str(smoke), "exec"), fixture_module.__dict__)
f = fixture_module
manifest = json.loads((f.GENERATED / "bundle-manifest.json").read_text())
if manifest["sourceCommit"] != SOURCE or manifest["bundleSha256"] != BUNDLE:
    raise SystemExit("The web assets do not match the verified PR candidate")
apk = root / "mobile/android/app/build/outputs/apk/debug/app-debug.apk"
if not apk.is_file() or apk.stat().st_size > 64 * 1024**2:
    raise SystemExit("Unexpected installed APK")
app_hash = hashlib.sha256(apk.read_bytes()).hexdigest()

with tempfile.TemporaryDirectory(prefix="mychef-installed-") as directory, ExitStack() as stack:
    state = Path(directory)
    ca, events, rejected = f.fixture(state, stack)
    avd_dir = state / "avd"
    avd_dir.mkdir()
    f.ENV["ANDROID_AVD_HOME"] = str(avd_dir)
    metadata, launch, _, diagnostics = f.android(state, stack, ca, apk)
    f.verify_stage(lambda: ("GET", "session", 200) in events, "native HTTPS session",
                   events, rejected, diagnostics, launch=launch)
    adb_path = str(Path(os.environ["ANDROID_HOME"]) / "platform-tools/adb")
    adb = lambda *args: f.command(adb_path, "-s", "emulator-5554", *args)
    services = adb("shell", "cmd", "package", "query-services", "--components", "--brief",
                   "-a", "android.accessibilityservice.AccessibilityService")
    if SERVICE not in services.splitlines():
        raise RuntimeError("Expected stock Google TalkBack service was not installed")
    # Isolated copies of the two harness modules. Only the test APK and its
    # TypeScript driver differ, and they are staged after the app was built.
    probe_path = root / "scripts/native-android/NativeJourneyProbe.java"
    journey_path = root / "scripts/native-android-journeys.ts"
    probe(probe_path, probe_path)
    journey(journey_path, journey_path)
    adb("install", str(build(state, Path(os.environ["ANDROID_HOME"]), f.command)))
    capability = json.loads((state / "fixture-inbox.json").read_text())["capability"]
    account = seed(18137, f.ORIGIN, capability, "nativeandroidtalkback")
    (state / "native-android-account.json").write_text(json.dumps(account))
    f.ENV.update({k: os.environ[k] for k in ("GITHUB_ACTIONS", "GITHUB_REPOSITORY", "NATIVE_PLATFORM")})
    f.ENV["MYCHEF_TALKBACK_COMPONENT"] = SERVICE
    report_data = dict(sourceCommit=SOURCE, sourceBundleSha256=BUNDLE, apkSha256=app_hash,
                       device=metadata, talkbackService=SERVICE, systemFontScale=1.5)
    recording = state / "talkback.mkv"
    app_log = stack.enter_context((state / "talkback-journey.log").open("w"))
    audio_log = stack.enter_context((state / "audio.log").open("w"))
    capture = None
    try:
        with f.android_reminder_text(adb):
            ui = subprocess.Popen(["bun", "scripts/native-android-journeys.ts", str(state), "talkback"],
                                  cwd=root, env=f.ENV, stdout=app_log, stderr=app_log)
            stack.callback(f.terminate, ui)
            f.wait_for(lambda: (state / "talkback-ready").exists() or ui.poll() is not None,
                       "signed-in fixture and bound TalkBack", seconds=120)
            if ui.poll() is not None:
                raise RuntimeError("Installed app did not reach TalkBack setup")
            recording_started = time.time()
            capture = subprocess.Popen([str(scrcpy), "--serial=emulator-5554", "--no-window", "--no-control",
                                        "--require-audio", "--audio-source=output", "--audio-codec=flac",
                                        "--record=" + str(recording)],
                                       env=dict(os.environ, ADB=adb_path, SCRCPY_SERVER_PATH=str(scrcpy.with_name("scrcpy-server"))), stdout=audio_log, stderr=audio_log)
            stack.callback(f.terminate, capture)
            f.wait_for(lambda: recording.exists() or capture.poll() is not None, "device audio recording", seconds=16)
            if capture.poll() is not None:
                raise RuntimeError("Android output audio capture failed")
            (state / "talkback-capture-ready").write_text("capturing\n")
            if ui.wait(timeout=110) != 0:
                raise RuntimeError("Installed TalkBack focus or control operation failed")
        capture.send_signal(signal.SIGINT)
        capture.wait(timeout=12)
        result = json.loads((state / "native-android-journeys-talkback.json").read_text())
        expected = [f"OS accessibility focus with TalkBack on {x}" for x in ("opt-in", "save", "device-off")]
        if result.get("passed") != expected:
            raise RuntimeError("Missing OS focus event receipt")
        if not recording.exists() or not 1000 < recording.stat().st_size < 12_000_000:
            raise RuntimeError("Unexpected Android output audio capture size")
        registrations = [(verb, status) for verb, route, status in events if route == "planning-reminders/installation"]
        if ("PUT", 204) not in registrations or registrations[-1:] != [("DELETE", 204)]:
            raise RuntimeError("Disposable registration and opt out were not both confirmed")
        phrases = {"opt-in": "monday reminder", "save": "save reminder preferences",
                   "device-off": "turn off on this device"}
        recognized = {}
        # Record, extract and recognize locally; no raw audio, transcript,
        # credentials, or account data are uploaded or written to job output.
        def recognize(path, prefix):
            output = state / prefix
            result = subprocess.run([str(recognizer), "-m", str(model), "-f", str(path), "-l", "en",
                                     "-t", "4", "-np", "-nt", "-otxt", "-of", str(output)],
                                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=60)
            if result.returncode != 0:
                raise RuntimeError("Offline speech recognition failed")
            text = output.with_suffix(".txt").read_text(errors="replace")
            return " " + " ".join(re.findall(r"[a-z]+", text.lower())) + " "

        for target, phrase in phrases.items():
            when = (state / ("talkback-focused-" + target)).stat().st_mtime
            if not 0 < when - recording_started < 90:
                raise RuntimeError("Invalid focus and audio timeline")
            clip = state / (target + ".wav")
            offset = max(0, when - recording_started - 1.8)
            subprocess.run(["ffmpeg", "-nostdin", "-v", "error", "-ss", f"{offset:.3f}", "-i", str(recording),
                            "-t", "4.2", "-vn", "-map", "0:a:0", "-ac", "1", "-ar", "16000",
                            "-c:a", "pcm_s16le", str(clip)], check=True, timeout=15)
            if not 1000 < clip.stat().st_size < 2_000_000:
                raise RuntimeError("Invalid control audio clip")
            recognized[target] = f" {phrase} " in recognize(clip, target + "-recognized")
        report_data.update(focusEvents=expected, deviceOptOut="pass", spokenPhrasesMatched=recognized,
                           speechMethod="Unprompted, offline recognition of Android output audio")
        if not all(recognized.values()):
            print("Expected phrases recognized:", recognized, flush=True)
            raise RuntimeError("Not all three exact TalkBack phrases were recognized")
        (report / "focus-receipt.json").write_text(json.dumps(report_data, indent=2) + "\n")
    except Exception:
        recording.unlink(missing_ok=True)
        # The driver already redacts fixture credentials. Keep CI output to
        # allowlisted stage names, not diagnostics from login or notification.
        statuses = re.findall(r"Installed Android check passed: OS accessibility focus with TalkBack on (opt-in|save|device-off)",
                              (state / "talkback-journey.log").read_text(errors="replace"))
        f.android_request_diagnostics(adb_path)
        print("Focus stages completed:", statuses, flush=True)
        raise
