package com.gkldevelopment.mychef.testing;

import java.util.regex.Matcher;
import java.util.regex.Pattern;

/** Identity gate for the disposable emulator's one allowed startup dismissal. */
public final class NativeStartupWindow {
    private NativeStartupWindow() {}
    private static final String TITLE = "Application Not Responding: com.android.systemui";

    public static String focusedTitle(String snapshot) {
        if (snapshot == null || snapshot.length() > 131072) return "";
        Matcher focus = Pattern.compile("(?m)^\\h*mCurrentFocus=([^\\r\\n]*)\\r?$").matcher(snapshot);
        if (!focus.find()) return "";
        String value = focus.group(1);
        if (focus.find()) return "";
        return value.matches("Window\\{[a-f0-9]+ u0 " + Pattern.quote(TITLE) + "\\}") ? TITLE : "";
    }

    public static boolean canClose(String title, String rootOwner, String buttonOwner, String buttonId) {
        return TITLE.equals(title)
            && "android".equals(rootOwner) && "android".equals(buttonOwner)
            && "android:id/aerr_close".equals(buttonId);
    }
}
