package com.gkldevelopment.mychef.preview;

import java.nio.charset.StandardCharsets;
import java.security.GeneralSecurityException;
import java.util.Arrays;
import javax.crypto.Cipher;
import javax.crypto.SecretKey;
import javax.crypto.spec.GCMParameterSpec;

public final class ShoppingCipher {
    private static byte[] aad(String owner, String instance) throws GeneralSecurityException {
        if (owner == null || instance == null || !owner.matches("[0-9]{1,20}") || !instance.matches("[a-f0-9]{32}"))
            throw new GeneralSecurityException("Invalid shopping owner");
        return ("mychef:shopping:v1:" + owner + ":" + instance).getBytes(StandardCharsets.UTF_8);
    }
    public static byte[] encrypt(SecretKey key, String owner, String instance, String payload) throws GeneralSecurityException {
        byte[] plain = payload.getBytes(StandardCharsets.UTF_8);
        if (plain.length > 8 * 1024 * 1024) throw new GeneralSecurityException("Shopping snapshot too large");
        Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding"); cipher.init(Cipher.ENCRYPT_MODE, key); cipher.updateAAD(aad(owner, instance));
        byte[] encrypted = cipher.doFinal(plain), iv = cipher.getIV();
        if (iv.length != 12) throw new GeneralSecurityException("Invalid IV");
        byte[] result = new byte[12 + encrypted.length];
        System.arraycopy(iv, 0, result, 0, 12); System.arraycopy(encrypted, 0, result, 12, encrypted.length); return result;
    }
    public static String decrypt(SecretKey key, String owner, String instance, byte[] input) throws GeneralSecurityException {
        if (input.length < 28 || input.length > 8 * 1024 * 1024 + 28) throw new GeneralSecurityException("Invalid shopping snapshot");
        Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding");
        cipher.init(Cipher.DECRYPT_MODE, key, new GCMParameterSpec(128, Arrays.copyOf(input, 12))); cipher.updateAAD(aad(owner, instance));
        return new String(cipher.doFinal(Arrays.copyOfRange(input, 12, input.length)), StandardCharsets.UTF_8);
    }
}
