package com.gkldevelopment.mychef.testing;

import java.nio.ByteBuffer;
import java.nio.ByteOrder;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.TimeUnit;

/** Only the disposable probe uses these closed Linux x86_64 input frames. */
public final class NativeTouchFrames {
    public interface Sink { void write(byte[] bytes) throws Exception; }
    private static final class Frame {
        final long delay; final byte[] bytes;
        Frame(long delay, byte[] bytes) { this.delay = delay; this.bytes = bytes; }
    }
    public static void validate(String kind, int x0, int x1, int y0, int y1, int pressure) {
        if (!("double-tap".equals(kind) || "next".equals(kind) || "release".equals(kind))
                || x0 < 0 || x0 > 65535 || x1 > 65535 || x1 < x0 + 64
                || y0 < 0 || y0 > 65535 || y1 > 65535 || y1 < y0 + 64
                || pressure < 0 || pressure > 65535)
            throw new IllegalArgumentException("Closed touch plan required");
    }
    private static void event(ByteBuffer b, int type, int code, int value) {
        b.putLong(0).putLong(0).putShort((short) type).putShort((short) code).putInt(value);
    }
    private static byte[] frame(int x, int y, Integer tracking, Boolean pressed, int pressure, boolean button) {
        ByteBuffer b = ByteBuffer.allocate(8 * 24).order(ByteOrder.LITTLE_ENDIAN);
        event(b,3,47,0);
        if (tracking != null) event(b,3,57,tracking);
        if (x >= 0) { event(b,3,53,x); event(b,3,54,y); }
        if (pressed != null) {
            if (pressure > 0) event(b,3,58,pressed ? Math.max(1,pressure / 2) : 0);
            if (button) event(b,1,330,pressed ? 1 : 0);
        }
        event(b,0,0,0);
        byte[] raw = new byte[b.position()]; b.flip(); b.get(raw); return raw;
    }
    private static int point(int low, int high, double fraction) {
        return low + (int) Math.round((high - low) * fraction);
    }
    public static void write(String kind, int x0, int x1, int y0, int y1, int pressure, boolean button, Sink sink) throws Exception {
        validate(kind,x0,x1,y0,y1,pressure);
        List<Frame> plan = new ArrayList<>();
        byte[] release = frame(-1,-1,-1,false,pressure,button);
        if (kind.equals("release")) plan.add(new Frame(0,release));
        else if (kind.equals("double-tap")) {
            plan.add(new Frame(0,frame(point(x0,x1,.498),point(y0,y1,.499),0,true,pressure,button)));
            plan.add(new Frame(25,release));
            plan.add(new Frame(80,frame(point(x0,x1,.502),point(y0,y1,.501),1,true,pressure,button)));
            plan.add(new Frame(25,release));
        } else {
            plan.add(new Frame(0,frame(point(x0,x1,.3),point(y0,y1,.48),0,true,pressure,button)));
            for (int step=1;step<=3;step++) plan.add(new Frame(20,
                frame(point(x0,x1,.3+.15*step),point(y0,y1,.48+.003*step),null,null,pressure,button)));
            plan.add(new Frame(20,release));
        }
        for (Frame next: plan) {
            // All frame allocation precedes the first contact. Device-local
            // monotonic waits avoid a shell/process launch between records.
            long until = System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(next.delay);
            for (long remaining; (remaining = until - System.nanoTime()) > 0;)
                TimeUnit.NANOSECONDS.sleep(remaining);
            sink.write(next.bytes);
        }
    }
}
