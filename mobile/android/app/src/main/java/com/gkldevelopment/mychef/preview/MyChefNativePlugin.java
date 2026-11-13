package com.gkldevelopment.mychef.preview;

import android.util.Base64;
import android.util.Log;
import android.app.Activity;
import android.content.Intent;
import android.net.Uri;
import android.net.ConnectivityManager;
import android.net.Network;
import android.content.Context;
import android.content.pm.ApplicationInfo;
import android.os.SystemClock;
import android.os.Bundle;
import androidx.activity.result.ActivityResult;
import com.getcapacitor.annotation.ActivityCallback;
import com.getcapacitor.JSArray;
import com.getcapacitor.JSObject;
import com.getcapacitor.Plugin;
import com.getcapacitor.PluginCall;
import com.getcapacitor.PluginMethod;
import com.getcapacitor.annotation.CapacitorPlugin;
import java.io.ByteArrayOutputStream;
import java.io.InputStream;
import java.net.URI;
import java.util.Arrays;
import java.util.HashMap;
import java.util.HashSet;
import java.util.Iterator;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.TimeUnit;
import okhttp3.Authenticator;
import okhttp3.Call;
import okhttp3.Callback;
import okhttp3.CookieJar;
import okhttp3.MediaType;
import okhttp3.OkHttpClient;
import okhttp3.Request;
import okhttp3.RequestBody;
import okhttp3.Response;
import java.io.IOException;
import java.io.OutputStream;
import java.util.UUID;

@CapacitorPlugin(name = "MyChefNative", permissions = {
    @com.getcapacitor.annotation.Permission(strings = { android.Manifest.permission.POST_NOTIFICATIONS }, alias = "reminders")
})
public class MyChefNativePlugin extends Plugin {
    private final Map<String, Call> calls = new ConcurrentHashMap<>();
    private final OkHttpClient http = new OkHttpClient.Builder()
        .cookieJar(CookieJar.NO_COOKIES).cache(null)
        .authenticator(Authenticator.NONE).proxyAuthenticator(Authenticator.NONE)
        .followRedirects(false).followSslRedirects(false).retryOnConnectionFailure(false)
        .connectTimeout(20, TimeUnit.SECONDS).readTimeout(60, TimeUnit.SECONDS).callTimeout(90, TimeUnit.SECONDS).build();
    private ProtectedRefresh refresh;
    private ProtectedShopping shopping;
    private URI origin;
    private ConnectivityManager connectivity;
    private ConnectivityManager.NetworkCallback networkCallback;
    private final java.util.concurrent.atomic.AtomicLong reminderPermissionGeneration = new java.util.concurrent.atomic.AtomicLong();
    private final Map<String, Long> reminderPermissionRequests = new ConcurrentHashMap<>();
    private void traceNativeCheck(String path, String method, String result) {
        if (origin == null || !"localhost".equals(origin.getHost())
                || (getContext().getApplicationInfo().flags & ApplicationInfo.FLAG_DEBUGGABLE) == 0) return;
        String operation;
        if ("/api/native/v1/session".equals(path) && ("GET".equals(method) || "POST".equals(method))) {
            operation = "session";
        } else if ("/api/native/v1/profile".equals(path) && "PUT".equals(method)) {
            operation = "profile";
        } else if (path.matches("/api/native/v1/favorite-weeks/[0-9]{4}-[0-9]{2}-[0-9]{2}") && "GET".equals(method)) {
            operation = "favorite-week";
        } else if (("/api/native/v1/menus".equals(path) || path.startsWith("/api/native/v1/menus?")) && "GET".equals(method)) {
            operation = "menus";
        } else if ("/api/native/v1/generations".equals(path) && "POST".equals(method)) {
            operation = "generations";
        } else return;
        Log.i("MyChefNativeCheck", operation + " " + method + " " + result);
    }
    void runtimeChanged() { notifyListeners("runtime", new JSObject()); }
    @PluginMethod public void readRuntime(PluginCall call) {
        try {
            MainActivity activity = (MainActivity)getActivity();
            JSObject result = new JSObject();
            result.put("active",activity.active);
            result.put("online",connectivity != null && connectivity.getActiveNetwork() != null);
            result.put("back",activity.runtime.takeBack());
            String link = activity.runtime.takeLink(SystemClock.elapsedRealtime());
            if (link != null) {
                try { result.put("link",NativeBoundary.accountLink(origin,link)); }
                catch (Exception ignored) { result.put("invalidLink",true); }
            }
            String reminder = ReminderNotifications.take(SystemClock.elapsedRealtime());
            if (reminder != null) result.put("reminder", reminder);
            reply(call,result);
        } catch (Exception ignored) { call.reject("App state is unavailable", "native_runtime"); }
    }
    @PluginMethod public void reminderPermission(PluginCall call) {
        MainActivity activity = (MainActivity)getActivity();
        long generation = reminderPermissionGeneration.incrementAndGet();
        if (!ReminderRegistration.available(activity) && !isReminderFixture()) {
            finishReminderPermission(call, false);
            return;
        }
        if (!ReminderNotifications.allowed(activity) && Boolean.TRUE.equals(call.getBoolean("prompt"))
                && android.os.Build.VERSION.SDK_INT >= 33 && getPermissionState("reminders") == com.getcapacitor.PermissionState.PROMPT) {
            reminderPermissionRequests.clear();
            reminderPermissionRequests.put(call.getCallbackId(), generation);
            requestPermissionForAlias("reminders", call, "reminderPermissionResult");
            return;
        }
        finishReminderPermission(call, ReminderNotifications.allowed(activity));
    }
    @com.getcapacitor.annotation.PermissionCallback
    private void reminderPermissionResult(PluginCall call) {
        Long generation = reminderPermissionRequests.remove(call.getCallbackId());
        finishReminderPermission(call, generation != null && generation == reminderPermissionGeneration.get()
                && ReminderNotifications.allowed(getContext()));
    }
    private boolean isReminderFixture() {
        return origin != null && "https://localhost:8977".equals(origin.toString())
                && (getContext().getApplicationInfo().flags & ApplicationInfo.FLAG_DEBUGGABLE) != 0;
    }
    private void finishReminderPermission(PluginCall call, boolean allowed) {
        if (allowed) {
            if (isReminderFixture()) {
                long generation = ReminderNotifications.begin(getContext());
                if (generation >= 0) ReminderNotifications.registered("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", generation);
            } else {
                ReminderRegistration.start((MainActivity)getActivity());
            }
        }
        JSObject result = new JSObject();
        result.put("allowed", allowed);
        reply(call, result);
    }
    @PluginMethod public void reminderRegistration(PluginCall call) {
        boolean allowed = ReminderNotifications.allowed(getContext());
        if (!allowed) ReminderRegistration.stop(getContext());
        JSObject result = new JSObject();
        result.put("allowed", allowed);
        result.put("token", allowed && ReminderNotifications.token() != null ? ReminderNotifications.token() : org.json.JSONObject.NULL);
        reply(call, result);
    }
    @PluginMethod public void stopReminderRegistration(PluginCall call) {
        reminderPermissionGeneration.incrementAndGet();
        reminderPermissionRequests.clear();
        ReminderRegistration.stop(getContext());
        reply(call);
    }
    @PluginMethod public void openReminderSettings(PluginCall call) {
        getActivity().startActivity(ReminderNotifications.settings(getContext()));
        reply(call);
    }

    private volatile PluginCall mediaPending;
    private volatile boolean mediaOutstanding;
    private volatile long mediaEpoch;
    private byte[] exportBytes;
    private final Set<String> responseHeaders = new HashSet<>(Arrays.asList(
        "content-type", "content-disposition", "date", "etag", "x-mychef-user", "retry-after"));

    private void reply(PluginCall call) { reply(call, new JSObject()); }
    private void reply(PluginCall call, JSObject value) {
        value.put("_callNonce", call.getString("_callNonce", "")); call.resolve(value);
    }
    @Override public void load() {
        refresh = new ProtectedRefresh(getContext());
        shopping = new ProtectedShopping(getContext());
        connectivity = (ConnectivityManager)getContext().getSystemService(Context.CONNECTIVITY_SERVICE);
        networkCallback = new ConnectivityManager.NetworkCallback() {
            @Override public void onAvailable(Network network) { runtimeChanged(); }
            @Override public void onLost(Network network) { runtimeChanged(); }
        };
        if (connectivity != null) connectivity.registerDefaultNetworkCallback(networkCallback);
        try { origin = NativeBoundary.origin(getConfig().getString("apiOrigin", "")); }
        catch (Exception ignored) { origin = null; }
    }
    @PluginMethod public void configuration(PluginCall call) {
        if (origin == null) { call.reject("Private API configuration is unavailable", "native_configuration"); return; }
        JSObject value = new JSObject(); value.put("platform", "android"); value.put("apiOrigin", origin.toString()); reply(call, value);
    }
    @PluginMethod public void readRefresh(PluginCall call) {
        try { String value = refresh.read(); JSObject result = new JSObject(); result.put("value", value == null ? org.json.JSONObject.NULL : value); reply(call, result); }
        catch (Exception ignored) { call.reject("Protected device storage is unavailable", "native_storage"); }
    }
    @PluginMethod public void writeRefresh(PluginCall call) {
        try {
            if (!call.getData().has("value")) throw new IllegalArgumentException();
            if (!call.getData().isNull("value") && !(call.getData().get("value") instanceof String)) throw new IllegalArgumentException();
            refresh.write(call.getString("value")); reply(call);
        } catch (Exception ignored) { call.reject("Protected device storage is unavailable", "native_storage"); }
    }
    @PluginMethod public void readShopping(PluginCall call) {
        try { reply(call, shopping.read()); }
        catch (Exception ignored) { call.reject("Protected shopping storage is unavailable", "native_storage"); }
    }
    @PluginMethod public void writeShopping(PluginCall call) {
        try {
            if (!call.getData().has("value")) throw new IllegalArgumentException();
            JSObject value = call.getObject("value");
            if (value == null && !call.getData().isNull("value")) throw new IllegalArgumentException();
            shopping.write(call.getString("generation", ""), value); reply(call);
        } catch (Exception ignored) { call.reject("Protected shopping storage is unavailable", "native_storage"); }
    }
    @PluginMethod public void clearShopping(PluginCall call) {
        try { reply(call, shopping.clear()); }
        catch (Exception ignored) { call.reject("Protected shopping storage is unavailable", "native_storage"); }
    }
    @PluginMethod public void request(PluginCall call) {
        String requestId = call.getString("requestId", "");
        String path = call.getString("path", ""), method = call.getString("method", "");
        traceNativeCheck(path, method, "bridge");
        try {
            if (origin == null || !requestId.matches("[a-f0-9-]{36}") || calls.size() >= 32) throw new IllegalArgumentException();
            URI target = NativeBoundary.target(origin, path);
            if (!Arrays.asList("GET", "HEAD", "POST", "PUT", "PATCH", "DELETE").contains(method)) throw new IllegalArgumentException();
            JSObject input = call.getObject("headers", new JSObject());
            Map<String, String> raw = new HashMap<>();
            for (Iterator<String> keys = input.keys(); keys.hasNext();) { String key = keys.next(); raw.put(key, input.getString(key)); }
            Map<String, String> headers = NativeBoundary.headers(raw);
            String encoded = call.getString("body", "");
            if (encoded.length() > NativeBoundary.MAX_REQUEST * 4 / 3 + 4) throw new IllegalArgumentException();
            byte[] bytes = Base64.decode(encoded, Base64.NO_WRAP);
            if (bytes.length > NativeBoundary.MAX_REQUEST) throw new IllegalArgumentException();
            boolean noBody = method.equals("GET") || method.equals("HEAD");
            if (noBody && bytes.length > 0) throw new IllegalArgumentException();
            Request.Builder builder = new Request.Builder().url(target.toString()).header("Cache-Control", "no-store");
            for (Map.Entry<String, String> header : headers.entrySet()) builder.header(header.getKey(), header.getValue());
            String contentType = headers.get("content-type");
            builder.method(method, noBody ? null : RequestBody.create(bytes, contentType == null ? null : MediaType.get(contentType)));
            enqueue(call, builder.build(), requestId, false);
        } catch (Exception ignored) {
            traceNativeCheck(path, method, "rejected");
            call.reject("The private API request failed", "native_request");
        }
    }
    private void enqueue(PluginCall call, Request request, String requestId, boolean image) {
        String path = request.url().encodedPath(), method = request.method();
        try {
            if (calls.size() >= 32) throw new IllegalArgumentException();
            final int limit = image ? 8 * 1024 * 1024 : NativeBoundary.MAX_RESPONSE;
            Call operation = http.newCall(request);
            if (calls.putIfAbsent(requestId, operation) != null) throw new IllegalArgumentException();
            traceNativeCheck(path, method, "queued");
            operation.enqueue(new Callback() {
                @Override public void onFailure(Call operation, IOException error) {
                    calls.remove(requestId, operation);
                    traceNativeCheck(path, method,
                        error instanceof javax.net.ssl.SSLException ? "io-tls" :
                        error instanceof java.net.ConnectException ? "io-connect" :
                        error instanceof java.net.SocketTimeoutException ? "io-timeout" :
                        error instanceof java.io.EOFException ? "io-eof" :
                        error instanceof java.net.ProtocolException ? "io-protocol" :
                        error instanceof java.net.SocketException ? "io-socket" : "io-other");
                    call.reject("The private API request failed", "native_request");
                }
                @Override public void onResponse(Call operation, Response response) {
                    traceNativeCheck(path, method, "response");
                    try (Response owned = response) {
                        if (response.code() >= 300 && response.code() < 400) throw new IOException();
                        if (response.body() == null || response.body().contentLength() > limit || (image && !response.isSuccessful())) throw new IOException();
                        ByteArrayOutputStream body = new ByteArrayOutputStream();
                        try (InputStream stream = response.body().byteStream()) {
                            byte[] buffer = new byte[8192]; int count;
                            while ((count = stream.read(buffer)) != -1) {
                                if (body.size() + count > limit) throw new IOException();
                                body.write(buffer, 0, count);
                            }
                        }
                        JSObject result = new JSObject(), safe = new JSObject();
                        if (image) {
                            String mime = response.header("content-type", "").split(";", 2)[0].trim();
                            if (!Arrays.asList("image/jpeg", "image/png", "image/webp").contains(mime)) throw new IOException();
                            result.put("mimeType", mime); result.put("data", Base64.encodeToString(body.toByteArray(), Base64.NO_WRAP));
                            reply(call, result); return;
                        }
                        for (String header : responseHeaders) if (response.header(header) != null) safe.put(header, response.header(header));
                        result.put("status", response.code()); result.put("headers", safe);
                        result.put("body", Base64.encodeToString(body.toByteArray(), Base64.NO_WRAP));
                        reply(call, result);
                    } catch (Exception ignored) {
                        traceNativeCheck(path, method, "response-rejected");
                        call.reject("The private API request failed", "native_request");
                    }
                    finally { calls.remove(requestId, operation); }
                }
            });
        } catch (Exception ignored) {
            traceNativeCheck(path, method, "enqueue-failure");
            call.reject("The private API request failed", "native_request");
        }
    }
    @PluginMethod public void publicImage(PluginCall call) {
        try {
            URI uri = NativeBoundary.publicPhoto(call.getString("url", ""));
            Request request = new Request.Builder().url(uri.toString()).tag("media")
                .header("Accept", "image/jpeg,image/png,image/webp").header("Cache-Control", "no-store").get().build();
            enqueue(call, request, UUID.randomUUID().toString(), true);
        } catch (Exception ignored) { call.reject("The photo is unavailable", "native_media"); }
    }
    @PluginMethod public void openExternal(PluginCall call) {
        try {
            URI uri = NativeBoundary.externalLink(call.getString("url", ""));
            getBridge().executeOnMainThread(() -> {
                try {
                    Intent intent = new Intent("mailto".equals(uri.getScheme()) ? Intent.ACTION_SENDTO : Intent.ACTION_VIEW, Uri.parse(uri.toString()));
                    intent.addCategory(Intent.CATEGORY_BROWSABLE); getActivity().startActivity(intent); reply(call);
                } catch (Exception ignored) { call.reject("This link could not be opened", "native_media"); }
            });
        } catch (Exception ignored) { call.reject("This link could not be opened", "native_media"); }
    }
    @PluginMethod public void pickPhotos(PluginCall call) {
        try {
            String purpose = call.getString("purpose", "");
            if (mediaOutstanding || !Arrays.asList("dish", "recipe-pages").contains(purpose)) throw new IllegalArgumentException();
            boolean pages = purpose.equals("recipe-pages");
            Intent intent = new Intent(Intent.ACTION_OPEN_DOCUMENT).addCategory(Intent.CATEGORY_OPENABLE).setType("image/*");
            intent.putExtra(Intent.EXTRA_MIME_TYPES, pages ? new String[] {"image/jpeg", "image/png", "image/heic", "image/heif"} : new String[] {"image/jpeg", "image/png", "image/webp"});
            intent.putExtra(Intent.EXTRA_ALLOW_MULTIPLE, pages); intent.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION);
            launchMedia(call, intent, "photoPicked");
        } catch (Exception ignored) { call.reject("The photo picker could not be opened", "native_media"); }
    }
    @PluginMethod public void saveExport(PluginCall call) {
        boolean prepared = false;
        try {
            if (mediaOutstanding) throw new IllegalArgumentException();
            String filename = NativeBoundary.exportName(call.getString("filename", ""));
            String encoded = call.getString("data", "");
            if (encoded.length() > (NativeBoundary.MAX_RESPONSE + 2) / 3 * 4) throw new IllegalArgumentException();
            byte[] bytes = Base64.decode(encoded, Base64.NO_WRAP); NativeBoundary.zip(bytes);
            // Capacitor's default activity-state serializer saves call options.
            // Export contents remain only in this bounded in-memory buffer.
            call.getData().remove("data"); exportBytes = bytes; prepared = true;
            Intent intent = new Intent(Intent.ACTION_CREATE_DOCUMENT).addCategory(Intent.CATEGORY_OPENABLE).setType("application/zip");
            intent.putExtra(Intent.EXTRA_TITLE, filename); launchMedia(call, intent, "exportSaved");
        } catch (Exception ignored) { if (prepared) clearExport(); call.reject("The download could not be saved", "native_media"); }
    }
    private void launchMedia(PluginCall call, Intent intent, String callback) {
        mediaPending = call; mediaOutstanding = true; long generation = mediaEpoch;
        getBridge().executeOnMainThread(() -> {
            if (generation != mediaEpoch || mediaPending != call) { mediaOutstanding = false; return; }
            try { startActivityForResult(call, intent, callback); }
            catch (Exception ignored) {
                getBridge().execute(() -> { mediaPending = null; mediaOutstanding = false; clearExport(); call.reject("The file picker could not be opened", "native_media"); });
            }
        });
    }
    @ActivityCallback private void photoPicked(PluginCall call, ActivityResult result) {
        getBridge().execute(() -> {
            try {
                if (call == null || mediaPending != call) return;
                JSArray files = result.getResultCode() == Activity.RESULT_OK && result.getData() != null
                    ? NativeMediaFiles.readPhotos(getContext(), result.getData(), call.getString("purpose", "")) : new JSArray();
                if (mediaPending == call) { JSObject response = new JSObject(); response.put("files", files); reply(call, response); }
            } catch (Exception ignored) { if (call != null) call.reject("The selected photo could not be opened", "native_media"); }
            finally { mediaPending = null; mediaOutstanding = false; }
        });
    }
    @ActivityCallback private void exportSaved(PluginCall call, ActivityResult result) {
        getBridge().execute(() -> {
            Uri target = result.getData() == null ? null : result.getData().getData();
            boolean saved = false;
            try {
                if (call == null || mediaPending != call || exportBytes == null) {
                    if (result.getResultCode() == Activity.RESULT_OK) NativeMediaFiles.discardIncompleteExport(getContext(), target);
                    return;
                }
                if (result.getResultCode() == Activity.RESULT_OK && target != null) {
                    if (!"content".equals(target.getScheme())) throw new IOException();
                    try (OutputStream output = getContext().getContentResolver().openOutputStream(target, "w")) {
                        if (output == null) throw new IOException(); output.write(exportBytes); output.flush();
                    }
                    saved = true;
                }
                JSObject response = new JSObject(); response.put("saved", saved); reply(call, response);
            } catch (Exception ignored) {
                NativeMediaFiles.discardIncompleteExport(getContext(), target);
                if (call != null) call.reject("The download could not be saved", "native_media");
            } finally { clearExport(); mediaPending = null; mediaOutstanding = false; }
        });
    }
    private void clearExport() { if (exportBytes != null) Arrays.fill(exportBytes, (byte) 0); exportBytes = null; }
    @PluginMethod public void cancelMedia(PluginCall call) {
        mediaEpoch++; PluginCall pending = mediaPending; mediaPending = null; clearExport();
        if (pending != null) pending.reject("The account changed", "native_cancelled");
        for (Call operation : calls.values()) if ("media".equals(operation.request().tag()) || operation.request().url().encodedPath().startsWith("/api/native/v1/personal-photos/")) operation.cancel();
        reply(call);
    }
    // Private files, source URIs and export bytes are never activity-restorable.
    @Override protected Bundle saveInstanceState() { return null; }
    @PluginMethod public void cancelRequest(PluginCall call) {
        Call operation = calls.get(call.getString("requestId", "")); if (operation != null) operation.cancel(); reply(call);
    }
    @Override protected void handleOnDestroy() {
        if (connectivity != null && networkCallback != null) connectivity.unregisterNetworkCallback(networkCallback);
        mediaEpoch++; clearExport(); mediaPending = null;
        for (Call call : calls.values()) call.cancel(); calls.clear();
        http.dispatcher().executorService().shutdown(); http.connectionPool().evictAll();
        super.handleOnDestroy();
    }
}
