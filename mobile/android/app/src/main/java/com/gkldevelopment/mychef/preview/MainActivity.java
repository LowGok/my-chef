package com.gkldevelopment.mychef.preview;

import com.getcapacitor.BridgeActivity;
import android.os.Bundle;
import android.os.SystemClock;
import android.content.Intent;
import androidx.activity.OnBackPressedCallback;

public class MainActivity extends BridgeActivity {
    final NativeRuntimeState runtime = new NativeRuntimeState();
    volatile boolean active;
    private void accept(Intent intent) {
        if (intent != null && intent.getData() != null && Intent.ACTION_VIEW.equals(intent.getAction())) {
            runtime.link(intent.getDataString(), SystemClock.elapsedRealtime());
            // Capacitor captures the launch URI. Strip it before handing over.
            intent.setData(null); intent.replaceExtras((Bundle)null);
        }
    }
    @Override public void onCreate(Bundle savedInstanceState) {
        accept(getIntent());
        registerPlugin(MyChefNativePlugin.class);
        super.onCreate(savedInstanceState);
        getOnBackPressedDispatcher().addCallback(this, new OnBackPressedCallback(true) {
            @Override public void handleOnBackPressed() { runtime.back(); signalRuntime(); }
        });
    }
    @Override protected void onNewIntent(Intent intent) {
        accept(intent); super.onNewIntent(intent); setIntent(intent); signalRuntime();
    }
    @Override public void onResume() { super.onResume(); active = true; signalRuntime(); }
    @Override public void onPause() { active = false; signalRuntime(); super.onPause(); }
    void signalRuntime() {
        if (getBridge() == null || getBridge().getPlugin("MyChefNative") == null) return;
        ((MyChefNativePlugin)getBridge().getPlugin("MyChefNative").getInstance()).runtimeChanged();
    }
}
