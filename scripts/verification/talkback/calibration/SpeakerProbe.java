package org.mychef.speakprobe;

import android.app.Activity;
import android.app.Instrumentation;
import android.media.AudioAttributes;
import android.media.AudioFormat;
import android.media.AudioTrack;
import android.os.Bundle;
import android.os.SystemClock;
import android.speech.tts.TextToSpeech;
import android.speech.tts.UtteranceProgressListener;
import java.util.Locale;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;

public final class SpeakerProbe extends Instrumentation {
    private String mode;
    @Override public void onCreate(Bundle arguments) {
        super.onCreate(arguments);
        mode = arguments.getString("mode", "");
        start();
    }
    private AudioAttributes attributes(boolean accessibility) {
        return new AudioAttributes.Builder()
            .setUsage(accessibility ? AudioAttributes.USAGE_ASSISTANCE_ACCESSIBILITY : AudioAttributes.USAGE_MEDIA)
            .setContentType(accessibility ? AudioAttributes.CONTENT_TYPE_SPEECH : AudioAttributes.CONTENT_TYPE_MUSIC)
            .build();
    }

    private String tone(boolean accessibility) {
        short[] pcm = new short[48_000];
        int frequency = accessibility ? 880 : 440;
        for (int i = 0; i < pcm.length; ++i) {
            pcm[i] = (short) (12000 * Math.sin(2 * Math.PI * frequency * i / 48_000));
        }
        AudioTrack track = new AudioTrack.Builder()
            .setAudioAttributes(attributes(accessibility))
            .setAudioFormat(new AudioFormat.Builder().setEncoding(AudioFormat.ENCODING_PCM_16BIT)
                .setSampleRate(48_000).setChannelMask(AudioFormat.CHANNEL_OUT_MONO).build())
            .setTransferMode(AudioTrack.MODE_STATIC).setBufferSizeInBytes(pcm.length * 2).build();
        try {
            if (track.write(pcm, 0, pcm.length) != pcm.length) return "tone-write-failed";
            track.play();
            SystemClock.sleep(1400);
            return track.getPlaybackHeadPosition() >= 44_000 ? "tone-complete" : "tone-incomplete";
        } finally {
            track.release();
        }
    }

    private String speech() throws InterruptedException {
        var ready = new CountDownLatch(1);
        int[] initialization = { TextToSpeech.ERROR };
        var tts = new TextToSpeech(getTargetContext(), value -> {
            initialization[0] = value;
            ready.countDown();
        }, "com.google.android.tts");
        try {
            if (!ready.await(12, TimeUnit.SECONDS) || initialization[0] != TextToSpeech.SUCCESS) return "tts-not-ready";
            int language = tts.setLanguage(Locale.US);
            if (language == TextToSpeech.LANG_MISSING_DATA) return "tts-language-missing";
            if (language < 0) return "tts-language-unsupported";
            if (tts.getVoice() == null) return "tts-voice-missing";
            if (tts.getVoice().isNetworkConnectionRequired()) return "tts-voice-needs-network";
            if (tts.setAudioAttributes(attributes(true)) != TextToSpeech.SUCCESS) return "tts-output-unavailable";
            var done = new CountDownLatch(1);
            String[] result = { "tts-timed-out" };
            tts.setOnUtteranceProgressListener(new UtteranceProgressListener() {
                @Override public void onStart(String id) {}
                @Override public void onDone(String id) { result[0] = "tts-complete"; done.countDown(); }
                @Override public void onError(String id) { result[0] = "tts-error"; done.countDown(); }
            });
            if (tts.speak("This is an Android audio calibration.", TextToSpeech.QUEUE_FLUSH, null, "probe")
                    != TextToSpeech.SUCCESS) return "tts-refused";
            if (!done.await(12, TimeUnit.SECONDS)) return "tts-timed-out";
            SystemClock.sleep(500);
            return result[0];
        } finally {
            tts.shutdown();
        }
    }

    @Override public void onStart() {
        var result = new Bundle();
        try {
            result.putString("probeStatus", switch (mode) {
                case "media" -> tone(false);
                case "accessibility" -> tone(true);
                case "speech" -> speech();
                default -> "invalid-mode";
            });
        } catch (Exception unavailable) {
            result.putString("probeStatus", "probe-error");
        }
        finish(Activity.RESULT_OK, result);
    }
}
