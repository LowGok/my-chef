"""Exercise the actual analytics authorization predicate without opening a device."""
import ast
from collections.abc import Mapping
from types import MappingProxyType
import copy
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parent.parent


def authorization():
    source = ast.parse((ROOT / 'mobile/tests/analytics_installed.py').read_text())
    function = next(node for node in source.body if isinstance(node, ast.FunctionDef) and node.name == 'authorized')
    scope = {'re': re, 'Mapping': Mapping}
    exec(compile(ast.Module(body=[function], type_ignores=[]), '<analytics-authorization>', 'exec'), scope)
    return scope['authorized']


class AnalyticsAuthorizationTest(unittest.TestCase):
    def setUp(self):
        self.authorized = authorization()
        self.event = {'repository': {'id': 1454481532, 'full_name': 'LowGok/my-chef', 'private': False},
                      'sender': {'login': 'LowGok'}, 'ref': 'refs/heads/ci/analytics-installed-b9e9699',
                      'after': 'a' * 40, 'deleted': False}
        self.environment = {'GITHUB_ACTIONS': 'true', 'GITHUB_EVENT_NAME': 'push',
                            'GITHUB_REPOSITORY': 'LowGok/my-chef', 'GITHUB_ACTOR': 'LowGok',
                            'GITHUB_REF': self.event['ref'], 'GITHUB_SHA': self.event['after'],
                            'NATIVE_PLATFORM': 'android'}

    def test_exact_push_is_allowed_for_only_the_two_unsigned_platforms(self):
        for platform in ('android', 'ios-simulator'):
            self.assertTrue(self.authorized(self.event, dict(self.environment, NATIVE_PLATFORM=platform)))

    def test_runner_environment_is_a_mapping_not_a_plain_dictionary(self):
        self.assertTrue(self.authorized(self.event, MappingProxyType(self.environment)))

    def test_foreign_event_environment_or_mismatched_revision_is_refused(self):
        cases = [('GITHUB_ACTIONS', 'false'), ('GITHUB_EVENT_NAME', 'pull_request_target'),
                 ('GITHUB_REPOSITORY', 'Other/my-chef'), ('GITHUB_ACTOR', 'Other'),
                 ('GITHUB_REF', 'refs/heads/main'), ('GITHUB_SHA', 'b' * 40),
                 ('NATIVE_PLATFORM', 'ios-device')]
        for key, value in cases:
            with self.subTest(environment=key):
                self.assertFalse(self.authorized(self.event, dict(self.environment, **{key: value})))
        for path, value in ((('repository', 'id'), 1), (('repository', 'private'), True),
                            (('repository', 'full_name'), 'Other/my-chef'), (('sender', 'login'), 'Other'),
                            (('ref',), 'refs/heads/main'), (('after',), 'b' * 40), (('deleted',), True)):
            event = copy.deepcopy(self.event)
            target = event
            for key in path[:-1]:
                target = target[key]
            target[path[-1]] = value
            with self.subTest(event=path):
                self.assertFalse(self.authorized(event, self.environment))

    def test_missing_malformed_or_coerced_revision_is_not_approval(self):
        for value in ('', 'a' * 39, 'A' * 40, 'g' * 40, None, 123):
            event = dict(self.event, after=value)
            with self.subTest(sha=value):
                self.assertFalse(self.authorized(event, dict(self.environment, GITHUB_SHA=value)))
        for value in (None, [], True, {'repository': None}, {'repository': {'id': 1454481532}, 'sender': None}):
            with self.subTest(payload=value):
                self.assertFalse(self.authorized(value, self.environment))

    def test_voiceover_uses_the_same_unsigned_artifact_and_a_standard_runner(self):
        workflow = (ROOT / '.github/workflows/native-compile.yml').read_text()
        self.assertIn('  analytics-voiceover:\n', workflow)
        lane = workflow.split('  analytics-voiceover:\n', 1)[1].split('  compile:\n', 1)[0]
        self.assertIn('needs: analytics-journeys', lane)
        self.assertIn('runs-on: xcode-27', lane)
        self.assertNotIn('xlarge', lane)
        self.assertIn('NATIVE_ANALYTICS_VOICEOVER: "1"', lane)
        self.assertIn('analytics-voiceover-input-${{ github.sha }}', lane)
        self.assertIn('analytics-journeys-ios-simulator-${{ github.sha }}', lane)
        self.assertIn('retention-days: 1', lane)
        self.assertNotIn('secrets:', lane)
        self.assertNotIn('mobile-compile', lane)

    def test_primary_ios_uses_standard_intel_capacity_without_changing_voiceover(self):
        workflow = (ROOT / '.github/workflows/native-compile.yml').read_text()
        primary = workflow.split('  analytics-journeys:\n', 1)[1].split('  analytics-voiceover:\n', 1)[0]
        self.assertIn("runs-on: ${{ matrix.platform == 'android' && 'ubuntu-24.04' || 'macos-15-intel' }}", primary)
        self.assertNotIn('xlarge', primary)
        self.assertNotIn('macos-15-large', primary)

    def test_isolated_artifact_uploads_allow_same_source_job_retries(self):
        workflow = (ROOT / '.github/workflows/native-compile.yml').read_text().split('  analytics-signup-checkbox:\n', 1)[0]
        uploads = [step for step in re.split(r'(?m)^      - ', workflow)[1:]
                   if 'uses: actions/upload-artifact@' in step]
        self.assertEqual(len(uploads), 3)
        for upload in uploads:
            self.assertIn('overwrite: true', upload)
            self.assertIn('retention-days: 1', upload)
            self.assertIn('${{ github.sha }}', upload)

    def test_full_journey_lane_never_changes_the_existing_native_job(self):
        workflow = (ROOT / '.github/workflows/native-compile.yml').read_text()
        self.assertIn('  analytics-journeys:\n', workflow)
        self.assertIn('NATIVE_ANALYTICS_JOURNEYS: "1"', workflow)
        self.assertNotIn('ios-device', workflow.split('  compile:\n')[0])
        self.assertNotIn('secrets:', workflow.split('  compile:\n')[0])
        # This digest is the immutable existing native/reminder job at b9e9699.
        import hashlib
        self.assertEqual(hashlib.sha256(workflow.split('  compile:\n', 1)[1].encode()).hexdigest(),
                         'b17c57655c0273c868e2729ecb7ac3a488e43232cf5428a6b44feb4161c39c35')


if __name__ == '__main__':
    unittest.main()
