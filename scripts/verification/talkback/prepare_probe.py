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
    private void focusForTalkBack(String target) throws Exception {
        UiAutomation automation = getUiAutomation(UiAutomation.FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES);
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
        if (!scaled[0]) throw new IllegalStateException("accessibility-controls");

        long end = SystemClock.uptimeMillis() + 10_000;
        while (SystemClock.uptimeMillis() < end) {
            AccessibilityNodeInfo root = automation.getRootInActiveWindow();
            AccessibilityNodeInfo node = root == null || root.getPackageName() == null || !packageName.contentEquals(root.getPackageName())
                    ? null : named(root, label, true, 0);
            if (node != null && (!target.equals("opt-in") || (node.isCheckable() && node.isChecked()))) {
                if (node.isAccessibilityFocused()) {
                    node.performAction(AccessibilityNodeInfo.ACTION_CLEAR_ACCESSIBILITY_FOCUS);
                }
                AccessibilityEvent result = automation.executeAndWaitForEvent(
                    () -> {
                        if (!node.performAction(AccessibilityNodeInfo.ACTION_ACCESSIBILITY_FOCUS)) {
                            throw new IllegalStateException("accessibility-controls");
                        }
                    },
                    (event) -> {
                        if (event.getEventType() != AccessibilityEvent.TYPE_VIEW_ACCESSIBILITY_FOCUSED
                                || event.getPackageName() == null || !packageName.contentEquals(event.getPackageName())) {
                            return false;
                        }
                        AccessibilityNodeInfo source = event.getSource();
                        return source != null && source.equals(node)
                                && (labelMatches(source.getText(), label) || labelMatches(source.getContentDescription(), label));
                    }, 5000);
                result.recycle();
                return;
            }
            SystemClock.sleep(250);
        }
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
