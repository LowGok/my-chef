package com.gkldevelopment.mychef.preview;

// One proof in process memory, never an Intent extra or saved instance state.
public final class NativeRuntimeState {
    private String link;
    private long received;
    private boolean back;
    public synchronized void link(String value, long now) {
        link = value != null && value.length() <= 512 ? value : null; received = now;
    }
    public synchronized String takeLink(long now) {
        String value = now >= received && now - received <= 300_000 ? link : null;
        link = null; return value;
    }
    public synchronized void back() { back = true; }
    public synchronized boolean takeBack() { boolean value = back; back = false; return value; }
}
