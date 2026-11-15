"""Authorization tests do not launch an emulator or impersonate a CI run."""
import copy
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
                                GITHUB_REPOSITORY='LowGok/my-chef', GITHUB_SHA='a' * 40,
                                NATIVE_PLATFORM='android')
        self.event = dict(repository=dict(id=1454481532, private=False),
                          sender=dict(login='LowGok'), after='a' * 40,
                          ref='refs/heads/ci/analytics-installed-b9e9699')

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
                ('after', 'b' * 40)):
            event = copy.deepcopy(self.event)
            event[key] = value
            self.assertFalse(lane.authorized(event, self.environment))

    def test_local_or_other_event_cannot_claim_installed_evidence(self):
        for key, value in (('GITHUB_ACTIONS', 'false'), ('GITHUB_EVENT_NAME', 'pull_request'),
                           ('GITHUB_REPOSITORY', 'other/my-chef')):
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
