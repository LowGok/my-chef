package com.gkldevelopment.mychef.preview;

import android.Manifest;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.content.Context;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.os.Build;
import android.provider.Settings;

import androidx.core.content.ContextCompat;

final class ReminderNotifications {
    static final String CHANNEL = "mychef-planning";
    private static boolean started;
    private static String token;
    private static String reminder;
    private static long received;
    private static long generation;

    static boolean allowed(Context context) {
        NotificationManager manager = context.getSystemService(NotificationManager.class);
        if (manager == null || !manager.areNotificationsEnabled()) return false;
        if (Build.VERSION.SDK_INT >= 33 && ContextCompat.checkSelfPermission(context,
                Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) return false;
        NotificationChannel channel = Build.VERSION.SDK_INT >= 26 ? manager.getNotificationChannel(CHANNEL) : null;
        return channel == null || channel.getImportance() != NotificationManager.IMPORTANCE_NONE;
    }

    static synchronized long begin(Context context) {
        if (started) return -1;
        if (!allowed(context)) return -1;
        NotificationManager manager = context.getSystemService(NotificationManager.class);
        if (Build.VERSION.SDK_INT >= 26 && manager != null) {
            manager.createNotificationChannel(new NotificationChannel(CHANNEL,
                    "Planning reminders", NotificationManager.IMPORTANCE_DEFAULT));
        }
        started = true;
        return ++generation;
    }

    static synchronized void registered(String value, long attempt) {
        if (started && generation == attempt && value != null && value.matches("[A-Za-z0-9_:-]{32,512}")) token = value;
    }

    static synchronized void failed(long attempt) {
        if (generation == attempt) {
            started = false;
            token = null;
        }
    }

    static synchronized long registrationGeneration() {
        return started ? generation : -1;
    }

    static synchronized void stop() {
        generation++;
        started = false;
        token = null;
    }

    static synchronized String token() { return started ? token : null; }

    static synchronized void receive(String value, long now) {
        if (value != null && value.matches("[a-f0-9]{32}")) {
            reminder = value;
            received = now;
        }
    }

    static synchronized String take(long now) {
        String value = now >= received && now - received < 300000 ? reminder : null;
        reminder = null;
        return value;
    }

    static Intent settings(Context context) {
        return new Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS)
                .putExtra(Settings.EXTRA_APP_PACKAGE, context.getPackageName());
    }

    private ReminderNotifications() {}
}
