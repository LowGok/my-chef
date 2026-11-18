package com.gkldevelopment.mychef.preview;

/** Uses actual package identity; a client declaration alone grants nothing. */
public final class NativeAppIdentity {
    private NativeAppIdentity() {}
    public static String packageFor(String lane) {
        if ("preview".equals(lane)) return "com.gkldevelopment.mychef.preview";
        if ("app".equals(lane)) return "dev.mychef.app";
        throw new IllegalArgumentException("Unsupported native identity");
    }
    public static void require(String installedPackage, String lane) {
        if (!packageFor(lane).equals(installedPackage)) {
            throw new IllegalArgumentException("Native package differs from its prepared identity");
        }
    }
}
