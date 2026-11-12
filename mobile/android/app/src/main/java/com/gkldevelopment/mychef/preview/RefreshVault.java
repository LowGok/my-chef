package com.gkldevelopment.mychef.preview;

import java.nio.charset.StandardCharsets;
import java.security.GeneralSecurityException;
import java.util.Arrays;
import javax.crypto.Cipher;
import javax.crypto.SecretKey;
import javax.crypto.spec.GCMParameterSpec;

public final class RefreshVault {
    private static final byte[] AAD = "mychef:refresh:v1".getBytes(StandardCharsets.UTF_8);
    public static byte[] encrypt(SecretKey key, String refresh) throws GeneralSecurityException {
        if (refresh == null || !refresh.matches("[a-f0-9]{64}")) throw new GeneralSecurityException("Invalid credential");
        Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding");
        cipher.init(Cipher.ENCRYPT_MODE, key);
        cipher.updateAAD(AAD);
        byte[] encrypted = cipher.doFinal(refresh.getBytes(StandardCharsets.UTF_8));
        byte[] output = new byte[12 + encrypted.length];
        if (cipher.getIV().length != 12) throw new GeneralSecurityException("Invalid IV");
        System.arraycopy(cipher.getIV(), 0, output, 0, 12);
        System.arraycopy(encrypted, 0, output, 12, encrypted.length);
        return output;
    }
    public static String decrypt(SecretKey key, byte[] value) throws GeneralSecurityException {
        if (value.length != 92) throw new GeneralSecurityException("Invalid protected credential");
        Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding");
        cipher.init(Cipher.DECRYPT_MODE, key, new GCMParameterSpec(128, Arrays.copyOf(value, 12)));
        cipher.updateAAD(AAD);
        String refresh = new String(cipher.doFinal(Arrays.copyOfRange(value, 12, value.length)), StandardCharsets.UTF_8);
        if (!refresh.matches("[a-f0-9]{64}")) throw new GeneralSecurityException("Invalid protected credential");
        return refresh;
    }
}
