package com.gkldevelopment.mychef.preview;

import android.content.Context;
import android.security.keystore.KeyGenParameterSpec;
import android.security.keystore.KeyProperties;
import android.util.AtomicFile;
import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.security.KeyStore;
import javax.crypto.KeyGenerator;
import javax.crypto.SecretKey;

final class ProtectedRefresh {
    private static final String ALIAS = "mychef.refresh.v1";
    private final AtomicFile file;
    ProtectedRefresh(Context context) {
        file = new AtomicFile(new File(context.getNoBackupFilesDir(), "native-refresh.v1"));
    }
    private SecretKey key(boolean create) throws Exception {
        KeyStore store = KeyStore.getInstance("AndroidKeyStore");
        store.load(null);
        if (!store.containsAlias(ALIAS)) {
            if (!create) throw new IOException("Missing protected credential key");
            KeyGenerator generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore");
            generator.init(new KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT | KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setRandomizedEncryptionRequired(true).setKeySize(256).build());
            generator.generateKey();
        }
        return (SecretKey) store.getKey(ALIAS, null);
    }
    synchronized String read() throws Exception {
        File base = file.getBaseFile();
        if (!base.exists() && !new File(base.getPath() + ".bak").exists()) return null;
        if (base.length() > 256 || new File(base.getPath() + ".bak").length() > 256) throw new IOException("Invalid protected credential");
        byte[] bytes = file.readFully();
        return RefreshVault.decrypt(key(false), bytes);
    }
    synchronized void write(String value) throws Exception {
        if (value == null) {
            // Destroy the key even if removing an inaccessible file later fails.
            KeyStore store = KeyStore.getInstance("AndroidKeyStore");
            store.load(null); store.deleteEntry(ALIAS);
            file.delete();
            File base = file.getBaseFile();
            if (base.exists() || new File(base.getPath() + ".bak").exists() || new File(base.getPath() + ".new").exists())
                throw new IOException("Protected credential erase failed");
            return;
        }
        byte[] encrypted = RefreshVault.encrypt(key(true), value);
        FileOutputStream output = null;
        try {
            output = file.startWrite();
            output.write(encrypted);
            file.finishWrite(output);
        } catch (Exception error) {
            if (output != null) file.failWrite(output);
            throw error;
        }
    }
}
