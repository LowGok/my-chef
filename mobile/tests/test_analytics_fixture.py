"""Offline fixture/controller boundaries; no device or CI authority is asserted."""
import io
import copy
import json
from pathlib import Path
import plistlib
import tempfile
import unittest
import zipfile
from contextlib import redirect_stdout

from analytics_fixture import Fixture, own_export
import analytics_ios


class FixtureBoundaryTest(unittest.TestCase):
    def test_profile_failure_diagnostics_keep_only_closed_facts(self):
        fixture = object.__new__(Fixture)
        fixture.native_events = [('PUT', 'profile', 200), ('PUT', 'profile', 200)]
        private = 'private-canary@example.test'
        fixture.call = lambda method, route: (200, json.dumps({
            'preferences': {'diet': ['Vegetarian mealsMediterranean ' + private]}}).encode(), None)
        output = io.StringIO()
        with redirect_stdout(output):
            for _ in range(2):
                self.assertEqual(fixture.operate({'operation': 'profile-input', 'target': 'updated'}), {'passed': False})
        lines = output.getvalue().splitlines()
        self.assertEqual(len(lines), 1)
        facts = json.loads(lines[0].split(': ', 1)[1])
        self.assertEqual(facts, dict(target='updated', profileSaves=2,
                                     initialPresent=True, updatedPresent=True, empty=False))
        for content in (private, 'Vegetarian', 'Mediterranean', 'preferences'):
            self.assertNotIn(content, output.getvalue())

    def test_xcode_failure_keeps_the_reason_without_private_input(self):
        value = analytics_ios.safe_diagnostic(
            'ignored secret stdout\nxcodebuild: error: Unable to load /private/project/Probe.xcodeproj user@example.test Bearer ' + 'x' * 43 + ' synthetic-password\nReason: missing target',
            ['synthetic-password'])
        self.assertIn('Unable to load', value)
        self.assertIn('Reason: missing target', value)
        for private in ('ignored secret stdout', '/private', 'user@example.test', 'x' * 43, 'synthetic-password'):
            self.assertNotIn(private, value)

    def test_native_profile_input_requires_exact_persisted_fixture_content(self):
        fixture = object.__new__(Fixture)
        calls = []
        def response(method, route):
            calls.append((method, route))
            return 200, json.dumps({'preferences': {'diet': ['Vegetarian meals']}}).encode(), None
        fixture.call = response
        self.assertEqual(fixture.operate({'operation': 'profile-input', 'target': 'initial'}), {'passed': True})
        self.assertEqual(fixture.operate({'operation': 'profile-input', 'target': 'updated'}), {'passed': False})
        self.assertEqual(calls, [('GET', 'profile'), ('GET', 'profile')])
        for fields in ({'operation': 'profile-input', 'target': 'arbitrary'},
                       {'operation': 'profile-input', 'target': 'initial', 'recipient': 'private@example.test'}):
            with self.assertRaises(ValueError): fixture.operate(fields)

    def test_approval_fixture_requires_a_real_disabled_notice_before_switching(self):
        with tempfile.TemporaryDirectory(prefix='analytics-notice-') as folder:
            state = Path(folder)
            config = state / 'analytics-config.json'
            config.write_text(json.dumps({'collection': {'Enabled': False}}))
            fixture = object.__new__(Fixture)
            fixture.state, fixture.native_events = state, []
            restarted = []
            fixture.restart = lambda: restarted.append(True)
            with self.assertRaises(RuntimeError):
                fixture.operate({'operation': 'approved'})
            self.assertFalse(json.loads(config.read_text())['collection']['Enabled'])
            self.assertEqual(restarted, [])
            fixture.native_events.append(('GET', 'analytics-notice-disabled', 200))
            self.assertEqual(fixture.operate({'operation': 'approved'}), {'passed': True})
            self.assertTrue(json.loads(config.read_text())['collection']['Enabled'])
            self.assertEqual(restarted, [True])

    def archive(self, **files):
        buffer = io.BytesIO()
        with zipfile.ZipFile(buffer, 'w') as archive:
            for name, value in files.items():
                archive.writestr(name, value)
        return buffer.getvalue()

    def test_native_saved_export_requires_owned_data_and_rejects_foreign_accounts(self):
        own = 'analyticsinstalled@example.test'
        self.assertTrue(own_export(self.archive(**{'account.json': own, 'analytics.json': '{}'}), own))
        self.assertFalse(own_export(b'not a ZIP', own))
        self.assertFalse(own_export(self.archive(**{'account.json': own}), own))
        self.assertFalse(own_export(self.archive(**{'account.json': 'foreign@example.test', 'analytics.json': '{}'}), own))
        for other in ('analyticsdisabled@example.test', 'analyticsassessed@example.test'):
            self.assertFalse(own_export(self.archive(**{'account.json': own, 'analytics.json': other}), own))

    def test_transfer_refuses_signing_material_databases_and_other_app_payloads(self):
        from analytics_artifact import verify_archive
        info = plistlib.dumps({'CFBundleIdentifier': 'com.gkldevelopment.mychef.preview',
                              'CFBundleSupportedPlatforms': ['iPhoneSimulator']})
        clean = {'App.app/Info.plist': info, 'App.app/App': b'fictional executable'}
        self.assertTrue(verify_archive(self.archive(**clean)))
        for name, value in (('App.app/customer.sqlite', b'private'), ('App.app/signing.p12', b'private'),
                            ('App.app/client.key', b'private'), ('App.app/hidden.bin', b'SQLite format 3\0private'),
                            ('App.app/secret.txt', b'-----BEGIN PRIVATE KEY-----'), ('Other.app/Info.plist', info)):
            self.assertFalse(verify_archive(self.archive(**{**clean, name: value})))
        self.assertFalse(verify_archive(self.archive(**{**clean, 'App.app/Info.plist': plistlib.dumps({
            'CFBundleIdentifier': 'com.gkldevelopment.mychef.preview', 'CFBundleSupportedPlatforms': ['iPhoneOS']})})))

    def test_speech_supplement_requires_the_same_candidate_and_every_other_observation(self):
        from analytics_voiceover import combine
        contract = json.loads((Path(__file__).parent / 'analytics-contract.json').read_text())
        binding = dict(sourceCommit='a' * 40, apiSha256='b' * 64, bundleSha256='c' * 64,
                       artifactSha256='d' * 64, fixtureConfigSha256='e' * 64, contractVersion=1)
        primary = dict(schemaVersion=1, platform='ios-simulator', binding=binding,
                       checks={group: dict.fromkeys(names, True) for group, names in contract['checks'].items()})
        speech = ('platformScreenReader', 'spokenStatesAndErrors', 'focusOrder')
        for key in speech:
            primary['checks']['accessibility'][key] = False
        supplement_binding = dict(binding, fixtureConfigSha256='f' * 64)
        supplement = dict(schemaVersion=1, binding=supplement_binding, checks=dict.fromkeys(speech, True))
        self.assertIsNotNone(combine(primary, supplement, binding, supplement_binding))
        for group, names in contract['checks'].items():
            for name in names:
                if group == 'accessibility' and name in speech:
                    continue
                bad = copy.deepcopy(primary); bad['checks'][group][name] = False
                self.assertIsNone(combine(bad, supplement, binding, supplement_binding))
        for key in binding:
            bad = copy.deepcopy(supplement)
            bad['binding'][key] = 2 if key == 'contractVersion' else '0' * (40 if key == 'sourceCommit' else 64)
            self.assertIsNone(combine(primary, bad, binding, supplement_binding))
        for name in speech:
            for value in (False, 1, 'true', None):
                bad = copy.deepcopy(supplement); bad['checks'][name] = value
                self.assertIsNone(combine(primary, bad, binding, supplement_binding))
        bad = copy.deepcopy(supplement); bad['email'] = 'private@example.test'
        self.assertIsNone(combine(primary, bad, binding, supplement_binding))

    def test_generated_probe_has_only_a_separate_unsigned_ui_target(self):
        with tempfile.TemporaryDirectory(prefix='analytics-probe-') as folder:
            project = analytics_ios.project(Path(folder))
            value = plistlib.loads((project / 'project.pbxproj').read_bytes())
            targets = [item for item in value['objects'].values() if item.get('isa') == 'PBXNativeTarget']
            self.assertEqual(len(targets), 1)
            self.assertEqual(targets[0]['productType'], 'com.apple.product-type.bundle.ui-testing')
            self.assertEqual(targets[0]['dependencies'], [])
            settings = [item['buildSettings'] for item in value['objects'].values() if item.get('isa') == 'XCBuildConfiguration']
            self.assertTrue(all(item['CODE_SIGNING_ALLOWED'] == 'NO' for item in settings))
            self.assertFalse(list(Path(folder).rglob('App.app')))


if __name__ == '__main__':
    unittest.main()
