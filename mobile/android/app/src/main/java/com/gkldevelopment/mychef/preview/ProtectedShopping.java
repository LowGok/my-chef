package com.gkldevelopment.mychef.preview;

import android.content.Context;
import android.security.keystore.KeyGenParameterSpec;
import android.security.keystore.KeyProperties;
import android.util.AtomicFile;
import com.getcapacitor.JSObject;
import java.io.ByteArrayInputStream;
import java.io.ByteArrayOutputStream;
import java.io.DataInputStream;
import java.io.DataOutputStream;
import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.security.KeyStore;
import java.util.UUID;
import javax.crypto.KeyGenerator;
import javax.crypto.SecretKey;

final class ProtectedShopping {
    private static final String ALIAS = "mychef.shopping.v1";
    private final AtomicFile file;
    private final AtomicFile erased;
    private String generation = UUID.randomUUID().toString();
    ProtectedShopping(Context context) {
        file = new AtomicFile(new File(context.getNoBackupFilesDir(), "native-shopping.v1"));
        erased = new AtomicFile(new File(context.getNoBackupFilesDir(), "native-shopping-erased.v1"));
    }
    private SecretKey key(boolean create) throws Exception {
        KeyStore store = KeyStore.getInstance("AndroidKeyStore"); store.load(null);
        if (!store.containsAlias(ALIAS)) {
            if (!create) throw new IOException("Shopping key unavailable");
            KeyGenerator generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore");
            generator.init(new KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT | KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setRandomizedEncryptionRequired(true).setKeySize(256).build()); generator.generateKey();
        }
        return (SecretKey) store.getKey(ALIAS, null);
    }
    private void atomic(AtomicFile target, byte[] bytes) throws IOException {
        FileOutputStream output = null;
        try { output = target.startWrite(); output.write(bytes); target.finishWrite(output); }
        catch (IOException error) { if (output != null) target.failWrite(output); throw error; }
    }
    private boolean exists(AtomicFile value) {
        return value.getBaseFile().exists() || new File(value.getBaseFile().getPath() + ".bak").exists();
    }
    synchronized JSObject read() throws Exception {
        JSObject result = new JSObject(); result.put("generation", generation); result.put("value", org.json.JSONObject.NULL);
        if (exists(erased) || !exists(file)) return result;
        if (file.getBaseFile().length() > 8 * 1024 * 1024 + 1024 || new File(file.getBaseFile().getPath() + ".bak").length() > 8 * 1024 * 1024 + 1024) throw new IOException("Shopping snapshot too large");
        try (DataInputStream input = new DataInputStream(new ByteArrayInputStream(file.readFully()))) {
            if (input.readInt() != 1) throw new IOException("Unsupported shopping snapshot");
            String owner = input.readUTF(), instance = input.readUTF(); int length = input.readInt();
            if (length < 28 || length > 8 * 1024 * 1024 + 28 || input.available() != length) throw new IOException("Invalid shopping snapshot");
            byte[] ciphertext = new byte[length]; input.readFully(ciphertext);
            JSObject value = new JSObject(); value.put("version", 1); value.put("accountId", owner); value.put("accountInstance", instance);
            value.put("payload", ShoppingCipher.decrypt(key(false), owner, instance, ciphertext)); result.put("value", value); return result;
        }
    }
    synchronized void write(String expected, JSObject value) throws Exception {
        if (!generation.equals(expected)) throw new IOException("Stale shopping write");
        if (value == null) { eraseContent(); return; }
        if (value.getInteger("version", 0) != 1) throw new IOException("Unsupported shopping snapshot");
        String owner = value.getString("accountId"), instance = value.getString("accountInstance"), payload = value.getString("payload");
        if (payload == null) throw new IOException("Invalid shopping snapshot");
        byte[] cipher = ShoppingCipher.encrypt(key(true), owner, instance, payload);
        ByteArrayOutputStream bytes = new ByteArrayOutputStream();
        try (DataOutputStream output = new DataOutputStream(bytes)) { output.writeInt(1); output.writeUTF(owner); output.writeUTF(instance); output.writeInt(cipher.length); output.write(cipher); }
        atomic(file, bytes.toByteArray()); erased.delete();
        if (exists(erased)) throw new IOException("Shopping erasure could not be replaced");
    }
    synchronized JSObject clear() throws Exception {
        generation = UUID.randomUUID().toString(); eraseContent();
        JSObject result = new JSObject(); result.put("generation", generation); return result;
    }
    private void eraseContent() throws Exception {
        // A durable marker hides prior content even if key/file cleanup fails.
        atomic(erased, new byte[] {1});
        KeyStore store = KeyStore.getInstance("AndroidKeyStore"); store.load(null); store.deleteEntry(ALIAS); file.delete();
    }
}
