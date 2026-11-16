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


class IOSProbeOrderTest(unittest.TestCase):
    def exercise(self, fail_warmup=False, precompile=False):
        with tempfile.TemporaryDirectory(prefix='analytics-probe-order-') as folder:
            state = Path(folder)
            stages = []

            def execute(arguments, **_):
                if arguments[-1] == 'build-for-testing':
                    stages.append('compile')
                    products = state / 'ios-probe-build/Build/Products'
                    products.mkdir(parents=True)
                    (products / 'AnalyticsProbe.xctestrun').write_bytes(plistlib.dumps({'AnalyticsProbe': {}}))
                else:
                    stages.append('test')
                    plan = plistlib.loads((state / 'ios-probe-build/Build/Products/AnalyticsProbe.xctestrun').read_bytes())
                    self.assertEqual(plan['AnalyticsProbe']['UITargetAppBundleIdentifier'], analytics_ios.native.APP)
                    (state / 'analytics-ios-observations.json').write_text(json.dumps({'checks': {}}))
                return subprocess.CompletedProcess(arguments, 0, b'')

            def warmup():
                stages.append('bootstrap')
                config = json.loads((state / 'analytics-ios-input.json').read_text())
                self.assertEqual(config['voiceOverOnly'], 'true')
                self.assertEqual(config['preserveBootstrap'], 'true')
                if fail_warmup:
                    raise RuntimeError('Fictional bootstrap unavailable')

            with patch.object(analytics_ios.subprocess, 'run', side_effect=execute):
                prepared = analytics_ios.prepare(state, 'fictional-device') if precompile else None
                if precompile:
                    stages.append('boot')
                run = lambda: analytics_ios.run(state, {'url': 'http://127.0.0.1:18137/control', 'token': 'a' * 43},
                                               {'email': 'fictional@example.test', 'password': 'fictional'},
                                               'fictional-device', state / 'container', voiceover_only=True,
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

    def test_failed_bootstrap_prevents_xctest_execution(self):
        self.assertEqual(self.exercise(fail_warmup=True), ['compile', 'bootstrap'])



class IOSSessionBootstrapTest(unittest.TestCase):
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


class VoiceOverHostAttachmentTest(unittest.TestCase):
    def snapshot(self, identifiers):
        return json.dumps({'devices': {'runtime': [{'udid': value, 'state': 'Booted'} for value in identifiers]}})

    def test_only_the_exact_owned_booted_phone_can_reach_the_native_warmup(self):
        with patch.object(analytics_voiceover.native, 'command', return_value=self.snapshot(['owned-phone'])):
            analytics_voiceover.require_owned_phone('owned-phone')
        for identifiers in ([], ['foreign-phone'], ['owned-phone', 'foreign-phone'], ['owned-phone', 'owned-phone']):
            with self.subTest(identifiers=identifiers), \
                 patch.object(analytics_voiceover.native, 'command', return_value=self.snapshot(identifiers)):
                with self.assertRaisesRegex(RuntimeError, 'owned booted VoiceOver phone'):
                    analytics_voiceover.require_owned_phone('owned-phone')


if __name__ == '__main__':
    unittest.main()
