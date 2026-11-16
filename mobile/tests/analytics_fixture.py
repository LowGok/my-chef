"""Private controller for disposable installed analytics tests, never an app API."""
from contextlib import closing
import hashlib
import http.client
import http.server
import io
import json
from pathlib import Path
import re
import secrets
import sqlite3
import threading
import time
import zipfile

import sys
ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'scripts'))
import test_native_installed as native
from native_fixture_account import seed


def own_export(raw, email):
    if len(raw) > 32 * 1024 * 1024:
        return False
    try:
        with zipfile.ZipFile(io.BytesIO(raw)) as archive:
            if sum(item.file_size for item in archive.infolist()) > 64 * 1024 * 1024:
                return False
            if 'analytics.json' not in archive.namelist():
                return False
            text = b'\n'.join(archive.read(item) for item in archive.infolist() if item.filename.endswith('.json'))
            return (email.encode() in text and b'analyticsdisabled@example.test' not in text
                    and b'analyticsassessed@example.test' not in text)
    except (ValueError, zipfile.BadZipFile, OSError):
        return False


class Fixture:
    def __init__(self, state, restart):
        self.state = Path(state)
        self.restart = restart
        self.sequence = 0
        self.account = None
        self.capability = json.loads((self.state / 'fixture-inbox.json').read_text())['capability']
        self.headers = None
        self.speech = None
        self.native_events = None

    def call(self, method, route, data=None, headers=None):
        with closing(http.client.HTTPConnection('127.0.0.1', 18137, timeout=20)) as c:
            c.request(method, '/api/v1/' + route, None if data is None else json.dumps(data),
                      {'Content-Type': 'application/json', 'Origin': native.ORIGIN, **(headers or self.headers or {})})
            r = c.getresponse()
            body = r.read(32 * 1024 * 1024 + 1)
            if len(body) > 32 * 1024 * 1024:
                raise RuntimeError('Fixture response too large')
            return r.status, body, r.getheader('Set-Cookie')

    def sign_in(self):
        status, raw, cookie = self.call('GET', 'session', headers={})
        value = json.loads(raw)
        if status != 200 or not cookie or not value.get('csrfToken'):
            raise RuntimeError('Fixture session unavailable')
        self.headers = {'Cookie': cookie.split(';', 1)[0], 'X-CSRF-Token': value['csrfToken']}
        status, raw, cookie = self.call('POST', 'login', self.account)
        if status != 200:
            raise RuntimeError('Fixture actor authentication failed')
        if cookie:
            self.headers['Cookie'] = cookie.split(';', 1)[0]
        # Authentication rotates the session; fetch the current CSRF value.
        status, raw, cookie = self.call('GET', 'session')
        if cookie:
            self.headers['Cookie'] = cookie.split(';', 1)[0]
        self.headers['X-CSRF-Token'] = json.loads(raw)['csrfToken']

    def worker(self, operation):
        if operation not in ('tick', 'backup', 'maintenance'):
            raise ValueError('Unknown fixture worker operation')
        self.sequence += 1
        pending = self.state / 'analytics-command.pending'
        pending.write_text(json.dumps(dict(sequence=self.sequence, operation=operation)))
        pending.replace(self.state / 'analytics-command.json')
        def complete():
            try:
                value = json.loads((self.state / 'analytics-response.json').read_text())
            except (OSError, ValueError):
                return False
            if value.get('sequence') != self.sequence:
                return False
            if value.get('passed') is not True:
                raise RuntimeError('Disposable analytics operation refused')
            return True
        native.wait_for(complete, 'disposable analytics operation', 25)

    def operate(self, value):
        if type(value) is not dict or set(value) - {'operation', 'enabled', 'recipient', 'target'}:
            raise ValueError('Invalid fixture controller request')
        op = value.get('operation')
        if op in ('speech-start', 'speech-stop', 'speech-gesture'):
            if self.speech is None or set(value) != {'operation', 'target'}:
                raise ValueError('Speech observation unavailable')
            return self.speech.operate(op, value['target'])
        if op == 'native-export':
            if self.native_events is None:
                raise ValueError('Native proxy observations unavailable')
            attempts = [status for method, kind, status in self.native_events if method == 'POST' and kind == 'account-export']
            return {'attempts': len(attempts), 'lastStatus': attempts[-1] if attempts else 0}
        if op == 'seed':
            if self.account is not None:
                raise ValueError('Fixture actor already created')
            self.account = seed(18137, native.ORIGIN, self.capability, 'analyticsinstalled')
            self.sign_in()
            return self.account
        if op == 'approved':
            if self.native_events is not None and ('GET', 'analytics-notice-disabled', 200) not in self.native_events:
                raise RuntimeError('Native disabled notice was not corroborated')
            config = self.state / 'analytics-config.json'
            document = json.loads(config.read_text())
            document['collection']['Enabled'] = True
            config.write_text(json.dumps(document, sort_keys=True))
            self.restart()
            return {'passed': True}
        if op == 'proof':
            recipient = value.get('recipient', '')
            if not re.fullmatch(r'analytics(?:disabled|assessed)@example\.test', recipient):
                raise ValueError('Expected a closed fictional signup recipient')
            proof = None
            def ready():
                nonlocal proof
                status, raw, _ = self.call('POST', 'fixture/inbox', {'recipient': recipient},
                                           {'Authorization': 'Bearer ' + self.capability})
                if status == 204:
                    return False
                if status != 200:
                    raise RuntimeError('Fixture signup inbox refused')
                match = re.search(r'/app/signup#([A-Za-z0-9_-]{43})', json.loads(raw).get('text', ''))
                proof = match.group(1) if match else None
                return proof is not None
            native.wait_for(ready, 'disposable signup message', 20)
            return {'url': native.ORIGIN + '/app/signup#' + proof}
        if op == 'profile-input':
            expected = {'initial': ['Vegetarian meals'], 'updated': ['Mediterranean']}
            if set(value) != {'operation', 'target'} or type(value.get('target')) is not str or value['target'] not in expected:
                raise ValueError('Closed native profile fixture target required')
            status, raw, _ = self.call('GET', 'profile')
            if status != 200:
                raise RuntimeError('Fixture profile content unavailable')
            profile = json.loads(raw)
            preferences = profile.get('preferences') if isinstance(profile, dict) else None
            diet = preferences.get('diet') if isinstance(preferences, dict) else None
            passed = diet == expected[value['target']]
            if not passed:
                items = [item for item in diet if isinstance(item, str)] if isinstance(diet, list) else []
                facts = dict(target=value['target'],
                             profileSaves=sum(method == 'PUT' and kind == 'profile'
                                              for method, kind, _ in (getattr(self, 'native_events', None) or [])),
                             initialPresent=any('Vegetarian meals' in item for item in items),
                             updatedPresent=any('Mediterranean' in item for item in items), empty=diet == [])
                if facts != getattr(self, 'last_profile_input_diagnostic', None):
                    print('Native profile input: ' + json.dumps(facts, sort_keys=True), flush=True)
                    self.last_profile_input_diagnostic = facts
            return {'passed': passed}
        if op == 'preference':
            status, raw, _ = self.call('GET', 'analytics/preferences')
            if status != 200:
                raise RuntimeError('Fixture preference unavailable')
            return json.loads(raw)
        if op == 'other-session-choice':
            enabled = value.get('enabled')
            if type(enabled) is not bool:
                raise ValueError('Fixture choice must be boolean')
            preference = self.operate({'operation': 'preference'})
            status, raw, _ = self.call('PUT', 'analytics/preferences', dict(enabled=enabled,
                       noticeVersion=preference['noticeVersion'], expectedRevision=preference['revision']))
            if status != 200:
                raise RuntimeError('Independent fixture choice failed')
            return {'passed': True}
        if op in ('offline', 'online'):
            path = self.state / 'native-network-offline'
            if op == 'offline':
                path.touch()
            else:
                path.unlink(missing_ok=True)
            return {'passed': True}
        if op in ('tick', 'backup', 'maintenance'):
            self.worker(op)
            return {'passed': True}
        if op == 'expire-backup':
            clock = self.state / 'analytics-clock'
            clock.write_text(str(int(clock.read_text()) + 7 * 86400 + 1))
            self.worker('maintenance')
            return {'passed': not (self.state / 'analytics-data/native-recovery.enc').exists()}
        if op == 'verify-native-export':
            raw = (self.state / 'native-export.zip').read_bytes()
            if not own_export(raw, self.account['email']):
                raise RuntimeError('Native export ownership check failed')
            return {'passed': True}
        if op == 'foreign-export':
            # This endpoint accepts only the signed-in actor. Supplying another
            # account selector must be refused by the actual strict decoder.
            with sqlite3.connect('file:' + str(self.state / 'app.db') + '?mode=ro', uri=True) as db:
                foreign = db.execute('SELECT user_id FROM users WHERE email=?', ('analyticsassessed@example.test',)).fetchone()
            if not foreign:
                raise RuntimeError('Expected the other completed fictional account')
            status, _, _ = self.call('POST', 'account/export', dict(currentPassword=self.account['password'], accountId=str(foreign[0])))
            if status != 400:
                raise RuntimeError('Foreign export selector was not refused')
            return {'passed': True}
        if op == 'counts':
            with sqlite3.connect('file:' + str(self.state / 'app.db') + '?mode=ro', uri=True) as db:
                queued = db.execute('SELECT count(*) FROM analytics_outbox').fetchone()[0]
                enabled = db.execute('SELECT count(*) FROM analytics_preferences WHERE enabled<>0').fetchone()[0]
                forbidden = db.execute("SELECT count(*) FROM analytics_outbox WHERE name NOT IN ('preferences_saved','meal_plan_saved','shopping_list_edited','favorite_saved','feedback_submitted','personal_recipe_saved','recipe_import_saved')").fetchone()[0]
                shopping = sum(item.get('name') == 'Analytics queued oats' for row in db.execute('SELECT body FROM shopping_lists')
                               for item in (json.loads(row[0]).get('items') or []))
            with sqlite3.connect('file:' + str(self.state / 'analytics-data/journal.sqlite') + '?mode=ro', uri=True) as db:
                delivered = db.execute('SELECT count(*) FROM facts').fetchone()[0]
            return dict(queued=queued, delivered=delivered, enabled=enabled, forbidden=forbidden, queuedProductItems=shopping)
        raise ValueError('Unknown fixture controller operation')

    def serve(self, stack):
        owner = self
        token = secrets.token_urlsafe(32)

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def do_POST(self):
                size = int(self.headers.get('Content-Length', '0'))
                if (self.path != '/control' or self.headers.get('Authorization') != 'Bearer ' + token
                        or not 0 < size <= 2048):
                    self.send_error(403)
                    return
                request = None
                try:
                    request = json.loads(self.rfile.read(size))
                    result = owner.operate(request)
                    if request.get('operation') == 'preference':
                        result = {'enabled': result['enabled']}
                    body = json.dumps(result).encode()
                    status = 200
                except (ValueError, RuntimeError, OSError) as error:
                    operation = request.get('operation') if type(request) is dict else None
                    closed = {'proof', 'approved', 'profile-input', 'speech-start', 'speech-stop', 'speech-gesture', 'preference', 'counts',
                              'tick', 'backup', 'maintenance', 'other-session-choice', 'offline', 'online',
                              'foreign-export', 'verify-native-export'}
                    operation = operation if isinstance(operation, str) and operation in closed else 'unknown'
                    reason = {'Timed out waiting for disposable signup message': 'proof-timeout',
                              'Fixture signup inbox refused': 'proof-inbox',
                              'Speaker capture is silent or outside bounds': 'speech-audio',
                              'Speech capture timeline mismatch': 'speech-timeline',
                              'Offline model changed': 'speech-model',
                              'Expected one emulator touch device': 'touch-device',
                              'Emulator console authentication refused': 'touch-console-auth',
                              'Emulator hardware command refused': 'touch-console-command',
                              'Owned emulator display unavailable': 'touch-display',
                              'Emulator touchscreen observer unavailable': 'touch-observer',
                              'Emulator touchscreen input was not observed': 'touch-input'}.get(str(error), 'refused')
                    print('Analytics fixture refusal: operation=' + operation + ' reason=' + reason, flush=True)
                    status, body = 409, b'{"error":"fixture_operation_refused"}'
                self.send_response(status)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(body)))
                self.end_headers()
                self.wfile.write(body)

        server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        stack.callback(server.server_close)
        stack.callback(server.shutdown)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        control = dict(url=f'http://127.0.0.1:{server.server_address[1]}/control', token=token)
        path = self.state / 'analytics-control.json'
        path.write_text(json.dumps(control))
        path.chmod(0o600)
        return control
