"""Reject silent or ambiguous speech evidence without starting an emulator."""
import array
import json
import os
import subprocess
from contextlib import ExitStack
from types import SimpleNamespace
from unittest.mock import patch
from pathlib import Path
import tempfile
import unittest
import wave

import analytics_speech
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

    def test_bare_state_prefix_requires_the_same_control_role_and_no_prior_ui_words(self):
        self.assertTrue(matches('signup-off', 'Off. Help improve MyChef. Checkbox.'))
        self.assertTrue(matches('signup-on', 'On. Help improve MyChef. Checkbox.'))
        self.assertTrue(matches('off', 'Checkbox off. Help improve MyChef.'))
        self.assertFalse(matches('off', 'Off. Help improve MyChef.'))
        self.assertFalse(matches('on', 'TalkBack on. Help improve MyChef. Checkbox.'))
        self.assertFalse(matches('off', 'Double tap to turn off. Help improve MyChef. Checkbox.'))
        self.assertFalse(matches('off', 'Other control off. Help improve MyChef. Checkbox.'))
        self.assertFalse(matches('on', 'Off. Help improve MyChef. Checkbox checked.'))
        self.assertFalse(matches('off', 'Off. Help improve MyChef. Checkbox checked.'))

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

    def test_capture_is_not_ready_when_only_the_wave_header_exists(self):
        with tempfile.TemporaryDirectory(prefix='analytics-capture-') as directory, ExitStack() as stack:
            speech = Speech.__new__(Speech)
            speech.state, speech.active, speech.passed, speech.attempts = Path(directory), None, set(), {}
            speech.env, speech.log, speech.sink = {}, None, 'fictional-sink'
            capture = SimpleNamespace(poll=lambda: None)
            def wait_for(check, description, seconds):
                path = speech.path
                path.write_bytes(b'R' * 44)
                self.assertFalse(check(), 'A file/header is not a running audio capture')
                path.write_bytes(b'R' * 8192)
                self.assertTrue(check(), 'Captured payload must release the focus operation')
            speech.native = SimpleNamespace(wait_for=wait_for)
            with patch('analytics_speech.subprocess.Popen', return_value=capture):
                self.assertEqual(speech.operate('speech-start', 'on'), {'passed': True})
            self.assertEqual(speech.active, 'on')

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


class SpeechToolSetupTest(unittest.TestCase):
    class ReachedRecognizer(Exception):
        pass

    def prepare(self, payload=None, timeout=False, available=()):
        self.calls = []
        with tempfile.TemporaryDirectory(prefix='analytics-package-test-') as directory:
            root = Path(directory)
            event = root / 'event.json'; event.write_text('{}')
            def run(arguments, **options):
                if 'apt-get' not in arguments:
                    raise self.ReachedRecognizer()
                self.calls.append((arguments, options))
                if 'install' in arguments and payload is not None:
                    output = options.get('stdout')
                    if hasattr(output, 'write'):
                        output.write(payload); output.flush()
                    if timeout:
                        raise subprocess.TimeoutExpired(arguments, options['timeout'], output=payload)
                    raise subprocess.CalledProcessError(100, arguments, output=payload)
                return subprocess.CompletedProcess(arguments, 0)
            with patch.dict(os.environ, {'GITHUB_EVENT_PATH': str(event), 'NATIVE_PLATFORM': 'android'}), \
                 patch('analytics_installed.authorized', return_value=True), \
                 patch('analytics_speech.tools', return_value=root / 'tools'), \
                 patch('shutil.which', side_effect=lambda name: '/fictional/bin/' + name if name in available else None), \
                 patch('analytics_speech.subprocess.run', side_effect=run):
                analytics_speech.prepare()

    def test_package_setup_cannot_prompt_and_preserves_existing_dependencies(self):
        with self.assertRaises(self.ReachedRecognizer):
            self.prepare()
        self.assertEqual(len(self.calls), 2)
        for arguments, options in self.calls:
            self.assertEqual(list(arguments[:3]), ['sudo', '-n', 'env'])
            self.assertIn('DEBIAN_FRONTEND=noninteractive', arguments)
            self.assertIn('NEEDRESTART_MODE=l', arguments)
            self.assertEqual(options.get('stdin'), subprocess.DEVNULL)
            self.assertEqual(options['timeout'], 420 if 'install' in arguments else 240)
        install = self.calls[1][0]
        self.assertIn('--no-upgrade', install)
        self.assertEqual(list(install[install.index('--no-install-recommends') + 1:]),
                         ['ffmpeg', 'cmake', 'g++', 'pulseaudio', 'pulseaudio-utils', 'xvfb', 'xdotool'])

    def test_preinstalled_runner_tools_do_not_trigger_any_package_operation(self):
        with self.assertRaises(self.ReachedRecognizer):
            self.prepare(available={'ffmpeg', 'cmake', 'g++', 'pulseaudio', 'pactl', 'Xvfb', 'xdotool'})
        self.assertEqual(self.calls, [])

    def test_only_missing_tools_request_their_existing_packages(self):
        with self.assertRaises(self.ReachedRecognizer):
            self.prepare(available={'ffmpeg', 'cmake', 'g++', 'Xvfb'})
        install = self.calls[1][0]
        self.assertEqual(list(install[install.index('--no-install-recommends') + 1:]),
                         ['pulseaudio', 'pulseaudio-utils', 'xdotool'])

    def test_timeout_progress_reports_only_a_closed_package_phase(self):
        for progress, phase in [(b'Get:1 https://private.example.test/repo', 'download'),
                                (b'Setting up private-customer-canary user@example.test', 'configure'),
                                (b'Processing triggers for private-customer-canary', 'trigger')]:
            with self.subTest(phase=phase), self.assertRaisesRegex(
                    RuntimeError, 'stage=install reason=timeout phase=' + phase) as caught:
                self.prepare(progress, timeout=True)
            for secret in ['private', 'customer', '@example', 'https:', 'canary']:
                self.assertNotIn(secret, str(caught.exception))

    def test_package_timeout_retains_only_a_closed_network_reason(self):
        payload = b'Failed to fetch https://private.example.test/repo Bearer private-token user@example.test'
        with self.assertRaisesRegex(RuntimeError, 'stage=install reason=network') as caught:
            self.prepare(payload, timeout=True)
        for value in ['private', 'Bearer', '@example', 'https:', 'token']:
            self.assertNotIn(value, str(caught.exception))

    def test_package_lock_failure_does_not_disclose_paths_or_messages(self):
        payload = b'Could not get lock /private/customer-canary/lock user@example.test'
        with self.assertRaisesRegex(RuntimeError, 'stage=install reason=lock') as caught:
            self.prepare(payload)
        for value in ['private', 'customer', '@example', '/lock']:
            self.assertNotIn(value, str(caught.exception))


if __name__ == '__main__':
    unittest.main()
