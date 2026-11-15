"""Fresh loopback-only process/CLI rehearsal; never accepts a database path."""
from contextlib import ExitStack, closing
import hashlib
import http.client
import json
import os
from pathlib import Path
import secrets
import socket
import sqlite3
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / 'mobile/tests'))
import test_native_installed as native
from analytics_fixture import Fixture
from e2e_server import fixture_email
from test_sqlite_upgrade import Client


def scalar(path, query, parameters=()):
    with closing(sqlite3.connect(path.as_uri() + '?mode=ro', uri=True)) as db:
        return db.execute(query, parameters).fetchone()[0]


def source_identity():
    names = subprocess.check_output(['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard'], cwd=ROOT).split(b'\0')
    digest = hashlib.sha256()
    for name in sorted(set(names) - {b''}):
        path = ROOT / os.fsdecode(name)
        digest.update(name + b'\0')
        if path.is_file():
            digest.update(hashlib.sha256(path.read_bytes()).digest())
        else:
            digest.update(b'absent')
    return digest.hexdigest()


def main():
    os.umask(0o077)
    source_tree = source_identity()
    # Preserve only build-cache controls; app credentials are not inherited by
    # the native fixture. Its database, inbox, proof keys and runtime are fresh.
    for key in ('GOCACHE', 'GOMODCACHE', 'GOTMPDIR'):
        if os.environ.get(key):
            native.ENV[key] = os.environ[key]
    native.ENV['GOTOOLCHAIN'] = 'local'
    checks = []
    with tempfile.TemporaryDirectory(prefix='mychef-installed-') as directory, ExitStack() as stack:
        state = Path(directory).resolve()
        config = native.analytics_fixture_configuration(state, False)
        overlay = state / 'go-overlay.json'
        overlay.write_text(json.dumps({'Replace': native.analytics_fixture_overlays(state)}))
        for binary in ('mychef', 'mychef-migrate', 'mychef-analytics'):
            native.command('go', 'build', '-overlay', str(overlay), '-trimpath', '-o', str(state / binary), './cmd/' + binary, timeout=450)
        database = state / 'app.db'
        email, capability = fixture_email(state)
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            port = listener.getsockname()[1]
        origin = 'http://127.0.0.1:' + str(port)
        env = dict(native.ENV, **email, MYCHEF_ADDR='127.0.0.1:' + str(port), MYCHEF_ORIGIN=origin,
                   MYCHEF_DB=str(database), MYCHEF_LIFECYCLE_DB=str(database) + '.lifecycle',
                   MYCHEF_DEV='1', MYCHEF_E2E='1', MYCHEF_PROVIDER='fixture',
                   MYCHEF_FEEDBACK_TO='operator@example.test', MYCHEF_ANALYTICS_CONFIG=str(config))
        native.command(str(state / 'mychef-migrate'), 'up', '--db', str(database))
        native.command(str(state / 'mychef-analytics'), 'live', 'init', '--app', str(database), '--config', str(config))
        log = stack.enter_context((state / 'server.log').open('w'))
        process = None
        def start():
            nonlocal process
            if process is not None:
                native.terminate(process)
            process = subprocess.Popen([str(state / 'mychef')], cwd=ROOT, env=env, stdout=log, stderr=log)
            def ready():
                if process.poll() is not None:
                    raise RuntimeError('Disposable rehearsal server exited')
                try:
                    with closing(http.client.HTTPConnection('127.0.0.1', port, timeout=2)) as connection:
                        connection.request('GET', '/healthz')
                        return connection.getresponse().status == 200
                except OSError:
                    return False
            native.wait_for(ready, 'owned rehearsal process', 30)
        stack.callback(lambda: native.terminate(process) if process is not None else None)
        controls = {'restart': start}
        start()
        class LocalFixture(Fixture):
            # This rehearsal owns a fresh ephemeral listener; native CI retains
            # its fixed guarded ports and never uses this adapter.
            def call(self, method, route, data=None, headers=None):
                with closing(http.client.HTTPConnection('127.0.0.1', port, timeout=20)) as connection:
                    connection.request(method, '/api/v1/' + route, None if data is None else json.dumps(data),
                                       {'Content-Type': 'application/json', 'Origin': origin, **(headers or self.headers or {})})
                    response = connection.getresponse()
                    body = response.read(32 * 1024 * 1024 + 1)
                    if len(body) > 32 * 1024 * 1024:
                        raise RuntimeError('Rehearsal response exceeded bound')
                    return response.status, body, response.getheader('Set-Cookie')
        actor = LocalFixture(state, start)
        client = Client(origin)
        client.call('GET', '/api/v1/session')
        account = {'email': 'rehearsal@example.test', 'password': 'GardenRehearsal' + secrets.token_hex(12)}
        client.register(dict(firstName='Rehearsal', lastName='Fixture', username='rehearsal', email=account['email']), account['password'], capability)
        actor.account = account
        actor.sign_in()
        initial = json.loads(config.read_text())
        assert initial['collection']['Enabled'] is False
        assert actor.operate({'operation': 'counts'})['delivered'] == 0
        checks.append('explicit migration and disabled initialization')

        def cli(operation, *arguments, app=database, accepted=True):
            result = subprocess.run([str(state / 'mychef-analytics'), 'live', operation,
                                     '--app', str(app), '--config', str(config), *map(str, arguments)],
                                    cwd=ROOT, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60)
            if (result.returncode == 0) != accepted:
                raise RuntimeError('Disposable analytics CLI result unexpected: ' + operation)
            return result.stdout

        # A fictional assessment applies only to these disposable fixtures.
        actor.operate({'operation': 'approved'})
        actor.operate({'operation': 'other-session-choice', 'enabled': True})
        preference = actor.operate({'operation': 'preference'})
        status, body, _ = actor.call('GET', 'profile')
        assert status == 200
        profile = json.loads(body)
        update = {key: profile[key] for key in ('username', 'firstName', 'lastName', 'birthdate', 'sex', 'preferences')}
        canary = 'REHEARSAL_PRIVATE_7391'
        update['preferences']['dislikes'] = [canary]
        headers = {**actor.headers, 'X-MyChef-Analytics-Revision': str(preference['revision'])}
        assert actor.call('PUT', 'profile', update, headers)[0] == 200
        assert actor.operate({'operation': 'counts'})['queued'] == 1
        actor.worker('tick')
        assert actor.operate({'operation': 'counts'})['delivered'] == 1
        checks.append('real authenticated action delivered once')
        actor.worker('backup')
        root = Path(initial['root'])
        copy = Path(json.loads(cli('refresh'))['extract'])
        reporting = scalar(copy, 'SELECT reporting_unix FROM analytics_schema')
        backup = root / 'native-recovery.enc'
        deadline = scalar(Path(initial['privacyPath']), "SELECT deadline FROM copies WHERE kind='backup'")
        assert deadline == int((state / 'analytics-clock').read_text()) + 7 * 86400
        assert b'SQLite format' not in backup.read_bytes()
        for path in (copy, root / 'journal.sqlite'):
            assert all(value.encode() not in path.read_bytes() for value in (canary, account['email'], account['password']))
        checks.append('content canaries absent and encrypted backup bounded')
        app_backup = state / 'app-backup.sqlite'
        cli('backup-app', '--destination', app_backup, '--lifecycle-db', state / 'app.db.lifecycle')
        assert scalar(app_backup, 'SELECT count(*) FROM analytics_outbox') == 0

        actor.operate({'operation': 'other-session-choice', 'enabled': False})
        disabled = json.loads(config.read_text())
        disabled['collection']['Enabled'] = False
        config.write_text(json.dumps(disabled))
        controls['restart']()
        cli('maintenance')
        assert actor.operate({'operation': 'preference'})['deletionStatus'] == 'backup_pending'
        assert scalar(copy, 'SELECT count(*) FROM activity_events') == 0
        assert scalar(copy, 'SELECT reporting_unix FROM analytics_schema') == reporting
        checks.append('disabled process restart retains withdrawal and frozen date')
        restored = root / 'cli-restored.sqlite'
        cli('restore', '--copy', backup, '--destination', restored)
        assert scalar(restored, 'SELECT count(*) FROM activity_events') == 0
        assert scalar(restored, 'SELECT reporting_unix FROM analytics_schema') == reporting
        assert scalar(Path(initial['privacyPath']), "SELECT deadline FROM copies WHERE kind='backup'") == deadline
        checks.append('CLI restore applies current privacy without extending backup')

        headers = {**actor.headers, 'Idempotency-Key': secrets.token_hex(16)}
        status, _, _ = actor.call('POST', 'account/deletion', {'currentPassword': account['password'], 'confirmation': 'DELETE'}, headers)
        assert status == 200
        app_restored = state / 'restored-app.sqlite'
        cli('restore-app', '--copy', app_backup, '--lifecycle-db', state / 'app.db.lifecycle', app=app_restored)
        assert scalar(app_restored, 'SELECT count(*) FROM users WHERE email=?', (account['email'],)) == 0
        assert scalar(app_restored, 'SELECT count(*) FROM analytics_outbox') == 0
        checks.append('account deletion survives actual app restore command')
        (state / 'analytics-clock').write_text(str(deadline + 1))
        cli('maintenance')
        assert not backup.exists()
        checks.append('original backup expiry enforced after restore')
        # The inventory is retained as counts and hashes, without private paths,
        # row values, identity/subject IDs, runtime keys or proof material.
        with closing(sqlite3.connect(Path(initial['privacyPath']).as_uri() + '?mode=ro', uri=True)) as db:
            inventory = [{'kind': kind, 'state': status, 'exists': Path(path).exists(),
                          'pathSha256': hashlib.sha256(path.encode()).hexdigest()}
                         for path, kind, status in db.execute('SELECT path,kind,state FROM copies ORDER BY path')]
        assert all(item['exists'] == (item['state'] != 'deleted') for item in inventory)
        result = {'schemaVersion': 1, 'status': 'passed',
                  'sourceCommit': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip(),
                  'appSchema': scalar(app_restored, 'SELECT version FROM schema_migrations'),
                  'sourceTreeSha256': source_tree,
                  'sourceDirty': bool(subprocess.check_output(['git', 'status', '--porcelain'], cwd=ROOT)),
                  'binarySha256': hashlib.sha256((state / 'mychef').read_bytes()).hexdigest(),
                  'overlaySha256': hashlib.sha256(overlay.read_bytes()).hexdigest(),
                  'configurationSha256': hashlib.sha256(config.read_bytes()).hexdigest(),
                  'fixtureOnly': True, 'liveAnalytics': 'disabled', 'checks': checks, 'inventory': inventory,
                  'limitations': ['fictional clock/worker overlays', 'no deployed-host or provider proof',
                                  'native UI and spoken accessibility are separate checks']}
    assert source_identity() == source_tree, 'Source changed during rehearsal'
    print(json.dumps(result, sort_keys=True))


if __name__ == '__main__':
    main()
