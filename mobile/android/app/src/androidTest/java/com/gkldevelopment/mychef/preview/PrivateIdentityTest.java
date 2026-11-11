package com.gkldevelopment.mychef.preview;

import static org.junit.Assert.assertEquals;

import androidx.test.ext.junit.runners.AndroidJUnit4;
import androidx.test.platform.app.InstrumentationRegistry;
import org.junit.Test;
import org.junit.runner.RunWith;

@RunWith(AndroidJUnit4.class)
public class PrivateIdentityTest {
    @Test
    public void installedCandidateUsesPrivateIdentity() {
        assertEquals("com.gkldevelopment.mychef.preview",
            InstrumentationRegistry.getInstrumentation().getTargetContext().getPackageName());
    }
}
