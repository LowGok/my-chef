"""Authorization tests do not launch an emulator or impersonate a CI run."""
import copy
from contextlib import ExitStack
import json
from unittest.mock import patch
import importlib.util
from pathlib import Path
import sqlite3
import unittest

path = Path(__file__).with_name('analytics_installed.py')
spec = importlib.util.spec_from_file_location('analytics_installed_lane', path)
lane = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lane)


class AnalyticsLaneTest(unittest.TestCase):
    def setUp(self):
        self.environment = dict(GITHUB_ACTIONS='true', GITHUB_EVENT_NAME='push',
                                GITHUB_REPOSITORY='LowGok/my-chef', GITHUB_SHA='a' * 40, GITHUB_ACTOR='LowGok',
                                NATIVE_PLATFORM='android', GITHUB_REF='refs/heads/ci/analytics-installed-b9e9699')
        self.event = dict(repository=dict(id=1454481532, private=False, full_name='LowGok/my-chef'), deleted=False,
                          sender=dict(login='LowGok'), after='a' * 40,
                          ref='refs/heads/ci/analytics-installed-b9e9699')

    def test_ios_preserves_one_fresh_bootstrap_after_text_setup_before_xctest(self):
        class StopJourney(Exception):
            pass
        for ready in (True, False):
            stages = []
            def fixture(state, _stack, **options):
                (state / 'analytics-config.json').write_text('{}')
                options['controls']['restart'] = lambda: None
                return 'fictional-ca', [], []
            def compile_probe(*_):
                stages.append('compile')
                return Path('owned.xctestrun')
            def ios(state, _stack, _ca, _artifact, **options):
                self.assertIs(options.get('headless'), True)
                options['before_boot']('owned-phone')
                stages.append('boot')
                return {}, lambda: stages.append('extra-launch'), lambda: stages.append('stop'), lambda: None
            def command(*arguments, **_):
                if arguments[2:5] == ('list', 'devices', 'booted'):
                    return json.dumps({'devices': {'runtime': [
                        {'name': 'MyChef fixture', 'udid': 'owned-phone'}]}})
                if arguments[2] == 'ui':
                    stages.append('text-size')
                    return ''
                return '/fictional/owned/container'
            def bootstrap(*_):
                stages.append('bootstrap')
                if not ready:
                    raise StopJourney()
            def run(*_, **options):
                options['before_test']()
                stages.append('xctest')
                raise StopJourney()
            with self.subTest(ready=ready), ExitStack() as mocks:
                mocks.enter_context(patch.object(lane.native, 'PLATFORM', 'ios-simulator'))
                mocks.enter_context(patch.object(lane.native, 'fixture', side_effect=fixture))
                actor = mocks.enter_context(patch.object(lane, 'Fixture')).return_value
                actor.operate.return_value = {}
                actor.serve.return_value = {}
                mocks.enter_context(patch.object(lane.native, 'ios', side_effect=ios))
                mocks.enter_context(patch.object(lane.native, 'command', side_effect=command))
                mocks.enter_context(patch.object(lane.native, 'verify_stage',
                    side_effect=lambda *_, **options: options['launch']()))
                mocks.enter_context(patch.object(lane.analytics_ios, 'prepare', side_effect=compile_probe))
                mocks.enter_context(patch.object(lane.analytics_ios, 'bootstrap', side_effect=bootstrap))
                mocks.enter_context(patch.object(lane.analytics_ios, 'run', side_effect=run))
                with self.assertRaises(StopJourney):
                    lane.full_journeys({}, Path('fictional.app.zip'))
            self.assertEqual(stages, ['compile', 'boot', 'text-size', 'bootstrap']
                             + (['xctest'] if ready else []))

    def test_exact_run_allows_only_the_two_unsigned_platforms(self):
        for platform in ('android', 'ios-simulator'):
            self.environment['NATIVE_PLATFORM'] = platform
            self.assertTrue(lane.authorized(self.event, self.environment))
        self.environment['NATIVE_PLATFORM'] = 'ios-device'
        self.assertFalse(lane.authorized(self.event, self.environment))

    def test_reminder_branch_fork_private_repo_actor_or_drift_are_refused(self):
        for key, value in (
                ('ref', 'refs/heads/feat/weekly-reminders'),
                ('sender', dict(login='another-user')),
                ('repository', dict(id=1454481532, private=True)),
                ('repository', dict(id=1, private=False)),
                ('after', 'b' * 40), ('deleted', True)):
            event = copy.deepcopy(self.event)
            event[key] = value
            self.assertFalse(lane.authorized(event, self.environment))

    def test_local_or_other_event_cannot_claim_installed_evidence(self):
        for key, value in (('GITHUB_ACTIONS', 'false'), ('GITHUB_EVENT_NAME', 'pull_request'),
                           ('GITHUB_REPOSITORY', 'other/my-chef'), ('GITHUB_ACTOR', 'another-user'), ('GITHUB_REF', 'refs/heads/main')):
            environment = dict(self.environment)
            environment[key] = value
            self.assertFalse(lane.authorized(self.event, environment))

    def test_off_controls_are_allowed_but_capture_or_enabled_authority_is_not(self):
        with sqlite3.connect(':memory:') as db:
            db.executescript("""
                CREATE TABLE analytics_outbox(id TEXT);
                CREATE TABLE analytics_preferences(enabled INTEGER,subject TEXT,epoch TEXT);
                CREATE TABLE analytics_receipts(enabled INTEGER);
                CREATE TABLE analytics_signup(enabled INTEGER);
                INSERT INTO analytics_preferences VALUES(0,'','');
                INSERT INTO analytics_receipts VALUES(0);
                INSERT INTO analytics_signup VALUES(0);
            """)
            lane.assert_disabled_storage(db)
            for table, assignment in (
                    ('analytics_preferences', "enabled=1"),
                    ('analytics_preferences', "subject='stray-subject'"),
                    ('analytics_preferences', "epoch='stray-epoch'"),
                    ('analytics_receipts', 'enabled=1'),
                    ('analytics_signup', 'enabled=1')):
                with self.subTest(table=table, assignment=assignment):
                    db.execute('SAVEPOINT test_case')
                    db.execute('UPDATE ' + table + ' SET ' + assignment)
                    with self.assertRaisesRegex(RuntimeError, 'captured data or enabled enrollment'):
                        lane.assert_disabled_storage(db)
                    db.execute('ROLLBACK TO test_case')
                    db.execute('RELEASE test_case')
            db.execute("INSERT INTO analytics_outbox VALUES('event')")
            with self.assertRaisesRegex(RuntimeError, 'captured data or enabled enrollment'):
                lane.assert_disabled_storage(db)


if __name__ == '__main__':
    unittest.main()
