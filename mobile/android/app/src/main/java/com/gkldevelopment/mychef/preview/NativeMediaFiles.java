package com.gkldevelopment.mychef.preview;

import android.content.Context;
import android.content.Intent;
import android.graphics.BitmapFactory;
import android.net.Uri;
import android.provider.DocumentsContract;
import android.util.Base64;
import com.getcapacitor.JSArray;
import com.getcapacitor.JSObject;
import java.io.ByteArrayOutputStream;
import java.io.InputStream;
import java.io.IOException;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;

final class NativeMediaFiles {
    static JSArray readPhotos(Context context, Intent result, String purpose) throws Exception {
        boolean pages = "recipe-pages".equals(purpose);
        List<Uri> selected = new ArrayList<>();
        if (result.getClipData() != null) {
            int count = result.getClipData().getItemCount();
            if (count > (pages ? 3 : 1)) throw new IOException("Too many photos");
            for (int i = 0; i < count; i++) selected.add(result.getClipData().getItemAt(i).getUri());
        } else if (result.getData() != null) selected.add(result.getData());
        JSArray files = new JSArray(); int total = 0;
        for (Uri uri : selected) {
            if (!"content".equals(uri.getScheme())) throw new IOException("Expected selected document");
            String mime = context.getContentResolver().getType(uri);
            if (pages && "image/heif".equals(mime)) mime = "image/heic";
            List<String> allowed = pages ? Arrays.asList("image/jpeg", "image/png", "image/heic") : Arrays.asList("image/jpeg", "image/png", "image/webp");
            if (!allowed.contains(mime)) throw new IOException("Unsupported photo");
            byte[] bytes;
            try (InputStream stream = context.getContentResolver().openInputStream(uri)) {
                if (stream == null) throw new IOException("Photo unavailable");
                ByteArrayOutputStream data = new ByteArrayOutputStream(); byte[] buffer = new byte[8192]; int count;
                while ((count = stream.read(buffer)) != -1) {
                    if (data.size() + count > (pages ? 10 : 12) * 1024 * 1024 || total + data.size() + count > 12 * 1024 * 1024) throw new IOException("Photo too large");
                    data.write(buffer, 0, count);
                }
                bytes = data.toByteArray();
            }
            try {
                if (bytes.length == 0) throw new IOException("Empty photo");
                // HEIC source pages are decoded by the existing bounded server
                // normalizer; old Android versions need not decode them here.
                if (!"image/heic".equals(mime)) {
                    BitmapFactory.Options options = new BitmapFactory.Options(); options.inJustDecodeBounds = true;
                    BitmapFactory.decodeByteArray(bytes, 0, bytes.length, options);
                    if (options.outWidth < 1 || options.outHeight < 1 || options.outWidth > 8192 || options.outHeight > 8192 || (long) options.outWidth * options.outHeight > 20_000_000) throw new IOException("Invalid photo dimensions");
                }
                JSObject file = new JSObject(); file.put("mimeType", mime); file.put("data", Base64.encodeToString(bytes, Base64.NO_WRAP)); files.put(file); total += bytes.length;
            } finally { Arrays.fill(bytes, (byte) 0); }
        }
        return files;
    }
    static void discardIncompleteExport(Context context, Uri uri) {
        // Called only for the fresh ACTION_CREATE_DOCUMENT result. Never call
        // this for a source-photo URI or an existing user-owned document.
        try { if (uri != null && "content".equals(uri.getScheme())) DocumentsContract.deleteDocument(context.getContentResolver(), uri); }
        catch (Exception ignored) { /* The provider may remove cancelled files itself. */ }
    }
}
