package com.gkldevelopment.mychef.testing;

import android.app.Activity;
import android.app.Instrumentation;
import android.content.Intent;
import android.os.Bundle;
import android.os.SystemClock;
import android.view.InputDevice;
import android.view.MotionEvent;
import android.view.View;
import android.view.ViewGroup;
import android.view.accessibility.AccessibilityNodeInfo;
import android.webkit.WebView;
import java.io.File;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.util.Arrays;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import org.json.JSONArray;
import org.json.JSONObject;

/** Installed only in a disposable emulator, never included in the MyChef APK. */
public final class NativeJourneyProbe extends Instrumentation {
    private WebView web;

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
        try { getUiAutomation().injectInputEvent(event, true); } finally { event.recycle(); }
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
                "Choose recipe photo", "Recipe-page photos").contains(label))
            throw new IllegalStateException("native-control");
        double[][] point = {null};
        CountDownLatch evaluated = new CountDownLatch(1);
        String quotedLabel = JSONObject.quote(label);
        String selector = switch (label) {
            case "Current password" -> "[...document.querySelectorAll('dialog[open] input[name=currentPassword]')].filter(n=>!n.disabled&&n.getClientRects().length)";
            case "Choose recipe photo", "Recipe-page photos" ->
                "[...document.querySelectorAll('input[type=button]')].filter(n=>!n.disabled&&n.getClientRects().length&&[...n.labels].some(l=>(l.textContent||'').trim()==="
                    + quotedLabel + "))";
            default -> "[...document.querySelectorAll('button')].filter(n=>(n.textContent||'').trim()==="
                + quotedLabel + "&&!n.disabled&&n.getClientRects().length)";
        };
        String expression = "(() => {const nodes=" + selector + ";"
            + "if(nodes.length!==1)return null;const r=nodes[0].getBoundingClientRect(),v=visualViewport;"
            + "return [r.x+r.width/2,r.y+r.height/2,v?v.offsetLeft:0,v?v.offsetTop:0,v?v.width:innerWidth,v?v.height:innerHeight]})()";
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
        if (!evaluated.await(10, TimeUnit.SECONDS)) throw new IllegalStateException("control-timeout");
        int[] location = new int[2], size = new int[2];
        boolean[] focused = {false};
        runOnMainSync(() -> {
            web.getLocationOnScreen(location); size[0] = web.getWidth(); size[1] = web.getHeight();
            focused[0] = web.isShown() && web.hasWindowFocus();
        });
        int[] target = NativeTouchPoint.locate(point[0], location, size[0], size[1], focused[0]);
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

    private static AccessibilityNodeInfo named(AccessibilityNodeInfo node, String label, boolean control, int depth) {
        if (node == null || depth > 32) return null;
        if (node.isVisibleToUser() && (!control || node.isEnabled() && node.isClickable() && node.isFocusable())
                && (label.equalsIgnoreCase(String.valueOf(node.getText()))
                || label.equalsIgnoreCase(String.valueOf(node.getContentDescription())))) return node;
        for (int i = 0; i < node.getChildCount(); i++) {
            AccessibilityNodeInfo result = named(node.getChild(i), label, control, depth + 1);
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

    private void selectSystemDocument(String target) {
        long end = SystemClock.uptimeMillis() + 30_000;
        boolean selected = false;
        while (SystemClock.uptimeMillis() < end) {
            AccessibilityNodeInfo root = getUiAutomation().getRootInActiveWindow();
            if (selected && root != null && getTargetContext().getPackageName().equals(String.valueOf(root.getPackageName()))) return;
            if (documents(root)) {
                if (selected) {
                    if (target.equals("native-page.png")) clickDocuments(named(root, "Open", true, 0));
                } else if (clickDocuments(named(root, target, false, 0))) selected = true;
                else if (!target.equals("SAVE")) {
                    if (!clickDocuments(named(root, "Downloads", false, 0))
                            && !clickDocuments(named(root, "Download", false, 0)))
                        clickDocuments(named(root, "Show roots", false, 0));
                }
            }
            SystemClock.sleep(650);
        }
        throw new IllegalStateException("picker-control");
    }

    private void checkAccessibility(String group) {
        String[] labels = group.equals("nav") ? new String[]{"Meals", "Shopping list", "Profile"}
            : new String[]{"Cancel", "Download ZIP"};
        long end = SystemClock.uptimeMillis() + 25_000;
        while (SystemClock.uptimeMillis() < end) {
            AccessibilityNodeInfo root = getUiAutomation().getRootInActiveWindow();
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
            Intent launch = getTargetContext().getPackageManager().getLaunchIntentForPackage(getTargetContext().getPackageName());
            if (launch == null) throw new IllegalStateException("launch");
            launch.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
            Activity activity = startActivitySync(launch);
            for (int i = 0; i < 100 && web == null; i++) {
                runOnMainSync(() -> web = find(activity.getWindow().getDecorView()));
                if (web == null) SystemClock.sleep(100);
            }
            if (web == null) throw new IllegalStateException("webview");
            boolean[] focused = {false};
            for (int i = 0; i < 100 && !focused[0]; i++) {
                runOnMainSync(() -> focused[0] = web.isShown() && web.hasWindowFocus());
                if (!focused[0]) SystemClock.sleep(100);
            }
            if (!focused[0]) throw new IllegalStateException("native-window-unfocused");
            boolean[] zoom = {false};
            runOnMainSync(() -> zoom[0] = web.getSettings().getBuiltInZoomControls());
            if (!zoom[0]) throw new IllegalStateException("pinch-disabled");
            // Privileged test instrumentation opens a local ADB inspection port.
            // The packaged app's config and normal runtime keep debugging off.
            runOnMainSync(() -> WebView.setWebContentsDebuggingEnabled(true));
            Bundle ready = new Bundle(); ready.putString("probe", "ready"); sendStatus(1, ready);
            long deadline = SystemClock.uptimeMillis() + 600_000;
            while (!stop.exists() && SystemClock.uptimeMillis() < deadline) {
                if (command.exists()) {
                    String value = new String(Files.readAllBytes(command.toPath()), StandardCharsets.UTF_8).trim();
                    command.delete();
                    if (value.equals("pinch")) pinch();
                    else if (value.matches("activate:[1-9][0-9]{0,3}:[A-Za-z -]{1,50}")) {
                        String[] parts = value.split(":", 3);
                        activate(parts[2]);
                        Files.write(done.toPath(), parts[1].getBytes(StandardCharsets.UTF_8));
                    } else if (value.matches("document:[1-9][0-9]{0,3}:(SAVE|native-(dish|page)\\.png)")) {
                        String[] parts = value.split(":", 3);
                        selectSystemDocument(parts[2]);
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
            String reason = error.getMessage();
            failure.putString("probeReason", reason != null && reason.matches("launch|webview|pinch-disabled|probe-timeout|native-control|native-window-(?:unfocused|bounds)|control-timeout|input-denied|picker-control|accessibility-controls") ? reason : "instrumentation");
            finish(Activity.RESULT_CANCELED, failure);
        } finally {
            runOnMainSync(() -> WebView.setWebContentsDebuggingEnabled(false));
            stop.delete(); command.delete(); done.delete();
        }
    }
}
