"""Limited installed disabled-state evidence; full analytics journeys stay pending."""
from contextlib import ExitStack
import hashlib
import json
import os
from pathlib import Path
import re
import sqlite3
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'scripts'))
import test_native_installed as native
from native_fixture_account import seed
from native_android_probe import build


def authorized(event, environment):
    return (environment.get('GITHUB_ACTIONS') == 'true'
            and environment.get('GITHUB_EVENT_NAME') == 'push'
            and environment.get('GITHUB_REPOSITORY') == 'LowGok/my-chef'
            and event.get('repository', {}).get('id') == 1454481532
            and event.get('repository', {}).get('private') is False
            and event.get('sender', {}).get('login') == 'LowGok'
            and event.get('ref') == 'refs/heads/ci/analytics-installed-b9e9699'
            and event.get('after') == environment.get('GITHUB_SHA')
            and environment.get('NATIVE_PLATFORM') in ('android', 'ios-simulator'))


def assert_disabled_storage(db):
    # Signup retains an Off preference/receipt even with no analytics runtime.
    # Those control records prevent accidental enrollment; they are not events.
    checks = (
        'SELECT count(*) FROM analytics_outbox',
        "SELECT count(*) FROM analytics_preferences WHERE enabled<>0 OR subject<>'' OR epoch<>''",
        'SELECT count(*) FROM analytics_receipts WHERE enabled<>0',
        'SELECT count(*) FROM analytics_signup WHERE enabled<>0',
    )
    if any(db.execute(query).fetchone()[0] for query in checks):
        raise RuntimeError('Disabled fixture captured data or enabled enrollment')


def main():
    os.umask(0o077)
    event = json.loads(Path(os.environ['GITHUB_EVENT_PATH']).read_text())
    if not authorized(event, os.environ):
        raise SystemExit('Analytics installed verification authorization refused')
    if native.PLATFORM == 'android':
        java = os.environ['JAVA_HOME_21_X64']
        native.ENV.update(JAVA_HOME=java, PATH=java + '/bin:' + native.ENV['PATH'])
    receipt = json.loads((native.ARTIFACTS / 'analytics-compilation.json').read_text())
    artifact = native.ARTIFACTS / receipt['artifact']
    if (receipt['candidate']['sourceCommit'] != os.environ['GITHUB_SHA']
            or receipt['platform'] != native.PLATFORM
            or hashlib.sha256(artifact.read_bytes()).hexdigest() != receipt['artifactSha256']):
        raise SystemExit('Candidate/artifact identity mismatch')
    with tempfile.TemporaryDirectory(prefix='mychef-installed-') as directory, ExitStack() as stack:
        state = Path(directory).resolve()
        ca, events, rejected = native.fixture(state, stack, journeys=False)
        if native.PLATFORM == 'android':
            metadata, launch, stop, diagnostic = native.android(state, stack, ca, artifact)
            adb_path = str(Path(native.ENV['ANDROID_HOME']) / 'platform-tools/adb')
            native.command(adb_path, '-s', 'emulator-5554', 'shell', 'settings', 'put', 'system', 'font_scale', '1.5')
        else:
            metadata, launch, stop, diagnostic = native.ios(state, stack, ca, artifact)
        native.verify_stage(lambda: ('POST', 'session', 201) in events and ('GET', 'session', 200) in events,
                            'native analytics fixture session', events, rejected, diagnostic, launch=launch)
        if native.PLATFORM == 'android':
            sdk = Path(native.ENV['ANDROID_HOME'])
            signer = (native.GENERATED / 'analytics-debug-keystore-path').read_text().strip()
            if not Path(signer).is_file():
                raise RuntimeError('Exact debug signer unavailable')
            def probe_command(*args, **kwargs):
                args = list(args)
                if Path(args[0]).name == 'apksigner' and args[1] == 'sign':
                    args[args.index('--ks') + 1] = signer
                return native.command(*args, **kwargs)
            probe = build(state, sdk, probe_command)
            tool = str(sdk / 'build-tools/36.0.0/apksigner')
            def fingerprint(path):
                value = native.command(tool, 'verify', '--print-certs', str(path))
                match = re.search(r'certificate SHA-256 digest: ([a-f0-9]{64})', value)
                if not match:
                    raise RuntimeError('Debug certificate identity unavailable')
                return match.group(1)
            if fingerprint(probe) != fingerprint(artifact):
                raise RuntimeError('Probe and app debug signatures differ')
            native.command(adb_path, '-s', 'emulator-5554', 'install', str(probe))
            account = seed(18137, native.ORIGIN, json.loads((state / 'fixture-inbox.json').read_text())['capability'], 'analyticsnativefixture')
            (state / 'native-android-account.json').write_text(json.dumps(account))
            for key in ('GITHUB_ACTIONS', 'GITHUB_REPOSITORY', 'NATIVE_PLATFORM'):
                native.ENV[key] = os.environ[key]
            native.command('bun', 'scripts/native-android-journeys.ts', str(state), 'analytics-disabled', timeout=150)
            result = json.loads((state / 'native-android-journeys-analytics-disabled.json').read_text())
            if result.get('passed') != ['disabled analytics signup control in installed Android']:
                raise RuntimeError('Disabled Android analytics UI receipt incomplete')
        else:
            stop()
            devices = json.loads(native.command('xcrun', 'simctl', 'list', 'devices', 'booted', '-j'))['devices']
            identifiers = [d['udid'] for group in devices.values() for d in group if d.get('name') == 'MyChef fixture']
            if len(identifiers) != 1:
                raise RuntimeError('Expected one run-owned simulator')
            identifier = identifiers[0]
            native.command('xcrun', 'simctl', 'ui', identifier, 'content_size', 'accessibility-medium')
            container = Path(native.command('xcrun', 'simctl', 'get_app_container', identifier, native.APP, 'data'))
            output = container / 'tmp/analytics-disabled-result.json'
            output.unlink(missing_ok=True)
            native.command('xcrun', 'simctl', 'launch', identifier, native.APP, '--mychef-analytics-disabled-fixture')
            def passed():
                try:
                    result = json.loads(output.read_text())
                except (OSError, ValueError):
                    return False
                return result == dict(disabled=True, unchecked=True, labelled=True, fits=True)
            native.wait_for(passed, 'installed iOS disabled analytics UI', 90)
        native.wait_for(lambda: ('GET', 'analytics-notice-disabled', 200) in events,
                        'real disabled analytics notice through native transport', 20)
        with sqlite3.connect(state / 'app.db') as db:
            assert_disabled_storage(db)
        result = dict(platform=native.PLATFORM, source=receipt['candidate']['sourceCommit'],
                      artifactSha256=receipt['artifactSha256'], device=metadata,
                      checks=['verified native HTTPS', 'disabled unchecked labelled signup control',
                              'layout after requesting OS text size', 'real unavailable notice', 'zero capture and enabled enrollment'],
                      fullJourneys='not verified', spokenAccessibility='not verified',
                      textScalingPropagation='not verified',
                      distribution='not authorized', productionCollection='disabled')
        (native.ARTIFACTS / 'analytics-installed-smoke.json').write_text(json.dumps(result, indent=2) + '\n')
        print('Installed disabled analytics smoke passed; full journeys and speech remain unverified')


if __name__ == '__main__':
    main()
