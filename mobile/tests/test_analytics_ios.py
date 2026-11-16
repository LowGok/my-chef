"""Order contracts for the separate probe, without claiming native evidence."""
import json
from pathlib import Path
import plistlib
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import analytics_ios


class IOSProbeOrderTest(unittest.TestCase):
    def exercise(self, fail_warmup=False):
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
                if fail_warmup:
                    raise RuntimeError('Fictional bootstrap unavailable')

            with patch.object(analytics_ios.subprocess, 'run', side_effect=execute):
                run = lambda: analytics_ios.run(state, {'url': 'http://127.0.0.1:18137/control', 'token': 'a' * 43},
                                               {'email': 'fictional@example.test', 'password': 'fictional'},
                                               'fictional-device', state / 'container', voiceover_only=True,
                                               before_test=warmup)
                if fail_warmup:
                    with self.assertRaisesRegex(RuntimeError, 'Fictional bootstrap unavailable'):
                        run()
                else:
                    self.assertEqual(run(), {})
            return stages

    def test_native_bootstrap_occurs_after_probe_compilation_before_xctest(self):
        self.assertEqual(self.exercise(), ['compile', 'bootstrap', 'test'])

    def test_failed_bootstrap_prevents_xctest_execution(self):
        self.assertEqual(self.exercise(fail_warmup=True), ['compile', 'bootstrap'])


if __name__ == '__main__':
    unittest.main()
