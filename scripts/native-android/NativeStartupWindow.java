package com.gkldevelopment.mychef.testing;

/** Identity gate for the disposable emulator's one allowed startup dismissal. */
public final class NativeStartupWindow {
    private NativeStartupWindow() {}

    public static boolean canClose(String title, String rootOwner, String buttonOwner, String buttonId) {
        return "Application Not Responding: com.android.systemui".equals(title)
            && "android".equals(rootOwner) && "android".equals(buttonOwner)
            && "android:id/aerr_close".equals(buttonId);
    }
}
