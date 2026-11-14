package com.gkldevelopment.mychef.preview;

import android.content.Context;

// Public builds never receive Firebase project configuration or credentials.
final class ReminderRegistration {
    static boolean available(Context context) { return false; }
    static void start(MainActivity activity) {}
    static void stop(Context context) { ReminderNotifications.stop(); }
    private ReminderRegistration() {}
}
