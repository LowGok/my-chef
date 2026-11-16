"""Supplement the same unsigned artifact with actual iOS 27 VoiceOver output."""
from contextlib import ExitStack
import copy
import hashlib
import json
import os
from pathlib import Path
import tempfile

from analytics_installed import authorized
import analytics_ios
from analytics_fixture import Fixture
from analytics_artifact import verify_archive, ENTITLEMENTS
import plistlib
import test_native_installed as native

ROOT = Path(__file__).resolve().parents[2]
SPEECH = {'platformScreenReader', 'spokenStatesAndErrors', 'focusOrder'}


def combine(primary, supplement, expected_primary, expected_supplement):
    """Closed evidence merge; JSON alone does not authenticate either runner."""
    if (type(primary) is not dict or type(supplement) is not dict
            or set(supplement) != {'schemaVersion', 'binding', 'checks'}
            or type(supplement['schemaVersion']) is not int or supplement['schemaVersion'] != 1
            or supplement['binding'] != expected_supplement or type(supplement['checks']) is not dict
            or set(supplement['checks']) != SPEECH or not all(value is True for value in supplement['checks'].values())
            or type(expected_primary) is not dict or type(expected_supplement) is not dict):
        return None
    keys = {'sourceCommit', 'apiSha256', 'bundleSha256', 'artifactSha256', 'fixtureConfigSha256', 'contractVersion'}
    if set(expected_primary) != keys or set(expected_supplement) != keys:
        return None
    if any(expected_primary[key] != expected_supplement[key] for key in keys - {'fixtureConfigSha256'}):
        return None
    try:
        if any(type(primary['checks']['accessibility'][name]) is not bool for name in SPEECH):
            return None
        merged = copy.deepcopy(primary)
        for name in SPEECH:
            merged['checks']['accessibility'][name] = True
    except (KeyError, TypeError):
        return None
    if not native.validate_analytics_probe(merged, 'ios-simulator', expected_primary):
        return None
    # Validate the supplemental binding with the same strict identity rules.
    checked = copy.deepcopy(merged)
    checked['binding'] = expected_supplement
    if not native.validate_analytics_probe(checked, 'ios-simulator', expected_supplement):
        return None
    combined = hashlib.sha256(json.dumps({'primary': expected_primary['fixtureConfigSha256'],
                                          'voiceOver': expected_supplement['fixtureConfigSha256']}, sort_keys=True).encode()).hexdigest()
    merged['binding']['fixtureConfigSha256'] = combined
    return merged


def main():
    os.umask(0o077)
    event = json.loads(Path(os.environ['GITHUB_EVENT_PATH']).read_text())
    if (not authorized(event, os.environ) or os.environ.get('NATIVE_PLATFORM') != 'ios-simulator'
            or os.environ.get('NATIVE_ANALYTICS_VOICEOVER') != '1'
            or os.environ.get('RUNNER_OS') != 'macOS' or os.environ.get('RUNNER_ARCH') != 'ARM64'):
        raise SystemExit('Analytics VoiceOver verification authorization refused')
    if native.command('git', 'rev-parse', 'HEAD') != os.environ['GITHUB_SHA'] or native.command('git', 'status', '--porcelain', '--untracked-files=all'):
        raise SystemExit('VoiceOver source is not the exact clean candidate')
    native.ENV['DEVELOPER_DIR'] = '/Applications/Xcode_27.1.app/Contents/Developer'
    version = native.command('xcodebuild', '-version')
    if not version.startswith('Xcode 27.1\n'):
        raise SystemExit('Expected the selected Xcode 27.1 test driver')
    inputs = ROOT / 'mobile/.generated/voiceover-input'
    receipt_path = inputs / 'artifacts/analytics-compilation.json'
    if receipt_path.stat().st_size > 64 * 1024:
        raise SystemExit('VoiceOver compilation receipt exceeds bound')
    receipt = json.loads(receipt_path.read_text())
    artifact = inputs / 'artifacts/analytics-simulator.app.zip'
    if artifact.stat().st_size > 64 * 1024 * 1024:
        raise SystemExit('VoiceOver artifact exceeds verification bound')
    if (receipt.get('platform') != 'ios-simulator' or receipt.get('artifact') != artifact.name
            or receipt['candidate']['sourceCommit'] != os.environ['GITHUB_SHA']
            or hashlib.sha256(artifact.read_bytes()).hexdigest() != receipt['artifactSha256']):
        raise SystemExit('VoiceOver artifact is not the compiled candidate')
    plist = inputs / 'simulator-keychain.plist'
    if plist.stat().st_size > 4096 or not verify_archive(artifact.read_bytes()) or plistlib.loads(plist.read_bytes()) != ENTITLEMENTS:
        raise SystemExit('VoiceOver artifact transfer boundary refused')
    primary_path = ROOT / 'mobile/.generated/voiceover-primary/analytics-installed-observations.json'
    if primary_path.exists() and primary_path.stat().st_size > 512 * 1024:
        raise SystemExit('VoiceOver observations exceed verification bound')
    primary = json.loads(primary_path.read_text())['probe'] if primary_path.exists() else None
    artifact_binding = dict(sourceCommit=os.environ['GITHUB_SHA'],
        apiSha256=hashlib.sha256((ROOT / 'api/openapi.yaml').read_bytes()).hexdigest(),
        bundleSha256=receipt['candidate']['bundleSha256'], artifactSha256=receipt['artifactSha256'], contractVersion=1)
    expected_primary = dict(artifact_binding, fixtureConfigSha256=primary['binding']['fixtureConfigSha256']) if primary is not None else None
    native.GENERATED = inputs
    with tempfile.TemporaryDirectory(prefix='mychef-installed-') as directory, ExitStack() as stack:
        state = Path(directory).resolve()
        controls = {}
        ca, events, rejected = native.fixture(state, stack, analytics='disabled', controls=controls)
        fixture = Fixture(state, controls['restart'])
        fixture.native_events = events
        account = fixture.operate({'operation': 'seed'})
        control = fixture.serve(stack)
        metadata, launch, _, diagnostic = native.ios(state, stack, ca, artifact,
            model='iPhone 17', fixture_name='MyChef analytics voice fixture')
        devices = json.loads(native.command('xcrun', 'simctl', 'list', 'devices', 'booted', '-j'))['devices']
        identifiers = [d['udid'] for group in devices.values() for d in group if d.get('name') == 'MyChef analytics voice fixture']
        if len(identifiers) != 1:
            raise RuntimeError('Expected one owned VoiceOver simulator')
        identifier = identifiers[0]
        native.command('xcrun', 'simctl', 'ui', identifier, 'content_size', 'accessibility-medium')
        metadata['contentSize'] = 'accessibility-medium'
        container = Path(native.command('xcrun', 'simctl', 'get_app_container', identifier, native.APP, 'data'))
        native.verify_stage(lambda: ('POST', 'session', 201) in events and ('GET', 'session', 200) in events,
                            'VoiceOver native session bootstrap', events, rejected, diagnostic, launch=launch)
        # XCTest launches/restarts the installed target itself. A separate
        # simctl terminate can stall the hosted runtime after HTTPS warm-up.
        checks = analytics_ios.run(state, control, account, identifier, container, voiceover_only=True)
        if not all(('GET', kind, 200) in events for kind in ('analytics-notice-disabled', 'analytics-notice-assessed')):
            raise RuntimeError('VoiceOver requires both actual signup notice responses')
        text_scale = json.loads((container / 'tmp/analytics-text-result.json').read_text())
        if (set(text_scale) != {'root', 'label', 'notice', 'fits', 'osTextEnlarged'}
                or not all(value is True for value in text_scale.values())):
            raise RuntimeError('VoiceOver enlarged OS text did not propagate or fit')
        expected_supplement = dict(artifact_binding, fixtureConfigSha256=hashlib.sha256(json.dumps({
            'configuration': hashlib.sha256((state / 'analytics-config.json').read_bytes()).hexdigest(),
            'driver': hashlib.sha256((ROOT / 'mobile/tests/ios/AnalyticsProbe.swift').read_bytes()).hexdigest(),
            'overlays': {name: hashlib.sha256((state / name).read_bytes()).hexdigest()
                         for name in ('analytics-runtime-fixture.go', 'analytics-worker-fixture.go')},
            'runtime': metadata['runtime'], 'xcode': version,
        }, sort_keys=True).encode()).hexdigest())
        supplement = dict(schemaVersion=1, binding=expected_supplement,
                          checks={name: checks['accessibility'][name] for name in SPEECH})
        if not all(value is True for value in supplement['checks'].values()):
            raise RuntimeError('Actual VoiceOver observations are incomplete')
        merged = combine(primary, supplement, expected_primary, expected_supplement) if primary is not None else None
        if primary is not None and merged is None:
            raise RuntimeError('Incomplete or mismatched VoiceOver and primary observations')
        result = dict(probe=merged, fullJourneys=merged is not None, speechSupplement=supplement,
                      primaryObservationSha256=hashlib.sha256(primary_path.read_bytes()).hexdigest() if primary is not None else None,
                      device=metadata, distribution='not authorized', liveCollection='disabled',
                      fixture='fictional isolated accounts and actual VoiceOver service output')
        native.ARTIFACTS.mkdir(parents=True, exist_ok=True)
        (native.ARTIFACTS / 'analytics-installed-voiceover.json').write_text(json.dumps(result, indent=2) + '\n')
    print('Actual same-artifact VoiceOver passed; full receipt ' + ('complete' if merged is not None else 'withheld without primary journeys'))


if __name__ == '__main__':
    main()
