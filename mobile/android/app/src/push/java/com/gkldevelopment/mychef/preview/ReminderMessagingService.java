package com.gkldevelopment.mychef.preview;

import android.app.Notification;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Intent;
import android.os.Build;
import com.google.firebase.messaging.FirebaseMessagingService;
import com.google.firebase.messaging.RemoteMessage;

public final class ReminderMessagingService extends FirebaseMessagingService {
    @Override public void onNewToken(String token) {
        ReminderNotifications.registered(token, ReminderNotifications.registrationGeneration());
        MainActivity activity = MainActivity.activeReminderActivity();
        if (activity != null) activity.runOnUiThread(activity::signalRuntime);
    }

    @Override public void onMessageReceived(RemoteMessage message) {
        String ref = message.getData().get("reminder");
        if (ref == null || !ref.matches("[a-f0-9]{32}") || !ReminderNotifications.allowed(this)) return;
        Intent intent = new Intent(this, MainActivity.class).setAction(Intent.ACTION_MAIN)
                .setFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP | Intent.FLAG_ACTIVITY_SINGLE_TOP).putExtra("reminder", ref);
        PendingIntent tap = PendingIntent.getActivity(this, ref.hashCode(), intent,
                PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
        Notification.Builder notification = Build.VERSION.SDK_INT >= 26
                ? new Notification.Builder(this, ReminderNotifications.CHANNEL) : new Notification.Builder(this);
        notification.setContentTitle("Plan your meals").setContentText("Ready to plan your week?")
                .setSmallIcon(R.mipmap.ic_launcher).setContentIntent(tap).setAutoCancel(true);
        NotificationManager manager = getSystemService(NotificationManager.class);
        if (manager != null) manager.notify(ref, 1, notification.build());
    }
}
