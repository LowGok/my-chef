"""Exercise the actual analytics authorization predicate without opening a device."""
import ast
import copy
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parent.parent


def authorization():
    source = ast.parse((ROOT / 'mobile/tests/analytics_installed.py').read_text())
    function = next(node for node in source.body if isinstance(node, ast.FunctionDef) and node.name == 'authorized')
    scope = {'re': re}
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
