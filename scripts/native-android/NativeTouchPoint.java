package com.gkldevelopment.mychef.testing;

/** Geometry for disposable instrumentation only, never part of the MyChef app. */
public final class NativeTouchPoint {
    private NativeTouchPoint() {}

    public static int[] locate(double[] point, int[] origin, int width, int height, boolean focused) {
        if (!focused) throw new IllegalArgumentException("native-window-unfocused");
        if (origin == null || origin.length != 2 || width <= 0 || height <= 0)
            throw new IllegalArgumentException("native-window-bounds");
        if (point == null || point.length != 6) throw new IllegalArgumentException("native-control");
        for (double value : point) if (!Double.isFinite(value)) throw new IllegalArgumentException("native-control");
        if (point[4] <= 0 || point[5] <= 0) throw new IllegalArgumentException("native-control");
        double x = point[0] - point[2], y = point[1] - point[3];
        if (x < 0 || y < 0 || x >= point[4] || y >= point[5]) throw new IllegalArgumentException("native-control");
        double scale = width / point[4];
        int localX = (int)Math.round(x * scale), localY = (int)Math.round(y * scale);
        if (localX < 0 || localY < 0 || localX >= width || localY >= height)
            throw new IllegalArgumentException("native-control");
        long screenX = (long) origin[0] + localX, screenY = (long) origin[1] + localY;
        if (screenX < 0 || screenY < 0 || screenX > Integer.MAX_VALUE || screenY > Integer.MAX_VALUE)
            throw new IllegalArgumentException("native-control");
        return new int[]{(int) screenX, (int) screenY};
    }
}
