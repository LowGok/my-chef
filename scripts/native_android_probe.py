"""Disposable test APK and OS picker driver; never changes the MyChef package."""
import os
from pathlib import Path
import re
import subprocess
import sys
import time
import xml.etree.ElementTree as ET
import zipfile

APP = "com.gkldevelopment.mychef.preview"
SYSTEM_UI_ANR = "Application Not Responding: com.android.systemui"
SYSTEM_PICKERS = ("com.android.documentsui", "com.google.android.documentsui")


def build(state, sdk, run):
    tools = sdk / "build-tools/36.0.0"
    platform = sdk / "platforms/android-37.0/android.jar"
    if not platform.is_file():
        platform = sdk / "platforms/android-37/android.jar"
    manifest = state / "probe-manifest.xml"
    manifest.write_text('''<manifest xmlns:android="http://schemas.android.com/apk/res/android"
package="com.gkldevelopment.mychef.preview.test">
<uses-sdk android:minSdkVersion="24" android:targetSdkVersion="36"/>
<application android:debuggable="true" android:hasCode="true" android:label="Disposable MyChef test probe"/>
<instrumentation android:name="com.gkldevelopment.mychef.testing.NativeJourneyProbe"
android:targetPackage="com.gkldevelopment.mychef.preview" android:functionalTest="true"/>
</manifest>''')
    classes, dex = state / "probe-classes", state / "probe-dex"
    classes.mkdir(); dex.mkdir()
    source = Path(__file__).resolve().parent / "native-android/NativeJourneyProbe.java"
    run("javac", "--release", "17", "-classpath", str(platform), "-d", str(classes), str(source), str(source.with_name("NativeTouchPoint.java")))
    jar = state / "probe.jar"
    run("jar", "--create", "--file", str(jar), "-C", str(classes), ".")
    run(str(tools / "d8"), "--lib", str(platform), "--output", str(dex), str(jar))
    apk = state / "probe.apk"
    run(str(tools / "aapt2"), "link", "-I", str(platform), "--manifest", str(manifest), "-o", str(apk))
    with zipfile.ZipFile(apk, "a") as archive:
        archive.write(dex / "classes.dex", "classes.dex")
    run(str(tools / "apksigner"), "sign", "--ks", str(Path.home() / ".android/debug.keystore"),
        "--ks-key-alias", "androiddebugkey", "--ks-pass", "pass:android", "--key-pass", "pass:android", str(apk))
    return apk


def _adb(*args):
    sdk = os.environ.get("ANDROID_HOME")
    binary = str(Path(sdk) / "platform-tools" / "adb") if sdk else "adb"
    return subprocess.check_output([binary, "-s", "emulator-5554", *args], text=True,
                                   stderr=subprocess.DEVNULL, timeout=20)


def _snapshot(adb=_adb):
    path = "/sdcard/mychef-picker.xml"
    adb("shell", "rm", "-f", path)
    adb("shell", "uiautomator", "dump", path)
    raw = adb("shell", "cat", path)
    try:
        return list(ET.fromstring(raw[raw.index("<?xml"):]).iter("node"))
    except (ValueError, ET.ParseError):
        return []


def focused_window(adb):
    # API 33 reports the focused WindowState in the display dump.
    displays = adb("shell", "dumpsys", "window", "displays")
    match = re.search(r"^\s*mCurrentFocus=Window\{[a-f0-9]+ u0 ([^\n]+)\}$",
                      displays, re.MULTILINE)
    return match.group(1) if match else ""


def dismiss_system_ui_anr(adb):
    """Recover the disposable emulator when Android's own System UI hangs."""
    if focused_window(adb) != SYSTEM_UI_ANR:
        return False
    close = next((node for node in _snapshot(adb)
                  if node.get("package") == "android"
                  and node.get("resource-id") == "android:id/aerr_close"
                  and node.get("enabled") == node.get("clickable") == "true"), None)
    if close is None:
        return False
    bounds = re.fullmatch(r"\[(\d+),(\d+)\]\[(\d+),(\d+)\]", close.get("bounds", ""))
    if not bounds or focused_window(adb) != SYSTEM_UI_ANR:
        return False
    x1, y1, x2, y2 = map(int, bounds.groups())
    if x2 <= x1 or y2 <= y1:
        return False
    adb("shell", "input", "tap", str((x1 + x2) // 2), str((y1 + y2) // 2))
    return True


def cancel_picker():
    """Send OS Back only to the focused system picker and wait for MyChef."""
    deadline = time.monotonic() + 35
    saw_documents = False
    while time.monotonic() < deadline:
        owner = picker_focus()
        if saw_documents and owner == "app":
            return
        if owner == "documents":
            saw_documents = True
            # The first Back can dismiss the filename keyboard alone.
            _adb("shell", "input", "keyevent", "4")
        time.sleep(.5)
    raise RuntimeError("Synthetic OS picker was not opened and closed")


def picker_focus():
    """Report only whether a known window owns focus, without window details."""
    try:
        window = focused_window(_adb)
    except (OSError, subprocess.SubprocessError):
        return "unknown"
    owner = window.split("/", 1)[0]
    if owner == APP:
        return "app"
    if owner in SYSTEM_PICKERS:
        return "documents"
    if window == SYSTEM_UI_ANR:
        return "system-ui-anr"
    return "other" if window else "unknown"


if __name__ == "__main__":
    if sys.argv[1:] == ["picker-focus"]:
        print(picker_focus())
        raise SystemExit(0)
    if sys.argv[1:] != ["cancel-picker"]:
        raise SystemExit("Only disposable picker cancellation is executable directly")
    try:
        cancel_picker()
    except (OSError, RuntimeError, subprocess.SubprocessError) as error:
        failure = {FileNotFoundError: "adb-missing", subprocess.TimeoutExpired: "adb-timeout",
                   subprocess.CalledProcessError: "adb-command-failed"}.get(type(error), "picker-timeout")
        print(f"{failure}:{picker_focus()}")
        raise SystemExit(1)
