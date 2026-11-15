"""Reject silent or ambiguous speech evidence without starting an emulator."""
import array
from pathlib import Path
import tempfile
import unittest
import wave

from analytics_speech import audible, matches


class SpeechEvidenceTest(unittest.TestCase):
    def test_control_state_cannot_be_inferred_from_a_hint_or_opposite_state(self):
        self.assertTrue(matches('on', 'Help improve MyChef, checked, checkbox'))
        self.assertTrue(matches('off', 'Help improve MyChef, not checked, checkbox'))
        self.assertFalse(matches('on', 'Help improve MyChef, not checked, checkbox'))
        self.assertFalse(matches('off', 'Help improve MyChef, checked, double tap to turn off'))
        self.assertFalse(matches('on', 'checked'))
        self.assertFalse(matches('invented', 'Help improve MyChef checked'))
        self.assertTrue(matches('stale', 'Your setting changed elsewhere. Review the current choice.'))
        self.assertTrue(matches('offline', 'Turning off was not confirmed. Analytics may still be On.'))
        self.assertFalse(matches('offline', 'Analytics On'))

    def test_silence_wrong_format_and_unbounded_audio_are_not_evidence(self):
        with tempfile.TemporaryDirectory(prefix='analytics-speech-') as directory:
            path = Path(directory) / 'speaker.wav'
            for rate, samples, expected in ((16000, [0] * 16000, False),
                                             (16000, [1000, -1000] * 8000, True),
                                             (8000, [1000, -1000] * 8000, False),
                                             (16000, [1000] * 16, False),
                                             (16000, [1000] * 16000 * 46, False)):
                with wave.open(str(path), 'wb') as sound:
                    sound.setnchannels(1)
                    sound.setsampwidth(2)
                    sound.setframerate(rate)
                    sound.writeframes(array.array('h', samples).tobytes())
                self.assertEqual(audible(path), expected)


if __name__ == '__main__':
    unittest.main()
