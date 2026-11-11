package com.gkldevelopment.mychef.preview;

import java.net.URI;
import java.nio.ByteBuffer;
import java.nio.ByteOrder;
import java.util.HashMap;
import java.util.Locale;
import java.util.Map;
import java.util.Set;
import java.util.Arrays;
import java.util.HashSet;

// Kept independent of Android so the actual boundary can also be tested on JVM.
public final class NativeBoundary {
    public static final int MAX_REQUEST = 24 * 1024 * 1024;
    public static final int MAX_RESPONSE = 32 * 1024 * 1024;
    private static final Set<String> HEADERS = new HashSet<>(Arrays.asList(
        "accept", "content-type", "authorization", "idempotency-key", "if-match", "if-none-match"));

    public static URI origin(String value) {
        URI uri = URI.create(value);
        String host = uri.getHost();
        if (!"https".equals(uri.getScheme()) || host == null ||
            !(host.equals("localhost") || host.matches("[a-z0-9-]+\\.tail[a-z0-9]+\\.ts\\.net")) ||
            uri.getRawUserInfo() != null || uri.getRawQuery() != null || uri.getRawFragment() != null ||
            !uri.getRawPath().isEmpty() || uri.getPort() == 0 || uri.getPort() > 65535) {
            throw new IllegalArgumentException("Invalid private API origin");
        }
        return uri;
    }

    public static URI target(URI origin, String path) {
        if (path == null || !path.startsWith("/api/native/v1/") || path.contains("\\") ||
            path.contains("#") || path.contains("\r") || path.contains("\n") ||
            path.split("\\?", 2)[0].contains("%")) throw new IllegalArgumentException("Invalid API path");
        URI uri = origin.resolve(path);
        if (!uri.normalize().equals(uri) || !origin.getRawAuthority().equals(uri.getRawAuthority()) ||
            !origin.getScheme().equals(uri.getScheme()) || !uri.getRawPath().startsWith("/api/native/v1/")) {
            throw new IllegalArgumentException("Invalid API path");
        }
        return uri;
    }

    public static Map<String, String> headers(Map<String, String> input) {
        Map<String, String> output = new HashMap<>();
        for (Map.Entry<String, String> entry : input.entrySet()) {
            String key = entry.getKey().toLowerCase(Locale.ROOT), value = entry.getValue();
            if (!HEADERS.contains(key)) continue;
            if (value == null || value.length() > 8192 || value.contains("\r") || value.contains("\n") ||
                (key.equals("authorization") && !value.matches("Bearer [a-f0-9]{64}"))) {
                throw new IllegalArgumentException("Invalid API header");
            }
            output.put(key, value);
        }
        return output;
    }
    public static URI publicPhoto(String value) {
        URI uri = URI.create(value);
        if (value.length() > 4096 || !"https".equals(uri.getScheme()) || !"res.cloudinary.com".equals(uri.getHost()) ||
            (uri.getPort() != -1 && uri.getPort() != 443) || uri.getRawUserInfo() != null || uri.getRawFragment() != null)
            throw new IllegalArgumentException("Invalid public photo");
        return uri;
    }
    public static URI externalLink(String value) {
        URI uri = URI.create(value);
        boolean mail = value.matches("mailto:[A-Za-z0-9._+-]+@[A-Za-z0-9.-]+\\.[A-Za-z]{2,}");
        if (value.length() > 4096 || (!mail && (!"https".equals(uri.getScheme()) || uri.getHost() == null)) ||
            uri.getRawUserInfo() != null || value.contains("\r") || value.contains("\n")) throw new IllegalArgumentException("Invalid external link");
        return uri;
    }
    public static String accountLink(URI origin, String value) {
        if (value == null || value.length() > 512) throw new IllegalArgumentException("Invalid account link");
        URI uri = URI.create(value);
        String path = uri.getRawPath(), fragment = uri.getRawFragment();
        if (uri.getRawQuery() != null || !Arrays.asList("/app/signup", "/app/verify-email", "/app/change-email", "/app/reset-password").contains(path) ||
            fragment == null || !fragment.matches("[A-Za-z0-9_-]{43}") || !value.equals(origin.toString()+path+"#"+fragment)) throw new IllegalArgumentException("Invalid account link");
        return value;
    }
    public static String exportName(String value) {
        if (value == null || !value.matches("mychef-data-[a-f0-9]{32}\\.zip")) throw new IllegalArgumentException("Invalid export name");
        return value;
    }
    public static void zip(byte[] bytes) {
        if (bytes.length < 22 || bytes.length > MAX_RESPONSE || ByteBuffer.wrap(bytes, bytes.length - 22, 4).order(ByteOrder.LITTLE_ENDIAN).getInt() != 0x06054b50)
            throw new IllegalArgumentException("Invalid account export");
    }
}
