"""Stage a disposable TalkBack focus probe, leaving the product APK source unchanged."""
from pathlib import Path
import argparse


def replace_once(text, old, new):
    if text.count(old) != 1:
        raise ValueError("Disposable probe source has drifted")
    return text.replace(old, new, 1)


def stage(source, output):
    text = source.read_text()
    text = replace_once(text, "import android.view.accessibility.AccessibilityNodeInfo;",
                        "import android.view.accessibility.AccessibilityEvent;\n"
                        "import android.view.accessibility.AccessibilityNodeInfo;")
    text = replace_once(text, "    private void checkAccessibility(String group) {", """
    private void speechStage(String name) {
        Bundle event = new Bundle();
        event.putString("speechStage", name);
        sendStatus(1, event);
    }

    private AccessibilityNodeInfo speechWindow(UiAutomation automation, String packageName) {
        for (AccessibilityWindowInfo window : automation.getWindows()) {
            if (window.getType() != AccessibilityWindowInfo.TYPE_APPLICATION) continue;
            AccessibilityNodeInfo root = window.getRoot();
            if (root != null && packageName.equals(String.valueOf(root.getPackageName()))) return root;
        }
        AccessibilityNodeInfo active = automation.getRootInActiveWindow();
        return active != null && packageName.equals(String.valueOf(active.getPackageName())) ? active : null;
    }

    private AccessibilityNodeInfo speechControl(AccessibilityNodeInfo root, String label) {
        AccessibilityNodeInfo control = named(root, label, true, 0);
        if (control != null) return control;
        // An enlarged Android WebView may expose the text on a child of the
        // actionable control. Focus the button, rather than the separate text.
        AccessibilityNodeInfo candidate = named(root, label, false, 0);
        for (int i = 0; i < 4 && candidate != null; ++i, candidate = candidate.getParent()) {
            if (candidate.isVisibleToUser() && candidate.isEnabled() && candidate.isClickable()
                    && candidate.isFocusable()) return candidate;
        }
        return null;
    }

    private void focusForTalkBack(String target) throws Exception {
        UiAutomation automation = getUiAutomation(UiAutomation.FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES);
        AccessibilityServiceInfo info = automation.getServiceInfo();
        if ((info.flags & AccessibilityServiceInfo.FLAG_RETRIEVE_INTERACTIVE_WINDOWS) == 0) {
            info.flags |= AccessibilityServiceInfo.FLAG_RETRIEVE_INTERACTIVE_WINDOWS;
            automation.setServiceInfo(info);
        }
        String label = switch (target) {
            case "opt-in" -> "Monday reminder";
            case "save" -> "Save reminder preferences";
            case "device-off" -> "Turn off on this device";
            default -> throw new IllegalArgumentException("accessibility-controls");
        };
        String packageName = getTargetContext().getPackageName();
        boolean[] scaled = {false};
        runOnMainSync(() -> scaled[0] = Math.abs(web.getResources().getConfiguration().fontScale - 1.5f) < 0.01f
                && web.getSettings().getTextZoom() >= 140);
        if (!scaled[0]) {
            speechStage("scale-not-applied");
            throw new IllegalStateException("accessibility-controls");
        }

        boolean hadWindow = false, hadControl = false;
        long end = SystemClock.uptimeMillis() + 18_000;
        while (SystemClock.uptimeMillis() < end) {
            AccessibilityNodeInfo root = speechWindow(automation, packageName);
            hadWindow |= root != null;
            AccessibilityNodeInfo node = speechControl(root, label);
            if (node != null && (!target.equals("opt-in") || (node.isCheckable() && node.isChecked()))) {
                hadControl = true;
                // A previous announcement or automatic scroll may already
                // have focused the control. Require a fresh native event.
                node.performAction(AccessibilityNodeInfo.ACTION_CLEAR_ACCESSIBILITY_FOCUS);
                SystemClock.sleep(150);
                if (!node.refresh()) continue;
                try {
                    AccessibilityEvent result = automation.executeAndWaitForEvent(
                        () -> node.performAction(AccessibilityNodeInfo.ACTION_ACCESSIBILITY_FOCUS),
                        event -> {
                            if (event.getEventType() != AccessibilityEvent.TYPE_VIEW_ACCESSIBILITY_FOCUSED
                                    || !packageName.equals(String.valueOf(event.getPackageName()))) return false;
                            AccessibilityNodeInfo source = event.getSource();
                            return source != null && source.equals(node);
                        }, 3500);
                    result.recycle();
                    return;
                } catch (java.util.concurrent.TimeoutException missed) {
                    // Refresh the real app window and retry after Android has
                    // finished delivering scroll and accessibility changes.
                }
            }
            SystemClock.sleep(250);
        }
        speechStage(!hadWindow ? "app-window-missing" : !hadControl ? "control-not-visible" : "focus-event-missing");
        throw new IllegalStateException("accessibility-controls");
    }

    private void checkAccessibility(String group) {""".lstrip("\n"))
    text = replace_once(text,
        '                    } else if (value.matches("accessibility:[1-9][0-9]{0,3}:(nav|export-dialog|reminder-opt-in|reminder-save)")) {',
        '''                    } else if (value.matches("speech-focus:[1-9][0-9]{0,3}:(opt-in|save|device-off)")) {
                        String[] parts = value.split(":", 3);
                        focusForTalkBack(parts[2]);
                        Files.write(done.toPath(), parts[1].getBytes(StandardCharsets.UTF_8));
                    } else if (value.matches("accessibility:[1-9][0-9]{0,3}:(nav|export-dialog|reminder-opt-in|reminder-save)")) {''')
    text = replace_once(text, "    @Override public void onStart() {",
                        "    @Override public void onStart() {\n"
                        "        getUiAutomation(UiAutomation.FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES);")
    output.write_text(text)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("source", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    stage(args.source.resolve(strict=True), args.output.resolve())
