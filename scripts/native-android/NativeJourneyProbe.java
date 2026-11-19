package com.gkldevelopment.mychef.testing;

import android.app.Activity;
import android.app.Instrumentation;
import android.app.UiAutomation;
import android.accessibilityservice.AccessibilityServiceInfo;
import android.content.Intent;
import android.graphics.Rect;
import android.os.Bundle;
import android.os.ParcelFileDescriptor;
import android.os.SystemClock;
import android.view.InputDevice;
import android.view.MotionEvent;
import android.view.View;
import android.view.ViewConfiguration;
import android.view.ViewGroup;
import android.view.accessibility.AccessibilityEvent;
import android.view.accessibility.AccessibilityManager;
import android.view.accessibility.AccessibilityNodeInfo;
import android.view.accessibility.AccessibilityWindowInfo;
import android.webkit.WebView;
import java.io.File;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.util.Arrays;
import java.util.Locale;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import org.json.JSONArray;
import org.json.JSONObject;

/** Installed only in a disposable emulator, never included in the MyChef APK. */
public final class NativeJourneyProbe extends Instrumentation {
    private volatile WebView web;
    private volatile Activity currentActivity;

    @Override public void callActivityOnResume(Activity activity) {
        super.callActivityOnResume(activity);
        if (activity.getPackageName().equals(getTargetContext().getPackageName()))
            currentActivity = activity;
    }

    private void refreshWeb() {
        Activity activity = currentActivity;
        if (activity != null && !activity.isDestroyed()) {
            WebView current = find(activity.getWindow().getDecorView());
            if (current != null) web = current;
        }
    }

    @Override public void onCreate(Bundle arguments) { super.onCreate(arguments); start(); }

    private WebView find(View view) {
        if (view instanceof WebView) return (WebView)view;
        if (view instanceof ViewGroup) {
            ViewGroup group = (ViewGroup)view;
            for (int i = 0; i < group.getChildCount(); i++) {
                WebView found = find(group.getChildAt(i));
                if (found != null) return found;
            }
        }
        return null;
    }

    private void touch(long down, int action, int count, float distance, int[] location) {
        MotionEvent.PointerProperties[] properties = new MotionEvent.PointerProperties[count];
        MotionEvent.PointerCoords[] coordinates = new MotionEvent.PointerCoords[count];
        for (int i = 0; i < count; i++) {
            properties[i] = new MotionEvent.PointerProperties(); properties[i].id = i;
            properties[i].toolType = MotionEvent.TOOL_TYPE_FINGER;
            coordinates[i] = new MotionEvent.PointerCoords();
            coordinates[i].x = location[0] + web.getWidth() / 2f + (i == 0 ? -distance : distance);
            coordinates[i].y = location[1] + web.getHeight() * .4f;
            coordinates[i].pressure = 1f; coordinates[i].size = .1f;
        }
        MotionEvent event = MotionEvent.obtain(down, SystemClock.uptimeMillis(), action, count,
            properties, coordinates, 0, 0, 1f, 1f, 0, 0, InputDevice.SOURCE_TOUCHSCREEN, 0);
        try { getUiAutomation(UiAutomation.FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES).injectInputEvent(event, true); } finally { event.recycle(); }
    }

    private void pinch() {
        int[] location = new int[2];
        runOnMainSync(() -> web.getLocationOnScreen(location));
        long down = SystemClock.uptimeMillis();
        float start = web.getWidth() * .06f, end = web.getWidth() * .3f;
        touch(down, MotionEvent.ACTION_DOWN, 1, start, location);
        touch(down, MotionEvent.ACTION_POINTER_DOWN | (1 << MotionEvent.ACTION_POINTER_INDEX_SHIFT), 2, start, location);
        for (int i = 1; i <= 20; i++) {
            SystemClock.sleep(35);
            touch(down, MotionEvent.ACTION_MOVE, 2, start + (end - start) * i / 20f, location);
        }
        touch(down, MotionEvent.ACTION_POINTER_UP | (1 << MotionEvent.ACTION_POINTER_INDEX_SHIFT), 2, end, location);
        touch(down, MotionEvent.ACTION_UP, 1, end, location);
    }

    private void activate(String label) throws Exception {
        if (!Arrays.asList("Save preferences", "Save password", "Send feedback", "Save rating", "Save", "Retry save",
                "Review menu", "Plan my week", "Add extra", "Check page", "Read recipe pages",
                "Download ZIP", "Permanently delete account", "Current password",
                "Choose recipe photo", "Recipe-page photos", "Create my account", "Create account").contains(label))
            throw new IllegalStateException("native-control");
        // A keyboard/scene transition can briefly release window focus.
        // Wait for the real current WebView before measuring or injecting;
        // no activation is attempted until this succeeds.
        boolean[] ready = {false};
        for (int i = 0; i < 50 && !ready[0]; i++) {
            runOnMainSync(() -> {
                refreshWeb();
                ready[0] = web != null && web.isShown() && web.hasWindowFocus();
            });
            if (!ready[0]) SystemClock.sleep(100);
        }
        if (!ready[0]) throw new IllegalStateException("native-window-unfocused");
        String quotedLabel = JSONObject.quote(label);
        String selector = switch (label) {
            case "Current password" -> "[...document.querySelectorAll('dialog[open] input[name=currentPassword]')].filter(n=>!n.disabled&&n.getClientRects().length)";
            case "Choose recipe photo", "Recipe-page photos" ->
                "[...document.querySelectorAll('input[type=button]')].filter(n=>!n.disabled&&n.getClientRects().length&&[...n.labels].some(l=>(l.textContent||'').trim()==="
                    + quotedLabel + "))";
            default -> "[...document.querySelectorAll('button')].filter(n=>(n.getAttribute('aria-label')||n.textContent||'').trim()==="
                + quotedLabel + "&&!n.disabled&&n.getClientRects().length)";
        };
        String expression = "(() => {const nodes=" + selector + ";"
            + "if(nodes.length!==1)return null;const r=nodes[0].getBoundingClientRect(),v=visualViewport;"
            + "return [r.x+r.width/2,r.y+r.height/2,v?v.offsetLeft:0,v?v.offsetTop:0,v?v.width:innerWidth,v?v.height:innerHeight]})()";
        // Both measurements and the optional layout-only correction share the
        // original measurement budget. Pointer construction stays below prepare.
        long deadline = SystemClock.uptimeMillis() + 10_000;
        int[] target = NativeTouchPoint.prepare(() -> {
            double[][] point = {null};
            CountDownLatch evaluated = new CountDownLatch(1);
            runOnMainSync(() -> web.evaluateJavascript(expression, value -> {
                try {
                    JSONArray result = new JSONArray(value);
                    if (result.length() == 6) {
                        point[0] = new double[6];
                        for (int i = 0; i < 6; i++) point[0][i] = result.getDouble(i);
                    }
                } catch (Exception ignored) { point[0] = null; }
                finally { evaluated.countDown(); }
            }));
            long remaining = deadline - SystemClock.uptimeMillis();
            if (remaining <= 0 || !evaluated.await(remaining, TimeUnit.MILLISECONDS))
                throw new IllegalStateException("control-timeout");
            int[] location = new int[2], size = new int[2];
            boolean[] focused = {false};
            runOnMainSync(() -> {
                web.getLocationOnScreen(location); size[0] = web.getWidth(); size[1] = web.getHeight();
                focused[0] = web.isShown() && web.hasWindowFocus();
            });
            return NativeTouchPoint.locate(point[0], location, size[0], size[1], focused[0]);
        }, () -> {
            CountDownLatch centered = new CountDownLatch(1);
            boolean[] corrected = {false};
            String correction = "(() => {const nodes=" + selector + ";"
                + "if(nodes.length!==1)return false;"
                + "nodes[0].scrollIntoView({block:'center',inline:'nearest',behavior:'instant'});return true})()";
            runOnMainSync(() -> web.evaluateJavascript(correction, value -> {
                corrected[0] = "true".equals(value); centered.countDown();
            }));
            long remaining = deadline - SystemClock.uptimeMillis();
            if (remaining <= 0 || !centered.await(remaining, TimeUnit.MILLISECONDS))
                throw new IllegalStateException("control-timeout");
            if (!corrected[0]) throw new IllegalStateException("control-target");
            if (deadline - SystemClock.uptimeMillis() < 350)
                throw new IllegalStateException("control-timeout");
            SystemClock.sleep(350);
        });
        long down = SystemClock.uptimeMillis();
        MotionEvent.PointerProperties finger = new MotionEvent.PointerProperties();
        finger.id = 0; finger.toolType = MotionEvent.TOOL_TYPE_FINGER;
        MotionEvent.PointerCoords position = new MotionEvent.PointerCoords();
        position.x = target[0]; position.y = target[1]; position.pressure = 1f; position.size = .1f;
        for (int action : new int[]{MotionEvent.ACTION_DOWN, MotionEvent.ACTION_UP}) {
            MotionEvent event = MotionEvent.obtain(down, SystemClock.uptimeMillis(), action, 1,
                new MotionEvent.PointerProperties[]{finger}, new MotionEvent.PointerCoords[]{position},
                0, 0, 1f, 1f, 0, 0, InputDevice.SOURCE_TOUCHSCREEN, 0);
            try { sendPointerSync(event); }
            catch (SecurityException denied) { throw new IllegalStateException("input-denied"); }
            finally { event.recycle(); }
            SystemClock.sleep(50);
        }
    }

    private static boolean documents(AccessibilityNodeInfo node) {
        if (node == null) return false;
        String owner = String.valueOf(node.getPackageName());
        return owner.equals("com.android.documentsui") || owner.equals("com.google.android.documentsui");
    }

    private UiAutomation interactiveWindows() {
        UiAutomation automation = getUiAutomation(UiAutomation.FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES);
        AccessibilityServiceInfo info = automation.getServiceInfo();
        int flags = AccessibilityServiceInfo.FLAG_RETRIEVE_INTERACTIVE_WINDOWS
            | AccessibilityServiceInfo.FLAG_REPORT_VIEW_IDS;
        if ((info.flags & flags) != flags) {
            info.flags |= flags;
            automation.setServiceInfo(info);
        }
        return automation;
    }

    private AccessibilityNodeInfo systemCloseButton(AccessibilityNodeInfo node, int depth) {
        if (node == null || depth > 24) return null;
        if ("android".equals(String.valueOf(node.getPackageName()))
                && "android:id/aerr_close".equals(node.getViewIdResourceName())
                && node.isVisibleToUser() && node.isEnabled() && node.isClickable()) return node;
        for (int i = 0; i < node.getChildCount(); i++) {
            AccessibilityNodeInfo found = systemCloseButton(node.getChild(i), depth + 1);
            if (found != null) return found;
        }
        return null;
    }

    private String focusedSystemUiTitle(UiAutomation automation) {
        // AccessibilityWindowInfo exposes an accessibility title, which is
        // distinct from WindowManager's process-specific ANR identity.
        // Use the same existing connection and a bounded read-only command.
        try (ParcelFileDescriptor.AutoCloseInputStream input = new ParcelFileDescriptor.AutoCloseInputStream(
                automation.executeShellCommand("dumpsys -t 5 window displays"))) {
            byte[] raw = input.readNBytes(131073);
            if (raw.length > 131072) return "";
            return NativeStartupWindow.focusedTitle(new String(raw, StandardCharsets.UTF_8));
        } catch (Exception unavailable) { return ""; }
    }

    private String recoverSystemUiAnr() {
        // Reuse this instrumentation's connection; a second uiautomator
        // process cannot register while the probe owns UiAutomation.
        UiAutomation automation = interactiveWindows();
        for (AccessibilityWindowInfo window : automation.getWindows()) {
            if (!window.isFocused() || window.getDisplayId() != 0) continue;
            AccessibilityNodeInfo root = window.getRoot();
            AccessibilityNodeInfo close = systemCloseButton(root, 0);
            if (root == null || close == null || !NativeStartupWindow.canClose(
                    focusedSystemUiTitle(automation), String.valueOf(root.getPackageName()),
                    String.valueOf(close.getPackageName()), close.getViewIdResourceName())) continue;
            return close.performAction(AccessibilityNodeInfo.ACTION_CLICK) ? "closed" : "unavailable";
        }
        return "absent";
    }

    private static AccessibilityNodeInfo pickerRoot(UiAutomation automation) {
        boolean hasFocusedAppWindow = false;
        for (AccessibilityWindowInfo window : automation.getWindows()) {
            if (!window.isFocused() || window.getType() != AccessibilityWindowInfo.TYPE_APPLICATION) continue;
            hasFocusedAppWindow = true;
            AccessibilityNodeInfo root = window.getRoot();
            if (documents(root)) return root;
        }
        if (hasFocusedAppWindow) return null;
        AccessibilityNodeInfo root = automation.getRootInActiveWindow();
        return documents(root) ? root : null;
    }

    private static boolean labelMatches(CharSequence actual, String expected) {
        if (actual == null) return false;
        String label = actual.toString().strip().toLowerCase(Locale.ROOT);
        String target = expected.toLowerCase(Locale.ROOT);
        if (label.equals(target)) return true;
        // DocumentsUI may omit the extension or append file metadata to a tile.
        return target.endsWith(".png") && (label.equals(target.substring(0, target.length() - 4))
            || label.startsWith(target + ",") || label.startsWith(target + "\n"));
    }

    private static AccessibilityNodeInfo named(AccessibilityNodeInfo node, String label, boolean control, int depth) {
        if (node == null || depth > 32) return null;
        if (node.isVisibleToUser() && (!control || node.isEnabled() && node.isClickable() && node.isFocusable())
                && (labelMatches(node.getText(), label) || labelMatches(node.getContentDescription(), label))) return node;
        for (int i = 0; i < node.getChildCount(); i++) {
            AccessibilityNodeInfo result = named(node.getChild(i), label, control, depth + 1);
            if (result != null) return result;
        }
        return null;
    }

    private static AccessibilityNodeInfo fileTitle(AccessibilityNodeInfo node, String filename, int depth) {
        if (node == null || depth > 32) return null;
        if (node.isVisibleToUser() && node.isEnabled() && labelMatches(node.getText(), filename)) return node;
        for (int i = 0; i < node.getChildCount(); i++) {
            AccessibilityNodeInfo result = fileTitle(node.getChild(i), filename, depth + 1);
            if (result != null) return result;
        }
        return null;
    }

    private static boolean clickDocuments(AccessibilityNodeInfo node) {
        for (int depth = 0; depth < 5 && documents(node); depth++, node = node.getParent()) {
            if (node.isVisibleToUser() && node.isEnabled() && node.isClickable())
                return node.performAction(AccessibilityNodeInfo.ACTION_CLICK);
        }
        return false;
    }

    private static boolean tapDocument(UiAutomation automation, AccessibilityNodeInfo root, AccessibilityNodeInfo node) {
        if (!documents(node) || !node.isVisibleToUser() || !node.isEnabled()) return false;
        return clickDocuments(node) || touchDocument(automation, root, node, 50);
    }

    private static boolean touchDocument(UiAutomation automation, AccessibilityNodeInfo root, AccessibilityNodeInfo node, int holdMillis) {
        if (!documents(node) || !node.isVisibleToUser() || !node.isEnabled()) return false;
        Rect area = new Rect(), window = new Rect();
        node.getBoundsInScreen(area);
        root.getBoundsInScreen(window);
        if (area.isEmpty() || !window.contains(area.centerX(), area.centerY())) return false;
        long down = SystemClock.uptimeMillis();
        // The DocumentsUI grid distinguishes a finger tap from a mouse or
        // unknown pointer; setting only the input source is not enough.
        MotionEvent.PointerProperties finger = new MotionEvent.PointerProperties();
        finger.id = 0;
        finger.toolType = MotionEvent.TOOL_TYPE_FINGER;
        MotionEvent.PointerCoords position = new MotionEvent.PointerCoords();
        position.x = area.centerX();
        position.y = area.centerY();
        position.pressure = 1f;
        position.size = .1f;
        for (int action : new int[]{MotionEvent.ACTION_DOWN, MotionEvent.ACTION_UP}) {
            MotionEvent event = MotionEvent.obtain(down, SystemClock.uptimeMillis(), action, 1,
                new MotionEvent.PointerProperties[]{finger}, new MotionEvent.PointerCoords[]{position},
                0, 0, 1f, 1f, 0, 0, InputDevice.SOURCE_TOUCHSCREEN, 0);
            try { if (!automation.injectInputEvent(event, true)) return false; }
            finally { event.recycle(); }
            SystemClock.sleep(action == MotionEvent.ACTION_DOWN ? holdMillis : 50);
        }
        return true;
    }

    private static boolean confirmDocuments(UiAutomation automation, AccessibilityNodeInfo root) {
        String owner = String.valueOf(root.getPackageName());
        for (AccessibilityNodeInfo control : root.findAccessibilityNodeInfosByViewId(owner + ":id/menu_open")) {
            if (tapDocument(automation, root, control)) return true;
        }
        for (String label : new String[]{"Open", "Select", "Done"}) {
            if (tapDocument(automation, root, named(root, label, false, 0))) return true;
        }
        return false;
    }

    private void selectSystemDocument(String target) {
        UiAutomation automation = interactiveWindows();
        long end = SystemClock.uptimeMillis() + 30_000;
        boolean save = target.equals("SAVE"), page = target.equals("native-page.png");
        boolean sawPicker = false, sawTarget = false, selected = false, confirmed = false;
        boolean openedRoots = false, visitedDownloads = false;
        int dishTaps = 0;
        long selectionTime = 0;
        while (SystemClock.uptimeMillis() < end) {
            AccessibilityNodeInfo root = pickerRoot(automation);
            if (selected) {
                // A departing picker can remain in the accessibility window
                // list while the app has already regained real input focus.
                boolean[] appFocused = {false};
                runOnMainSync(() -> appFocused[0] = web.isShown() && web.hasWindowFocus());
                if (appFocused[0] && (!page || confirmed)) return;
            }
            if (root != null) {
                sawPicker = true;
                if (selected) {
                    if (!save && !confirmed) confirmed = confirmDocuments(automation, root);
                }
                // If the single-file picker still owns focus and has no
                // confirmation control, reacquire the file before another tap.
                boolean retryDish = !save && !page && !confirmed && dishTaps < 3
                    && SystemClock.uptimeMillis() - selectionTime >= 2_500;
                if (!selected || retryDish) {
                    AccessibilityNodeInfo file = save ? null : fileTitle(root, target, 0);
                    if (file == null) file = named(root, target, false, 0);
                    sawTarget |= file != null;
                    // In the multi-file picker, select the file with a long
                    // press and use the action bar to confirm the selection.
                    boolean sent = save ? tapDocument(automation, root, file)
                        : touchDocument(automation, root, file,
                            page ? ViewConfiguration.getLongPressTimeout() + 150 : 50);
                    if (sent) {
                        selected = true;
                        selectionTime = SystemClock.uptimeMillis();
                        if (!save && !page) dishTaps++;
                    }
                    if (!selected && file == null && !save) {
                        if (!openedRoots) openedRoots = tapDocument(automation, root, named(root, "Show roots", false, 0));
                        else if (!visitedDownloads) {
                            AccessibilityNodeInfo downloads = named(root, "Downloads", false, 0);
                            if (downloads == null) downloads = named(root, "Download", false, 0);
                            visitedDownloads = tapDocument(automation, root, downloads);
                        }
                    }
                }
            }
            SystemClock.sleep(650);
        }
        throw new IllegalStateException(selected ? page && !confirmed ? "picker-confirmation" : "picker-return" : sawTarget ? "picker-activation"
            : sawPicker ? "picker-target" : "picker-window");
    }

    private AccessibilityNodeInfo speechNode(AccessibilityNodeInfo node, String label, int depth) {
        if (node == null || depth > 32) return null;
        String text = String.valueOf(node.getText()).strip().replaceAll("\\s+", " ");
        String description = String.valueOf(node.getContentDescription()).strip().replaceAll("\\s+", " ");
        if (node.isVisibleToUser() && (text.equals(label) || description.equals(label)
                || text.startsWith(label + " ") || description.startsWith(label + " "))) {
            if (label.equals("Help improve MyChef")) {
                AccessibilityNodeInfo candidate = node;
                for (int i = 0; i < 4 && candidate != null; i++, candidate = candidate.getParent()) {
                    if (candidate.isCheckable()) return candidate;
                }
            } else return node;
        }
        for (int i = 0; i < node.getChildCount(); i++) {
            AccessibilityNodeInfo result = speechNode(node.getChild(i), label, depth + 1);
            if (result != null) return result;
        }
        return null;
    }

    private AccessibilityNodeInfo speechWindow(UiAutomation automation) {
        String owner = getTargetContext().getPackageName();
        for (AccessibilityWindowInfo window : automation.getWindows()) {
            if (window.getType() != AccessibilityWindowInfo.TYPE_APPLICATION) continue;
            AccessibilityNodeInfo root = window.getRoot();
            if (root != null && owner.equals(String.valueOf(root.getPackageName()))) return root;
        }
        AccessibilityNodeInfo root = automation.getRootInActiveWindow();
        return root != null && owner.equals(String.valueOf(root.getPackageName())) ? root : null;
    }

    private void speechFocus(String target) throws Exception {
        speechTouchMode();
        String label = switch (target) {
            case "on", "off" -> "Help improve MyChef";
            case "stale" -> "Your setting changed elsewhere.";
            case "offline" -> "Turning off was not confirmed.";
            case "cleanup" -> "Off. Active deletion is in progress.";
            case "backup" -> "Off. Active data is erased; isolated backup cleanup is pending";
            default -> throw new IllegalStateException("accessibility-controls");
        };
        UiAutomation automation = interactiveWindows();
        long end = SystemClock.uptimeMillis() + 18_000;
        boolean hadWindow = false, hadNode = false, hadState = false;
        while (SystemClock.uptimeMillis() < end) {
            AccessibilityNodeInfo root = speechWindow(automation);
            hadWindow |= root != null;
            AccessibilityNodeInfo node = speechNode(root, label, 0);
            hadNode |= node != null;
            if (node != null && (!target.equals("on") && !target.equals("off")
                    || node.isCheckable() && node.isChecked() == target.equals("on"))) {
                hadState = true;
                node.performAction(AccessibilityNodeInfo.ACTION_CLEAR_ACCESSIBILITY_FOCUS);
                SystemClock.sleep(150);
                if (!node.refresh()) continue;
                try {
                    AccessibilityEvent event = automation.executeAndWaitForEvent(
                        () -> node.performAction(AccessibilityNodeInfo.ACTION_ACCESSIBILITY_FOCUS),
                        value -> value.getEventType() == AccessibilityEvent.TYPE_VIEW_ACCESSIBILITY_FOCUSED
                            && getTargetContext().getPackageName().equals(String.valueOf(value.getPackageName()))
                            && node.equals(value.getSource()), 3500);
                    event.recycle();
                    return;
                } catch (java.util.concurrent.TimeoutException ignored) { }
            }
            SystemClock.sleep(200);
        }
        throw new IllegalStateException(!hadWindow ? "speech-window" : !hadNode ? "speech-node" : !hadState ? "speech-state" : "speech-event");
    }

    private void speechTouchMode() {
        AccessibilityManager manager = getTargetContext().getSystemService(AccessibilityManager.class);
        long end = SystemClock.uptimeMillis() + 15_000;
        String reason = "speech-touch-service";
        while (manager != null && SystemClock.uptimeMillis() < end) {
            for (AccessibilityServiceInfo info : manager.getEnabledAccessibilityServiceList(AccessibilityServiceInfo.FEEDBACK_SPOKEN)) {
                if (!info.getId().startsWith("com.google.android.marvin.talkback/")) continue;
                if ((info.getCapabilities() & AccessibilityServiceInfo.CAPABILITY_CAN_REQUEST_TOUCH_EXPLORATION) == 0)
                    reason = "speech-touch-capability";
                else if ((info.flags & AccessibilityServiceInfo.FLAG_REQUEST_TOUCH_EXPLORATION_MODE) == 0)
                    reason = "speech-touch-request";
                else if (!manager.isTouchExplorationEnabled()) reason = "speech-touch-mode";
                else return;
            }
            SystemClock.sleep(200);
        }
        throw new IllegalStateException(reason);
    }

    private void speechToggle(String before) throws Exception {
        speechTouchMode();
        speechFocus(before);
        // The coordinator now sends the hardware gesture through the
        // run-owned emulator window. UiAutomation injection bypasses InputFilter.
    }

    private String orderTrace = "";

    private void speechOrder() throws Exception {
        speechTouchMode();
        UiAutomation automation = interactiveWindows();
        boolean status = false, hadWindow = false, hadFocus = false;
        orderTrace = "";
        long end = SystemClock.uptimeMillis() + 22_000;
        while (SystemClock.uptimeMillis() < end) {
            AccessibilityNodeInfo root = speechWindow(automation);
            AccessibilityNodeInfo focused = root == null ? null : root.findFocus(AccessibilityNodeInfo.FOCUS_ACCESSIBILITY);
            hadWindow |= root != null;
            hadFocus |= focused != null;
            String category = root == null ? "window" : focused == null ? "none" : "other";
            if (focused != null) {
                String text = String.valueOf(focused.getText()).strip();
                String description = String.valueOf(focused.getContentDescription()).strip();
                category = text.equals("On") || description.equals("On") ? "status"
                    : text.equals("Refresh analytics setting") || description.equals("Refresh analytics setting") ? "refresh"
                    : text.startsWith("Help improve MyChef") || description.startsWith("Help improve MyChef") ? "choice" : "other";
                if (!orderTrace.equals(category) && !orderTrace.endsWith("," + category) && orderTrace.split(",").length < 16)
                    orderTrace += (orderTrace.isEmpty() ? "" : ",") + category;
                if (text.equals("On") || description.equals("On")) status = true;
                if (text.equals("Refresh analytics setting") || description.equals("Refresh analytics setting")) {
                    if (status) return;
                    throw new IllegalStateException("speech-order-status");
                }
            }
            if (focused == null && !orderTrace.equals(category) && !orderTrace.endsWith("," + category)
                    && orderTrace.split(",").length < 16)
                orderTrace += (orderTrace.isEmpty() ? "" : ",") + category;
            SystemClock.sleep(100);
        }
        throw new IllegalStateException(!hadWindow ? "speech-order-window" : !hadFocus ? "speech-order-focus"
            : !status ? "speech-order-status" : "speech-order-refresh");
    }

    private void checkAccessibility(String group) {
        String[] labels = group.equals("nav") ? new String[]{"Meals", "Shopping list", "Profile"}
            : new String[]{"Cancel", "Download ZIP"};
        long end = SystemClock.uptimeMillis() + 25_000;
        while (SystemClock.uptimeMillis() < end) {
            AccessibilityNodeInfo root = getUiAutomation(UiAutomation.FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES).getRootInActiveWindow();
            boolean ready = root != null && getTargetContext().getPackageName().equals(String.valueOf(root.getPackageName()));
            for (String label : labels) {
                if (!ready) break;
                ready = named(root, label, true, 0) != null;
            }
            if (ready) return;
            SystemClock.sleep(500);
        }
        throw new IllegalStateException("accessibility-controls");
    }

    @Override public void onStart() {
        File stop = new File(getTargetContext().getCacheDir(), "native-check-finish");
        File command = new File(getTargetContext().getCacheDir(), "native-check-command");
        File done = new File(getTargetContext().getCacheDir(), "native-check-done");
        stop.delete(); command.delete(); done.delete();
        try {
            // Prime the non-suppressing connection before the first UI action.
            if (getUiAutomation(UiAutomation.FLAG_DONT_SUPPRESS_ACCESSIBILITY_SERVICES) == null)
                throw new IllegalStateException("automation-connect");
            Intent launch = getTargetContext().getPackageManager().getLaunchIntentForPackage(getTargetContext().getPackageName());
            if (launch == null) throw new IllegalStateException("launch");
            launch.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
            Activity activity = startActivitySync(launch);
            for (int i = 0; i < 100 && web == null; i++) {
                runOnMainSync(this::refreshWeb);
                if (web == null) SystemClock.sleep(100);
            }
            if (web == null) throw new IllegalStateException("webview");
            boolean[] focused = {false};
            boolean systemUiRecoveryAttempted = false;
            for (int i = 0; i < 300 && !focused[0]; i++) {
                runOnMainSync(() -> {
                    refreshWeb();
                    focused[0] = web.isShown() && web.hasWindowFocus();
                });
                if (!focused[0]) {
                    if (!systemUiRecoveryAttempted && i % 10 == 0) {
                        String recovery = recoverSystemUiAnr();
                        if (!recovery.equals("absent")) {
                            systemUiRecoveryAttempted = true;
                            Bundle result = new Bundle(); result.putString("probeSystemUiRecovery", recovery);
                            sendStatus(1, result);
                            getTargetContext().startActivity(launch);
                        }
                    }
                    // Bring only this fixture's activity forward after cold
                    // system startup; never accept an unfocused app window.
                    if (i > 0 && i % 50 == 0) getTargetContext().startActivity(launch);
                    SystemClock.sleep(100);
                }
            }
            if (!focused[0]) {
                Bundle diagnostic = new Bundle();
                diagnostic.putString("probeActivityReplaced", Boolean.toString(currentActivity != activity));
                runOnMainSync(() -> {
                    diagnostic.putString("probeActivityDestroyed", Boolean.toString(currentActivity == null || currentActivity.isDestroyed()));
                    diagnostic.putString("probeWebShown", Boolean.toString(web.isShown()));
                    diagnostic.putString("probeWindowFocus", Boolean.toString(web.hasWindowFocus()));
                });
                sendStatus(1, diagnostic);
                throw new IllegalStateException("native-window-unfocused");
            }
            boolean[] zoom = {false};
            runOnMainSync(() -> zoom[0] = web.getSettings().getBuiltInZoomControls());
            if (!zoom[0]) throw new IllegalStateException("pinch-disabled");
            // Privileged test instrumentation opens a local ADB inspection port.
            // The packaged app's config and normal runtime keep debugging off.
            runOnMainSync(() -> WebView.setWebContentsDebuggingEnabled(true));
            Bundle ready = new Bundle(); ready.putString("probe", "ready"); sendStatus(1, ready);
            long deadline = SystemClock.uptimeMillis() + 900_000;
            while (!stop.exists() && SystemClock.uptimeMillis() < deadline) {
                if (command.exists()) {
                    runOnMainSync(this::refreshWeb);
                    String value = new String(Files.readAllBytes(command.toPath()), StandardCharsets.UTF_8).trim();
                    command.delete();
                    if (value.matches("system-ui:[1-9][0-9]{0,3}:RECOVER")) {
                        if (systemUiRecoveryAttempted) throw new IllegalStateException("native-window-unfocused");
                        systemUiRecoveryAttempted = true;
                        String recovery = recoverSystemUiAnr();
                        Bundle result = new Bundle(); result.putString("probeSystemUiRecovery", recovery);
                        sendStatus(1, result);
                        if (!recovery.equals("closed")) throw new IllegalStateException("native-window-unfocused");
                        Files.write(done.toPath(), value.split(":", 3)[1].getBytes(StandardCharsets.UTF_8));
                    }
                    else if (value.equals("pinch")) pinch();
                    else if (value.matches("activate:[1-9][0-9]{0,3}:[A-Za-z -]{1,50}")) {
                        String[] parts = value.split(":", 3);
                        activate(parts[2]);
                        Files.write(done.toPath(), parts[1].getBytes(StandardCharsets.UTF_8));
                    } else if (value.matches("document:[1-9][0-9]{0,3}:(SAVE|native-(dish|page)\\.png)")) {
                        String[] parts = value.split(":", 3);
                        selectSystemDocument(parts[2]);
                        Files.write(done.toPath(), parts[1].getBytes(StandardCharsets.UTF_8));
                    } else if (value.matches("speech-focus:[1-9][0-9]{0,3}:(on|off|stale|offline|cleanup|backup)")) {
                        String[] parts = value.split(":", 3);
                        speechFocus(parts[2]);
                        Files.write(done.toPath(), parts[1].getBytes(StandardCharsets.UTF_8));
                    } else if (value.matches("speech-toggle:[1-9][0-9]{0,3}:(on|off)")) {
                        String[] parts = value.split(":", 3);
                        speechToggle(parts[2]);
                        Files.write(done.toPath(), parts[1].getBytes(StandardCharsets.UTF_8));
                    } else if (value.matches("speech-order:[1-9][0-9]{0,3}:on")) {
                        String[] parts = value.split(":", 3);
                        speechOrder();
                        Files.write(done.toPath(), parts[1].getBytes(StandardCharsets.UTF_8));
                    } else if (value.matches("accessibility:[1-9][0-9]{0,3}:(nav|export-dialog)")) {
                        String[] parts = value.split(":", 3);
                        checkAccessibility(parts[2]);
                        Files.write(done.toPath(), parts[1].getBytes(StandardCharsets.UTF_8));
                    } else throw new IllegalStateException("native-control");
                }
                SystemClock.sleep(100);
            }
            if (!stop.exists()) throw new IllegalStateException("probe-timeout");
            finish(Activity.RESULT_OK, new Bundle());
        } catch (Exception error) {
            Bundle failure = new Bundle(); failure.putString("probe", "failed");
            if (!orderTrace.isEmpty()) failure.putString("probeOrder", orderTrace);
            String reason = error.getMessage();
            if (reason != null && reason.contains("already registered")) reason = "automation-busy";
            else if (reason != null && (reason.contains("UiAutomation not connected") || reason.contains("connecting UiAutomation"))) reason = "automation-connect";
            failure.putString("probeReason", reason != null && reason.matches("launch|webview|pinch-disabled|probe-timeout|native-control|native-window-(?:unfocused|bounds)|control-(?:timeout|target|geometry|viewport|outside-viewport|outside-webview|screen-bounds)|input-denied|picker-(?:window|target|activation|confirmation|return)|accessibility-controls|speech-(window|node|state|event|touch-mode|touch-request|touch-capability|touch-service|order-window|order-focus|order-status|order-refresh)|automation-(busy|connect)") ? reason : "instrumentation");
            finish(Activity.RESULT_CANCELED, failure);
        } finally {
            runOnMainSync(() -> WebView.setWebContentsDebuggingEnabled(false));
            stop.delete(); command.delete(); done.delete();
        }
    }
}
