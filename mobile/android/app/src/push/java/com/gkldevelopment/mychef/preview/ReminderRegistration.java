package com.gkldevelopment.mychef.preview;

import android.content.Context;
import com.google.firebase.FirebaseApp;
import com.google.firebase.messaging.FirebaseMessaging;

final class ReminderRegistration {
    static boolean available(Context context) {
        return !FirebaseApp.getApps(context).isEmpty();
    }

    static void start(MainActivity activity) {
        if (!available(activity)) return;
        long generation = ReminderNotifications.begin(activity);
        if (generation < 0) return;
        // The manifest prevents startup registration; getToken is called only
        // after the signed-in app verifies its opt-in and OS permission.
        FirebaseMessaging messaging = FirebaseMessaging.getInstance();
        messaging.getToken().addOnCompleteListener(activity, result -> {
            if (result.isSuccessful()) ReminderNotifications.registered(result.getResult(), generation);
            else ReminderNotifications.failed(generation);
            activity.signalRuntime();
        });
    }

    static void stop(Context context) {
        if (available(context) && ReminderNotifications.registrationGeneration() >= 0) {
            FirebaseMessaging.getInstance().setAutoInitEnabled(false);
        }
        ReminderNotifications.stop();
    }

    private ReminderRegistration() {}
}
