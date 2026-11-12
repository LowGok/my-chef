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


def _control_bounds(node, name):
    if (node.get("package") != APP or
            not (node.get("text") == name or node.get("content-desc") == name) or
            not all(node.get(flag) == "true" for flag in ("enabled", "clickable", "focusable"))):
        return None
    match = re.fullmatch(r"\[(\d+),(\d+)\]\[(\d+),(\d+)\]", node.get("bounds", ""))
    if not match:
        return None
    x1, y1, x2, y2 = map(int, match.groups())
    return (x1, y1, x2, y2) if x2 > x1 and y2 > y1 else None


def accessibility(group):
    """Inspect real Android accessibility nodes, without saving their text."""
    groups = {"nav": ("Meals", "Shopping list", "Profile"),
              "export-dialog": ("Cancel", "Download ZIP")}
    if group not in groups:
        raise ValueError("Unsupported native accessibility group")
    deadline = time.monotonic() + 35
    while time.monotonic() < deadline:
        nodes = _snapshot()
        if all(any(_control_bounds(node, name) is not None for node in nodes)
               for name in groups[group]):
            return
        time.sleep(.5)
    raise RuntimeError("Labelled native controls are absent from the accessibility tree")


def picker(filename):
    """Use a real system picker and wait for it to return to MyChef."""
    if filename not in ("SAVE", "CANCEL") and not re.fullmatch(r"native-(?:dish|page)\.png", filename):
        raise ValueError("Unsupported synthetic picker target")
    adb, snapshot = _adb, _snapshot
    deadline = time.monotonic() + 35

    def returned_to_app():
        while time.monotonic() < deadline:
            owner = picker_focus()
            if owner == "app":
                return
            if filename == "CANCEL" and owner == "documents":
                # The first Back can dismiss the filename keyboard alone.
                adb("shell", "input", "keyevent", "4")
            time.sleep(.5)
        raise RuntimeError("Synthetic OS picker did not return to MyChef")

    while time.monotonic() < deadline:
        if picker_focus() != "documents":
            time.sleep(.5)
            continue
        if filename == "CANCEL":
            adb("shell", "input", "keyevent", "4")
            returned_to_app()
            return
        try:
            nodes = [node for node in snapshot() if node.get("package") in SYSTEM_PICKERS]
        except (subprocess.CalledProcessError, subprocess.TimeoutExpired):
            # UiAutomator can report an unavailable hierarchy during transitions.
            time.sleep(.5)
            continue
        wanted = [n for n in nodes if n.get("text", "").casefold() == filename.casefold()
                  or n.get("content-desc", "").casefold() == filename.casefold()]
        if not wanted and filename != "SAVE":
            wanted = [n for n in nodes if n.get("text") in ("Downloads", "Download")]
            if not wanted:
                wanted = [n for n in nodes if n.get("content-desc") == "Show roots"]
        if wanted:
            bounds = re.fullmatch(r"\[(\d+),(\d+)\]\[(\d+),(\d+)\]", wanted[-1].get("bounds", ""))
            if bounds and picker_focus() == "documents":
                x1, y1, x2, y2 = map(int, bounds.groups())
                adb("shell", "input", "tap", str((x1 + x2) // 2), str((y1 + y2) // 2))
                if wanted[-1].get("text", "").casefold() == filename.casefold() or wanted[-1].get("content-desc", "").casefold() == filename.casefold():
                    returned_to_app()
                    return
        time.sleep(.5)
    raise RuntimeError("Synthetic OS picker target was not available")


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
    operations = {"picker": picker, "accessibility": accessibility}
    if len(sys.argv) != 3 or sys.argv[1] not in operations:
        raise SystemExit("Only disposable native UI checks are executable directly")
    mode = sys.argv[1]
    try:
        operations[mode](sys.argv[2])
    except (OSError, RuntimeError, subprocess.SubprocessError) as error:
        if mode == "picker":
            failure = {FileNotFoundError: "adb-missing", subprocess.TimeoutExpired: "adb-timeout",
                       subprocess.CalledProcessError: "adb-command-failed"}.get(type(error), "picker-timeout")
            print(f"{failure}:{picker_focus()}")
        raise SystemExit(1)
