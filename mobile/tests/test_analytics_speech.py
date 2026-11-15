"""Reject silent or ambiguous speech evidence without starting an emulator."""
import array
from pathlib import Path
import tempfile
import unittest
import wave

from analytics_speech import Speech, audible, matches


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

    def test_state_before_label_requires_adjacent_control_state_and_no_conflict(self):
        self.assertTrue(matches('on', 'Checked. Help improve MyChef. Checkbox.'))
        self.assertTrue(matches('off', 'Not checked. Help improve MyChef. Checkbox.'))
        self.assertTrue(matches('on', 'Checked checkbox, Help improve MyChef.'))
        self.assertFalse(matches('on', 'TalkBack on. Help improve MyChef.'))
        self.assertFalse(matches('on', 'Checked. Other control. Help improve MyChef.'))
        self.assertFalse(matches('on', 'Help improve MyChef. Checkbox. Double tap to turn on.'))
        self.assertFalse(matches('on', 'Checked. Help improve MyChef. Not checked.'))
        self.assertFalse(matches('off', 'Checked. Help improve MyChef. Not checked.'))

    def test_signup_states_and_cleanup_announcements_require_the_actual_words(self):
        self.assertTrue(matches('signup-on', 'Help improve MyChef, checked, checkbox'))
        self.assertTrue(matches('signup-off', 'Help improve MyChef, unchecked, checkbox'))
        self.assertFalse(matches('signup-off', 'Help improve MyChef checked'))
        self.assertTrue(matches('cleanup', 'Off. Active deletion is in progress.'))
        self.assertFalse(matches('cleanup', 'Off.'))
        self.assertTrue(matches('backup', 'Off. Active data is erased; isolated backup cleanup is pending for up to seven days.'))
        self.assertFalse(matches('backup', 'Active deletion is in progress'))

    def test_capture_retries_are_bounded_and_cannot_mark_missing_speech_passed(self):
        speech = Speech.__new__(Speech)
        speech.active, speech.passed, speech.attempts = None, set(), {'on': 3}
        with self.assertRaisesRegex(ValueError, 'attempt limit'):
            speech.operate('speech-start', 'on')
        self.assertEqual(speech.passed, set())
        self.assertIsNone(speech.active)
        self.assertEqual(speech.attempts, {'on': 3})

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
