"""Order contracts for the separate probe, without claiming native evidence."""
import json
from pathlib import Path
import plistlib
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import analytics_ios
import analytics_voiceover


class IOSTextObservationTest(unittest.TestCase):
    def test_observations_use_the_current_installed_container(self):
        with tempfile.TemporaryDirectory(prefix='analytics-text-') as folder:
            old, current = Path(folder) / 'old', Path(folder) / 'current'
            for container in (old, current):
                (container / 'tmp').mkdir(parents=True)
            flags = dict.fromkeys(('root', 'label', 'notice', 'fits', 'osTextEnlarged'), True)
            (old / 'tmp/analytics-text-result.json').write_text('{}')
            (current / 'tmp/analytics-text-result.json').write_text(json.dumps(flags))
            with patch.object(analytics_ios.native, 'command', return_value=str(current)) as command:
                self.assertEqual(analytics_ios.text_observation('owned-phone'), (current, flags))
                command.assert_called_once_with('xcrun', 'simctl', 'get_app_container',
                                                'owned-phone', analytics_ios.native.APP, 'data')

    def test_missing_untrusted_and_incomplete_measurements_fail_closed(self):
        with tempfile.TemporaryDirectory(prefix='analytics-text-') as folder:
            container = Path(folder)
            (container / 'tmp').mkdir()
            path = container / 'tmp/analytics-text-result.json'
            flags = dict.fromkeys(('root', 'label', 'notice', 'fits', 'osTextEnlarged'), True)
            with patch.object(analytics_ios.native, 'command', return_value=str(container)):
                for value in (None, '{}', 'x' * 4097, json.dumps({**flags, 'fits': False}),
                              json.dumps({**flags, 'label': 1}), json.dumps({**flags, 'extra': True})):
                    with self.subTest(value=value):
                        path.unlink(missing_ok=True)
                        if value is not None:
                            path.write_text(value)
                        with self.assertRaisesRegex(RuntimeError, 'Installed iOS text observation unavailable or invalid'):
                            analytics_ios.text_observation('owned-phone')
                        path.unlink(missing_ok=True)
                other = container / 'other.json'
                other.write_text(json.dumps(flags))
                path.symlink_to(other)
                with self.assertRaisesRegex(RuntimeError, 'Installed iOS text observation unavailable or invalid'):
                    analytics_ios.text_observation('owned-phone')


class IOSProbeOrderTest(unittest.TestCase):
    def exercise(self, fail_warmup=False, precompile=False, *, layout_only=False, voiceover_only=True,
                 expected_timeout=1500, modified_source=None):
        with tempfile.TemporaryDirectory(prefix='analytics-probe-order-') as folder:
            state = Path(folder)
            stages = []

            def execute(arguments, **options):
                if arguments[-1] == 'build-for-testing':
                    stages.append('compile')
                    products = state / 'ios-probe-build/Build/Products'
                    products.mkdir(parents=True)
                    (products / 'AnalyticsProbe.xctestrun').write_bytes(plistlib.dumps({'AnalyticsProbe': {}}))
                else:
                    stages.append('test')
                    self.assertEqual(options['timeout'], expected_timeout)
                    plan = plistlib.loads((state / 'ios-probe-build/Build/Products/AnalyticsProbe.xctestrun').read_bytes())
                    self.assertEqual(plan['AnalyticsProbe']['UITargetAppBundleIdentifier'], analytics_ios.native.APP)
                    (state / 'analytics-ios-observations.json').write_text(json.dumps({'checks': {}}))
                return subprocess.CompletedProcess(arguments, 0, b'Analytics iOS VoiceOver navigation: native traversal verified=true\n')

            def warmup():
                stages.append('bootstrap')
                config = json.loads((state / 'analytics-ios-input.json').read_text())
                self.assertEqual(config['voiceOverOnly'], 'true' if voiceover_only else 'false')
                self.assertEqual(config['preserveBootstrap'], 'true')
                if fail_warmup:
                    raise RuntimeError('Fictional bootstrap unavailable')

            with patch.object(analytics_ios.subprocess, 'run', side_effect=execute), \
                 patch.object(analytics_ios, 'capture_anonymous_start', side_effect=lambda _identifier: stages.append('anonymous'), create=True):
                prepared = analytics_ios.prepare(state, 'fictional-device') if precompile else None
                if precompile:
                    stages.append('boot')
                    if modified_source:
                        (state / 'ios-analytics-probe' / modified_source).write_text('untrusted change')
                run = lambda: analytics_ios.run(state, {'url': 'http://127.0.0.1:18137/control', 'token': 'a' * 43},
                                               {'email': 'fictional@example.test', 'password': 'fictional'},
                                               'fictional-device', state / 'container', layout_only=layout_only,
                                               voiceover_only=voiceover_only,
                                               before_test=warmup, prepared_plan=prepared)
                if fail_warmup:
                    with self.assertRaisesRegex(RuntimeError, 'Fictional bootstrap unavailable'):
                        run()
                else:
                    self.assertEqual(run(), {})
            return stages

    def test_native_bootstrap_occurs_after_probe_compilation_before_xctest(self):
        self.assertEqual(self.exercise(), ['compile', 'bootstrap', 'test'])

    def test_precompiled_probe_is_not_rebuilt_after_the_phone_boots(self):
        self.assertEqual(self.exercise(precompile=True), ['compile', 'boot', 'bootstrap', 'test'])

    def test_precompiled_probe_rejects_modified_swift_and_native_gesture_sources(self):
        for name in ('AnalyticsProbe.swift', 'AnalyticsTouch.h', 'AnalyticsTouch.m'):
            with self.subTest(source=name), self.assertRaisesRegex(RuntimeError, 'current isolated XCTest plan'):
                self.exercise(precompile=True, modified_source=name)

    def test_failed_bootstrap_prevents_xctest_execution(self):
        self.assertEqual(self.exercise(fail_warmup=True), ['compile', 'bootstrap'])

    def test_failed_primary_bootstrap_cannot_capture_media_or_start_test_actions(self):
        self.assertEqual(self.exercise(fail_warmup=True, voiceover_only=False), ['compile', 'bootstrap'])

    def test_only_the_complete_functional_journey_receives_the_longer_budget(self):
        self.assertEqual(self.exercise(voiceover_only=False, expected_timeout=2100),
                         ['compile', 'bootstrap', 'anonymous', 'test'])
        self.assertEqual(self.exercise(layout_only=True, voiceover_only=False),
                         ['compile', 'bootstrap', 'test'])
        self.assertEqual(self.exercise(), ['compile', 'bootstrap', 'test'])



class IOSSessionBootstrapTest(unittest.TestCase):
    def test_only_exact_native_traversal_completion_is_accepted(self):
        self.assertTrue(analytics_ios.voice_navigation('Analytics iOS VoiceOver navigation: native traversal verified=true\n'))
        for line in ('flat selected=true', 'grouped selected=true', 'native traversal verified=false',
                     'native traversal verified=true private', 'native traversal verified=1'):
            self.assertFalse(analytics_ios.voice_navigation('Analytics iOS VoiceOver navigation: ' + line))

    def test_voice_environment_summary_keeps_only_closed_flags_and_counts(self):
        raw = ('private fictional utterance must stay private\n'
               'Analytics iOS voice environment: api=true native=false voices=12 english=2\n')
        self.assertEqual(analytics_ios.voice_environment(raw),
                         'api=true native=false voices=12 english=2')
        for value in ('api=yes native=false voices=12 english=2',
                      'api=true native=false voices=1000 english=2',
                      'api=true native=false voices=1 english=2',
                      'api=true native=false voices=12 english=2 private'):
            self.assertIsNone(analytics_ios.voice_environment('Analytics iOS voice environment: ' + value))

    def test_cold_native_bootstrap_accepts_late_real_responses_with_a_bounded_deadline(self):
        for arrival, accepted in ((130, True), (181, False)):
            events = [('POST', 'session', 201), ('GET', 'session', 200)]
            elapsed = [0]
            def sleep(seconds):
                elapsed[0] += seconds
                if elapsed[0] == arrival:
                    events.extend([('POST', 'refresh', 200), ('GET', 'session', 200)])
            with self.subTest(arrival=arrival), \
                 patch.object(analytics_ios.native, 'command') as launch, \
                 patch.object(analytics_ios.native.time, 'monotonic', side_effect=lambda: elapsed[0]), \
                 patch.object(analytics_ios.native.time, 'sleep', side_effect=sleep):
                if accepted:
                    analytics_ios.bootstrap('owned-phone', events, [], lambda: None)
                    self.assertEqual(elapsed[0], arrival)
                else:
                    with self.assertRaisesRegex(RuntimeError, 'fresh iOS analytics fixture session'):
                        analytics_ios.bootstrap('owned-phone', events, [], lambda: None)
                self.assertLessEqual(elapsed[0], 180)
                launch.assert_called_once()

    def test_only_fresh_native_session_responses_acknowledge_the_warmed_app(self):
        for responses, accepted in (([], False), ([('GET', 'session', 200)], False),
                ([('POST', 'session', 201)], False),
                ([('POST', 'session', 201), ('GET', 'session', 200)], True),
                ([('POST', 'refresh', 200), ('GET', 'session', 200)], True)):
            events = [('POST', 'session', 201), ('GET', 'session', 200)]
            def launch(*args, **_):
                self.assertEqual(args, ('xcrun', 'simctl', 'launch', 'owned-phone', analytics_ios.native.APP,
                                        '--mychef-analytics-native-fixture'))
                events.extend(responses)
            def wait(check, *_):
                if not check():
                    raise RuntimeError('Fresh session missing')
            with self.subTest(responses=responses), \
                 patch.object(analytics_ios.native, 'command', side_effect=launch) as command, \
                 patch.object(analytics_ios.native, 'wait_for', side_effect=wait):
                if accepted:
                    analytics_ios.bootstrap('owned-phone', events, [], lambda: None)
                else:
                    with self.assertRaisesRegex(RuntimeError, 'Fresh session missing'):
                        analytics_ios.bootstrap('owned-phone', events, [], lambda: None)
                self.assertEqual(command.call_count, 1)


class VoiceOverOwnedPhoneTest(unittest.TestCase):
    def snapshot(self, identifiers):
        return json.dumps({'devices': {'runtime': [{'udid': value, 'state': 'Booted'} for value in identifiers]}})

    def test_only_the_exact_owned_booted_phone_can_reach_native_bootstrap(self):
        with patch.object(analytics_voiceover.native, 'command', return_value=self.snapshot(['owned-phone'])):
            analytics_voiceover.require_owned_phone('owned-phone')
        for identifiers in ([], ['foreign-phone'], ['owned-phone', 'foreign-phone'], ['owned-phone', 'owned-phone']):
            with self.subTest(identifiers=identifiers), \
                 patch.object(analytics_voiceover.native, 'command', return_value=self.snapshot(identifiers)):
                with self.assertRaisesRegex(RuntimeError, 'owned booted VoiceOver phone'):
                    analytics_voiceover.require_owned_phone('owned-phone')


class AnonymousCaptureTest(unittest.TestCase):
    def test_bounded_png_is_captured_once_with_a_host_deadline(self):
        with tempfile.TemporaryDirectory(prefix='anonymous-capture-') as folder:
            artifacts = Path(folder)
            image = artifacts / 'analytics-anonymous.png'
            def capture(*arguments, **options):
                self.assertEqual(arguments, ('xcrun', 'simctl', 'io', 'owned-phone', 'screenshot', str(artifacts / 'analytics-anonymous-pending.png')))
                self.assertEqual(options, {'timeout': 20})
                Path(arguments[-1]).write_bytes(b'\x89PNG\r\n\x1a\nfictional')
                return ''
            with patch.object(analytics_ios.native, 'ARTIFACTS', artifacts), \
                 patch.object(analytics_ios.native, 'command', side_effect=capture) as command:
                self.assertTrue(analytics_ios.capture_anonymous_start('owned-phone'))
                self.assertEqual(command.call_count, 1)
                self.assertEqual(image.read_bytes(), b'\x89PNG\r\n\x1a\nfictional')

    def test_capture_failure_cleans_partial_media_and_hides_error_details(self):
        with tempfile.TemporaryDirectory(prefix='anonymous-capture-') as folder:
            artifacts = Path(folder)
            image = artifacts / 'analytics-anonymous.png'
            def capture(*arguments, **_options):
                Path(arguments[-1]).write_bytes(b'partial')
                raise RuntimeError('fictional-private-capture-details')
            with patch.object(analytics_ios.native, 'ARTIFACTS', artifacts), \
                 patch.object(analytics_ios.native, 'command', side_effect=capture), \
                 patch('builtins.print') as output:
                self.assertFalse(analytics_ios.capture_anonymous_start('owned-phone'))
                self.assertFalse(image.exists())
                self.assertNotIn('fictional-private-capture-details', str(output.call_args_list))

    def test_invalid_or_oversized_media_is_not_retained(self):
        for data in (b'not a png', b'\x89PNG\r\n\x1a\n' + bytes(2 * 1024 * 1024)):
            with self.subTest(size=len(data)), tempfile.TemporaryDirectory(prefix='anonymous-capture-') as folder:
                artifacts = Path(folder)
                image = artifacts / 'analytics-anonymous.png'
                with patch.object(analytics_ios.native, 'ARTIFACTS', artifacts), \
                     patch.object(analytics_ios.native, 'command', side_effect=lambda *args, **_options: Path(args[-1]).write_bytes(data)):
                    self.assertFalse(analytics_ios.capture_anonymous_start('owned-phone'))
                    self.assertFalse(image.exists())


    def test_failed_cleanup_cannot_publish_unvalidated_media(self):
        with tempfile.TemporaryDirectory(prefix='anonymous-capture-') as folder:
            artifacts = Path(folder)
            image = artifacts / 'analytics-anonymous.png'
            pending = artifacts / 'analytics-anonymous-pending.png'
            with patch.object(analytics_ios.native, 'ARTIFACTS', artifacts), \
                 patch.object(analytics_ios.native, 'command', side_effect=lambda *args, **_options: Path(args[-1]).write_bytes(b'invalid')), \
                 patch.object(Path, 'unlink', side_effect=PermissionError('fictional cleanup failure')):
                self.assertFalse(analytics_ios.capture_anonymous_start('owned-phone'))
                self.assertFalse(image.exists())
                self.assertTrue(pending.exists())

    def test_capture_cannot_replace_an_image_created_during_the_command(self):
        with tempfile.TemporaryDirectory(prefix='anonymous-capture-') as folder:
            artifacts = Path(folder)
            image = artifacts / 'analytics-anonymous.png'
            def capture(*arguments, **_options):
                Path(arguments[-1]).write_bytes(b'\x89PNG\r\n\x1a\nfictional')
                image.write_bytes(b'original immutable image')
            with patch.object(analytics_ios.native, 'ARTIFACTS', artifacts), \
                 patch.object(analytics_ios.native, 'command', side_effect=capture):
                self.assertFalse(analytics_ios.capture_anonymous_start('owned-phone'))
                self.assertEqual(image.read_bytes(), b'original immutable image')

    def test_existing_image_cannot_be_overwritten_by_a_later_capture(self):
        with tempfile.TemporaryDirectory(prefix='anonymous-capture-') as folder:
            artifacts = Path(folder)
            image = artifacts / 'analytics-anonymous.png'
            image.write_bytes(b'original immutable pre-input media')
            with patch.object(analytics_ios.native, 'ARTIFACTS', artifacts), \
                 patch.object(analytics_ios.native, 'command') as command:
                self.assertFalse(analytics_ios.capture_anonymous_start('owned-phone'))
                self.assertEqual(image.read_bytes(), b'original immutable pre-input media')
                command.assert_not_called()



if __name__ == '__main__':
    unittest.main()
