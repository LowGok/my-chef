"""Guarded installed verification with explicit limits for incomplete proof."""
from contextlib import ExitStack
from collections.abc import Mapping
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
sys.path.insert(0, str(Path(__file__).resolve().parent))
import test_native_installed as native
from native_fixture_account import seed
from native_android_probe import build
from analytics_fixture import Fixture, own_export
import analytics_ios


def authorized(event, environment):
    if not isinstance(event, dict) or not isinstance(environment, Mapping):
        return False
    repository, sender = event.get('repository'), event.get('sender')
    revision = environment.get('GITHUB_SHA')
    if not isinstance(repository, dict) or not isinstance(sender, dict):
        return False
    return (isinstance(revision, str) and re.fullmatch(r'[a-f0-9]{40}', revision) is not None
            and environment.get('GITHUB_ACTIONS') == 'true'
            and environment.get('GITHUB_EVENT_NAME') == 'push'
            and environment.get('GITHUB_REPOSITORY') == 'LowGok/my-chef'
            and environment.get('GITHUB_ACTOR') == 'LowGok'
            and repository.get('id') == 1454481532
            and repository.get('full_name') == 'LowGok/my-chef'
            and repository.get('private') is False
            and sender.get('login') == 'LowGok'
            and event.get('deleted') is False
            and event.get('ref') == 'refs/heads/ci/analytics-installed-b9e9699'
            and environment.get('GITHUB_REF') == event['ref']
            and event.get('after') == revision
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


def install_android_probe(state, artifact):
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
    adb_path = str(sdk / 'platform-tools/adb')
    native.command(adb_path, '-s', 'emulator-5554', 'install', str(probe))


def full_journeys(receipt, artifact):
    with tempfile.TemporaryDirectory(prefix='mychef-installed-') as directory, ExitStack() as stack:
        state = Path(directory).resolve()
        controls = {}
        ca, events, rejected = native.fixture(state, stack, analytics='disabled', controls=controls)
        fixture = Fixture(state, controls['restart'])
        fixture.native_events = events
        account = fixture.operate({'operation': 'seed'})
        control = fixture.serve(stack)
        config_digest = hashlib.sha256((state / 'analytics-config.json').read_bytes()).hexdigest()
        speech = None
        if native.PLATFORM == 'android':
            if os.environ.get('NATIVE_ANALYTICS_SPEECH') == '1':
                from analytics_speech import Speech
                native.ENV['NATIVE_ANALYTICS_SPEECH'] = '1'
                speech = Speech(state, stack, native)
                fixture.speech = speech
            metadata, launch, stop, diagnostic = native.android(state, stack, ca, artifact)
            if speech is not None:
                from analytics_touch import focus_display
                focus_display(native)
            adb_path = str(Path(native.ENV['ANDROID_HOME']) / 'platform-tools/adb')
            # Establish a measured normal viewport before the 320-CSS-pixel
            # large-text case, independent of the runner's AVD defaults.
            native.command(adb_path, '-s', 'emulator-5554', 'shell', 'wm', 'size', '1080x1920')
            native.command(adb_path, '-s', 'emulator-5554', 'shell', 'wm', 'density', '432')
            native.command(adb_path, '-s', 'emulator-5554', 'shell', 'settings', 'put', 'system', 'font_scale', '1.5' if speech is not None else '1.0')
        else:
            metadata, launch, stop, diagnostic = native.ios(state, stack, ca, artifact)
        native.verify_stage(lambda: ('POST', 'session', 201) in events and ('GET', 'session', 200) in events,
                            'native analytics fixture session', events, rejected, diagnostic, launch=launch)
        if native.PLATFORM == 'android':
            install_android_probe(state, artifact)
            (state / 'native-android-account.json').write_text(json.dumps(account))
            for key in ('GITHUB_ACTIONS', 'GITHUB_REPOSITORY', 'NATIVE_PLATFORM'):
                native.ENV[key] = os.environ[key]
            try:
                native.command('bun', 'scripts/native-android-journeys.ts', str(state), 'analytics', timeout=900)
            except RuntimeError:
                print('Native Android account API results: ' + native.account_api_summary(events), flush=True)
                raise
            startup_recovery = re.search(r'INSTRUMENTATION_STATUS: probeSystemUiRecovery=(closed|unavailable)\b',
                                         (state / 'probe-private.log').read_text())
            metadata['instrumentationStartupRecovery'] = startup_recovery[1] if startup_recovery else 'not needed'
            checks = json.loads((state / 'native-android-journeys-analytics.json').read_text())['checks']
            if speech is not None:
                from analytics_speech import TARGETS
                if speech.passed != set(TARGETS):
                    raise RuntimeError('Incomplete actual TalkBack speech evidence')
                checks['accessibility'].update(platformScreenReader=True, spokenStatesAndErrors=True, focusOrder=True)
                metadata['speech'] = dict(method='stock TalkBack speaker output, emulator hardware gestures and pinned offline recognition',
                                         targets=sorted(speech.passed), attempts=speech.attempts,
                                         rawAudioRetained=False, externalRecognition=False)
        else:
            devices = json.loads(native.command('xcrun', 'simctl', 'list', 'devices', 'booted', '-j'))['devices']
            identifiers = [d['udid'] for group in devices.values() for d in group if d.get('name') == 'MyChef fixture']
            if len(identifiers) != 1:
                raise RuntimeError('Expected one run-owned simulator')
            identifier = identifiers[0]
            native.command('xcrun', 'simctl', 'ui', identifier, 'content_size', 'accessibility-medium')
            container = Path(native.command('xcrun', 'simctl', 'get_app_container', identifier, native.APP, 'data'))
            stop()
            try:
                checks = analytics_ios.run(state, control, account, identifier, container)
            except RuntimeError:
                # Fixed route groups and HTTP statuses; no headers/bodies/IDs.
                print('Native iOS account API results: ' + native.account_api_summary(events), flush=True)
                print('Native iOS API results: ' + ', '.join(method + '/' + kind + '/' + str(status)
                    for method, kind, status in events[-16:]), flush=True)
                raise
            scale = json.loads((container / 'tmp/analytics-text-result.json').read_text())
            if set(scale) != {'root', 'label', 'notice', 'fits', 'osTextEnlarged'} or not all(value is True for value in scale.values()):
                raise RuntimeError('Installed iOS text scale did not propagate')
            checks['accessibility'].update(enlargedOSText=True, textScalePropagated=True, noClipping=True)
            if (container / 'Library/Application Support/MyChefNative/shopping.v1').exists():
                raise RuntimeError('Deleted account retained native shopping snapshot')
            checks['lifecycle']['relaunchSessionCacheCleared'] = True
            # The fresh run-owned simulator contains no owner's Files data.
            # Inspect the OS-saved copy, excluding the app's transient staging.
            copies = [path for path in container.parents[3].rglob('mychef-data-*.zip')
                      if re.fullmatch(r'mychef-data-[a-f0-9]{32}\.zip', path.name)
                      and not path.is_relative_to(container / 'Library/Caches')]
            if len(copies) != 1 or not own_export(copies[0].read_bytes(), account['email']):
                raise RuntimeError('OS-saved iOS export ownership verification failed')
            checks['lifecycle']['ownExportOnly'] = True
            # A separate fresh supported small iPhone uses the same verified
            # archive, API and OS text setting; it does not replace the first
            # phone's authenticated journey or claim screen-reader speech.
            small = state / 'small-phone'
            small.mkdir(mode=0o700)
            small_metadata, _, _, _ = native.ios(small, stack, ca, artifact,
                model='iPhone SE (3rd generation)', fixture_name='MyChef analytics small fixture')
            devices = json.loads(native.command('xcrun', 'simctl', 'list', 'devices', 'booted', '-j'))['devices']
            identifiers = [d['udid'] for group in devices.values() for d in group if d.get('name') == 'MyChef analytics small fixture']
            if len(identifiers) != 1:
                raise RuntimeError('Expected one owned small-phone simulator')
            small_id = identifiers[0]
            native.command('xcrun', 'simctl', 'ui', small_id, 'content_size', 'accessibility-medium')
            small_container = Path(native.command('xcrun', 'simctl', 'get_app_container', small_id, native.APP, 'data'))
            small_checks = analytics_ios.run(small, control, account, small_id, small_container, layout_only=True)
            small_scale = json.loads((small_container / 'tmp/analytics-text-result.json').read_text())
            if (set(small_scale) != {'root', 'label', 'notice', 'fits', 'osTextEnlarged'}
                    or not all(value is True for value in small_scale.values())
                    or small_checks['accessibility']['phoneWidths'] is not True
                    or small_checks['accessibility']['keyboardUnobstructed'] is not True):
                raise RuntimeError('Small supported iPhone layout did not pass')
            checks['accessibility']['phoneWidths'] = True
            metadata['smallPhone'] = small_metadata
        if not all(('GET', kind, 200) in events for kind in ('analytics-notice-disabled', 'analytics-notice-assessed')):
            raise RuntimeError('Both native signup notices must be corroborated by actual responses')
        boundary = json.loads((state / 'analytics-client-boundary.json').read_text())
        if boundary['observed'] < 1 or boundary['passed'] is not True:
            raise RuntimeError('Native response boundary included analytics identifiers')
        checks['participation']['noClientAnalyticsIdentifiers'] = True
        fixture.worker('maintenance')
        if fixture.operate({'operation': 'expire-backup'})['passed'] is not True:
            raise RuntimeError('Original native fixture backup expiry was not enforced')
        binding = dict(sourceCommit=receipt['candidate']['sourceCommit'],
                       apiSha256=hashlib.sha256((ROOT / 'api/openapi.yaml').read_bytes()).hexdigest(),
                       bundleSha256=receipt['candidate']['bundleSha256'], artifactSha256=receipt['artifactSha256'],
                       fixtureConfigSha256=hashlib.sha256(json.dumps(dict(
                           disabled=config_digest,
                           approved=hashlib.sha256((state / 'analytics-config.json').read_bytes()).hexdigest(),
                           overlays={name: hashlib.sha256((state / name).read_bytes()).hexdigest() for name in (
                               'analytics-runtime-fixture.go', 'analytics-worker-fixture.go')},
                       ), sort_keys=True).encode()).hexdigest(), contractVersion=1)
        probe = dict(schemaVersion=1, platform=native.PLATFORM, binding=binding, checks=checks)
        complete = native.validate_analytics_probe(probe, native.PLATFORM, binding)
        result = dict(probe=probe, device=metadata, fullJourneys=complete,
                      apiExecutableSha256=hashlib.sha256((state / 'mychef').read_bytes()).hexdigest(),
                      fixture='fictional isolated accounts and assessment; controlled clock and worker',
                      distribution='not authorized', liveCollection='disabled',
                      limitations=[] if complete else ['Incomplete observations; no full native receipt issued'])
        (native.ARTIFACTS / 'analytics-installed-observations.json').write_text(json.dumps(result, indent=2) + '\n')
        # The separate full receipt exists only after all six journeys, including
        # observed speech, pass. A green functional run is not that conclusion.
        if complete:
            (native.ARTIFACTS / 'analytics-installed-full.json').write_text(json.dumps(result, indent=2) + '\n')
        print('Installed analytics observations retained; full receipt ' + ('complete' if complete else 'withheld'))
        if native.PLATFORM == 'android' and speech is not None and not complete:
            raise RuntimeError('Android spoken verification requires every native observation')


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
    if os.environ.get('NATIVE_ANALYTICS_JOURNEYS') == '1':
        full_journeys(receipt, artifact)
        return
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
