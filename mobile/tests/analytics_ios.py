"""Build a separate XCTest probe; never rebuild or replace the tested app."""
import json
import os
import stat
from pathlib import Path
import plistlib
import re
import shutil
import subprocess

import sys
ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'scripts'))
import test_native_installed as native

PROBE_SOURCES = ('AnalyticsProbe.swift', 'AnalyticsTouch.h', 'AnalyticsTouch.m')


def text_observation(identifier):
    """Read actual measurements from the app's current, owned data container."""
    try:
        container = Path(native.command('xcrun', 'simctl', 'get_app_container',
                                        identifier, native.APP, 'data'))
        if not container.is_absolute():
            raise ValueError('Invalid container')
        path = container / 'tmp/analytics-text-result.json'
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(descriptor, 'rb') as stream:
            metadata = os.fstat(stream.fileno())
            if not stat.S_ISREG(metadata.st_mode) or metadata.st_size > 4096:
                raise ValueError('Invalid measurement file')
            raw = stream.read(4097)
        if len(raw) > 4096:
            raise ValueError('Oversized measurement')
        scale = json.loads(raw)
        if (not isinstance(scale, dict)
                or set(scale) != {'root', 'label', 'notice', 'fits', 'osTextEnlarged'}
                or not all(value is True for value in scale.values())):
            raise ValueError('Incomplete actual measurements')
        return container, scale
    except (OSError, ValueError, TypeError) as error:
        # A fixed category distinguishes an expired/missing measurement from
        # a rejected measurement without exposing paths or file contents.
        reason = 'missing' if isinstance(error, FileNotFoundError) else 'invalid'
        raise RuntimeError('Installed iOS text observation unavailable or invalid; reason=' + reason) from None


def safe_diagnostic(text, secrets):
    selected = [line for line in text.splitlines() if re.search(r'\berror:\s|\breason:\s|Assertion|Exception', line, re.I) and 'DVTAssertions:' not in line]
    value = '\n'.join(selected[:8])
    for secret in secrets:
        if secret:
            value = value.replace(secret, '[private]')
    value = re.sub(r'https?://\S+', '[url]', value)
    value = re.sub(r'[^\s<>"\']+@[^\s<>"\']+', '[address]', value)
    value = re.sub(r'/[^\s"\'<>]+', '[path]', value)
    value = re.sub(r'\b[A-Za-z0-9_-]{43,}\b', '[opaque]', value)
    return value[:1200]


def voice_navigation(text):
    return re.search(r'^Analytics iOS VoiceOver navigation: native traversal verified=true$', text, re.M) is not None


def voice_focus(text):
    """Retain fixed focus facts, never the matched utterance."""
    rows = re.findall(r'^Analytics iOS voice focus facts: labelStart=(true|false) '
                      r'container=(true|false) otherLabels=([0-9])$', text, re.M)
    return ','.join('/'.join(row) for row in rows[-4:]) if rows else None


def voice_environment(text):
    """Retain no voice identifiers, raw utterance, locale list or device paths."""
    rows = re.findall(r'^Analytics iOS voice environment: api=(true|false) native=(true|false) '
                      r'voices=([0-9]{1,3}) english=([0-9]{1,3})$', text, re.M)
    if not rows:
        return None
    api, native_enabled, voices, english = rows[-1]
    if int(english) > int(voices):
        return None
    return 'api=' + api + ' native=' + native_enabled + ' voices=' + voices + ' english=' + english


def project(state):
    root = Path(state) / 'ios-analytics-probe'
    root.mkdir(mode=0o700)
    for name in PROBE_SOURCES:
        shutil.copyfile(ROOT / 'mobile/tests/ios' / name, root / name)
    identifiers = {name: f'A19{index:021X}' for index, name in enumerate((
        'project', 'group', 'products', 'target', 'product', 'source', 'build', 'sources',
        'frameworks', 'resources', 'projectList', 'projectDebug', 'targetList', 'targetDebug',
        'touchHeader', 'touchSource', 'touchBuild'), 1)}
    objects = {}
    def put(key, **value):
        objects[identifiers[key]] = value
    def ref(name):
        return identifiers[name]
    put('source', isa='PBXFileReference', lastKnownFileType='sourcecode.swift', path='AnalyticsProbe.swift', sourceTree='<group>')
    put('touchHeader', isa='PBXFileReference', lastKnownFileType='sourcecode.c.h', path='AnalyticsTouch.h', sourceTree='<group>')
    put('touchSource', isa='PBXFileReference', lastKnownFileType='sourcecode.c.objc', path='AnalyticsTouch.m', sourceTree='<group>')
    put('touchBuild', isa='PBXBuildFile', fileRef=ref('touchSource'))
    put('product', isa='PBXFileReference', explicitFileType='wrapper.cfbundle', path='AnalyticsProbe.xctest', sourceTree='BUILT_PRODUCTS_DIR')
    put('build', isa='PBXBuildFile', fileRef=ref('source'))
    put('sources', isa='PBXSourcesBuildPhase', buildActionMask=2147483647, files=[ref('build'), ref('touchBuild')], runOnlyForDeploymentPostprocessing=0)
    for name, kind in (('frameworks', 'PBXFrameworksBuildPhase'), ('resources', 'PBXResourcesBuildPhase')):
        put(name, isa=kind, buildActionMask=2147483647, files=[], runOnlyForDeploymentPostprocessing=0)
    put('products', isa='PBXGroup', children=[ref('product')], name='Products', sourceTree='<group>')
    put('group', isa='PBXGroup', children=[ref('source'), ref('touchHeader'), ref('touchSource'), ref('products')], sourceTree='<group>')
    put('projectDebug', isa='XCBuildConfiguration', name='Debug', buildSettings=dict(
        SDKROOT='iphonesimulator', IPHONEOS_DEPLOYMENT_TARGET='17.0', SWIFT_VERSION='5.0', CODE_SIGNING_ALLOWED='NO'))
    put('targetDebug', isa='XCBuildConfiguration', name='Debug', buildSettings=dict(
        PRODUCT_NAME='AnalyticsProbe', PRODUCT_BUNDLE_IDENTIFIER='com.gkldevelopment.mychef.analyticsprobe',
        GENERATE_INFOPLIST_FILE='YES', SDKROOT='iphonesimulator', SUPPORTED_PLATFORMS='iphonesimulator',
        IPHONEOS_DEPLOYMENT_TARGET='17.0', SWIFT_VERSION='5.0', TARGETED_DEVICE_FAMILY='1',
        CODE_SIGNING_ALLOWED='NO', CODE_SIGN_IDENTITY='', ENABLE_TESTING_SEARCH_PATHS='YES',
        TEST_TARGET_BUNDLE_IDENTIFIER=native.APP, CLANG_ENABLE_OBJC_ARC='YES', CLANG_ENABLE_MODULES='YES',
        SWIFT_OBJC_BRIDGING_HEADER='$(SRCROOT)/AnalyticsTouch.h',
        LD_RUNPATH_SEARCH_PATHS=['$(inherited)', '@executable_path/Frameworks', '@loader_path/Frameworks']))
    for name, config in (('projectList', 'projectDebug'), ('targetList', 'targetDebug')):
        put(name, isa='XCConfigurationList', buildConfigurations=[ref(config)], defaultConfigurationIsVisible=0, defaultConfigurationName='Debug')
    put('target', isa='PBXNativeTarget', name='AnalyticsProbe', productName='AnalyticsProbe',
        productReference=ref('product'), productType='com.apple.product-type.bundle.ui-testing',
        buildConfigurationList=ref('targetList'), buildPhases=[ref('sources'), ref('frameworks'), ref('resources')],
        buildRules=[], dependencies=[])
    put('project', isa='PBXProject', attributes=dict(LastUpgradeCheck='2630'), buildConfigurationList=ref('projectList'),
        compatibilityVersion='Xcode 15.0', developmentRegion='en', hasScannedForEncodings=0, knownRegions=['en', 'Base'],
        mainGroup=ref('group'), productRefGroup=ref('products'), projectDirPath='', projectRoot='', targets=[ref('target')])
    path = root / 'AnalyticsProbe.xcodeproj'
    path.mkdir()
    # Xcode's project reader expects scalar strings as in an OpenStep plist.
    # XML NSNumber values can crash IDEContainer before compilation starts.
    def scalars(value):
        if isinstance(value, dict):
            return {key: scalars(item) for key, item in value.items()}
        if isinstance(value, list):
            return [scalars(item) for item in value]
        return str(value) if isinstance(value, int) else value
    (path / 'project.pbxproj').write_bytes(plistlib.dumps(scalars(dict(archiveVersion=1, classes={}, objectVersion=60,
                                                                   objects=objects, rootObject=ref('project')))))
    schemes = path / 'xcshareddata/xcschemes'
    schemes.mkdir(parents=True)
    buildable = (f'<BuildableReference BuildableIdentifier="primary" BlueprintIdentifier="{ref("target")}" '
                 'BuildableName="AnalyticsProbe.xctest" BlueprintName="AnalyticsProbe" ReferencedContainer="container:AnalyticsProbe.xcodeproj"/>')
    (schemes / 'AnalyticsProbe.xcscheme').write_text(
        '<?xml version="1.0" encoding="UTF-8"?><Scheme version="1.3" LastUpgradeVersion="2630">'
        '<BuildAction parallelizeBuildables="NO" buildImplicitDependencies="YES"><BuildActionEntries>'
        '<BuildActionEntry buildForTesting="YES" buildForRunning="NO" buildForProfiling="NO" buildForArchiving="NO" buildForAnalyzing="YES">'
        + buildable + '</BuildActionEntry></BuildActionEntries></BuildAction>'
        '<TestAction buildConfiguration="Debug" shouldUseLaunchSchemeArgsEnv="YES"><Testables>'
        '<TestableReference skipped="NO" parallelizable="NO">' + buildable + '</TestableReference>'
        '</Testables></TestAction></Scheme>')
    return path


def bootstrap(identifier, events, rejected, diagnostic):
    """Warm the actual installed app and require fresh native HTTPS proof."""
    boundary = len(events)
    def ready():
        fresh = events[boundary:]
        return (('GET', 'session', 200) in fresh
                and (('POST', 'session', 201) in fresh or ('POST', 'refresh', 200) in fresh))
    # Cold simulator runs returned the real session responses during failure
    # diagnostics, after the previous 100-second readiness window. Allow that
    # measured startup delay; old responses still cannot acknowledge this launch.
    native.verify_stage(ready, 'fresh iOS analytics fixture session', events, rejected, diagnostic,
                        launch=lambda: native.command('xcrun', 'simctl', 'launch', identifier, native.APP,
                                                      '--mychef-analytics-native-fixture', timeout=150),
                        event_start=boundary, tls_start=len(rejected), wait_after_launch_timeout=True,
                        wait_seconds=180)



def capture_anonymous_start(identifier):
    """Optional host capture after anonymous bootstrap and before XCTest input."""
    path = native.ARTIFACTS / 'analytics-anonymous.png'
    pending = native.ARTIFACTS / 'analytics-anonymous-pending.png'
    try:
        if path.exists():
            return False  # Never replace the initial image in a later phase.
        path.parent.mkdir(parents=True, exist_ok=True)
        native.command('xcrun', 'simctl', 'io', identifier, 'screenshot', str(pending), timeout=20)
        if not 8 <= pending.stat().st_size <= 2 * 1024 * 1024:
            raise RuntimeError('Anonymous image outside bounds')
        with pending.open('rb') as image:
            if image.read(8) != b'\x89PNG\r\n\x1a\n':
                raise RuntimeError('Anonymous image format unavailable')
        pending.chmod(0o600)
        # Publish validated bytes atomically without replacing an existing
        # image. Pending media is outside the CI artifact upload allowlist.
        os.link(pending, path)
        return True
    except (OSError, RuntimeError, subprocess.SubprocessError):
        print('Optional anonymous pre-input image unavailable', flush=True)
        return False
    finally:
        try:
            pending.unlink(missing_ok=True)
        except OSError:
            pass


def prepare(state, identifier):
    """Compile only the separate probe, optionally while its phone is shut down."""
    root = Path(state)
    source = project(root)
    derived = root / 'ios-probe-build'
    arguments = ['xcodebuild', '-project', str(source), '-scheme', 'AnalyticsProbe', '-sdk', 'iphonesimulator',
                 '-destination', 'id=' + identifier, '-derivedDataPath', str(derived), 'CODE_SIGNING_ALLOWED=NO',
                 'CODE_SIGN_IDENTITY=', 'build-for-testing']
    try:
        result = subprocess.run(arguments, cwd=root, env=native.ENV, stdout=subprocess.PIPE,
                                stderr=subprocess.STDOUT, timeout=240)
    except subprocess.TimeoutExpired:
        raise RuntimeError('Isolated iOS analytics probe compilation deadline 240 seconds') from None
    if result.returncode:
        # Compilation has no account input. Still publish only fixed classes
        # and numeric source lines, never raw compiler or runner output.
        text = result.stdout.decode(errors='replace')
        sites = sorted(set(re.findall(r'(AnalyticsProbe\.swift|AnalyticsTouch\.[hm]):([0-9]{1,4}):', text)))
        raise RuntimeError('Isolated iOS analytics probe compilation failed; source lines '
                           + ','.join(name + ':' + line for name, line in sites[-6:]))
    plans = list((derived / 'Build/Products').glob('*.xctestrun'))
    if len(plans) != 1:
        raise RuntimeError('Expected one isolated XCTest plan')
    return plans[0]


def run(state, control, account, identifier, container, layout_only=False, voiceover_only=False, before_test=None,
        prepared_plan=None):
    root = Path(state)
    output = root / 'analytics-ios-observations.json'
    input_path = root / 'analytics-ios-input.json'
    input_path.write_text(json.dumps({**control, **account, 'appContainer': str(container),
                                     'contract': str(ROOT / 'mobile/tests/analytics-contract.json'), 'output': str(output),
                                     'layoutOnly': 'true' if layout_only else 'false',
                                     'voiceOverOnly': 'true' if voiceover_only else 'false',
                                     'preserveBootstrap': 'true' if before_test is not None else 'false',
                                     'anonymousScreenshot': str(native.ARTIFACTS / 'analytics-anonymous.png')}))
    input_path.chmod(0o600)
    # Raw XCTest logs can include synthetic field values. Keep them private,
    # never echo a failing assertion's data or upload an xcresult/screenshots.
    def execute(arguments, seconds):
        try:
            result = subprocess.run(arguments, cwd=root, env=native.ENV, stdout=subprocess.PIPE,
                                    stderr=subprocess.STDOUT, timeout=seconds)
        except subprocess.TimeoutExpired as error:
            text = (error.stdout or b'').decode(errors='replace')
            stages = re.findall(r'Analytics iOS (?:phase|control|voice phase): ([A-Za-z ?]{1,60})', text)
            raise RuntimeError('Isolated iOS analytics probe deadline ' + str(seconds)
                               + ' seconds; controls ' + ', '.join(stages[-4:])) from None
        if result.returncode:
            # Fixed compiler error classes only, without source/paths/values.
            text = result.stdout.decode(errors='replace')
            categories = sorted({kind for kind in ('SwiftCompile', 'CompileSwift', 'CompileAssetCatalog', 'CodeSign', 'Testing failed') if kind in text})
            lines = sorted(set(re.findall(r'AnalyticsProbe\.swift:([0-9]{1,4}):(?:[0-9]+:)?\s*(?:error:)?', text)))
            stages = re.findall(r'Analytics iOS (?:phase|control|voice phase): ([A-Za-z ?]{1,60})', text)
            detail = ', '.join(categories or ['XCTest'])
            if lines:
                detail += '; source lines ' + ', '.join(lines[-6:])
            if stages:
                detail += '; controls ' + ', '.join(stages[-4:])
            focus = voice_focus(text)
            if focus:
                detail += '; focused speech labelStart/container/otherLabels ' + focus
            environment = voice_environment(text)
            if environment:
                detail += '; voice environment ' + environment
            requests = re.findall(r'Analytics iOS preference requests: reads=([0-9]{1,5}) writes=([0-9]{1,5}) status=([0-9]{1,3})', text)
            if requests:
                detail += '; preference reads/writes/status ' + '/'.join(requests[-1])
            choices = re.findall(r'Analytics iOS preference control: state=(on|off|unknown)', text)
            if choices:
                detail += '; preference control ' + choices[-1]
            selections = re.findall(r'Analytics iOS Flat selection: label=(true|false) cell=(true|false) button=(true|false)', text)
            if selections:
                detail += '; Flat label/cell/button selected ' + '/'.join(selections[-1])
            settings_rows = re.findall(r'Analytics iOS Settings row: exists=(true|false) frame=(true|false) '
                                       r'bar=(true|false) visible=(true|false) stable=([0-9]{1,2})', text)
            if settings_rows:
                detail += '; Settings row exists/frame/bar/visible/stable ' + '/'.join(settings_rows[-1])
            roles = re.findall(r'Analytics iOS accessibility type: ([0-9]{1,3}) state: (off|on|other) enabled: (true|false)', text)
            if roles:
                detail += '; observed roles ' + ', '.join('/'.join(role) for role in roles[-8:])
            choice_frames = re.findall(r'Analytics iOS choice frame: width=([0-9]{1,4}) height=([0-9]{1,4})', text)
            if choice_frames:
                detail += '; choice width/height ' + '/'.join(choice_frames[-1])
            known = re.findall(r'Analytics iOS known control: ([A-Za-z ]{1,30}) types: ([0-9,]{0,30})', text)
            if known:
                detail += '; known roles ' + ', '.join(name + '=' + types for name, types in known[-12:])
            states = re.findall(r'Analytics iOS screen state: (signed out|profile|shopping|startup|inbox|signup|completion|created|creating|secure failure|configuration failure|storage failure|shopping failure|device failure) present=(true|false)', text)
            keyboard_states = re.findall(r'Analytics iOS keyboard state: present=(true|false) done=(true|false)', text)
            if keyboard_states:
                detail += '; keyboard present/done ' + '/'.join(keyboard_states[-1])
            app_state = re.findall(r'Analytics iOS app state: ([0-9]) webview=([0-9]{1,2}) alert=([0-9]{1,2})', text)
            if app_state:
                detail += '; app state/webview/alert ' + '/'.join(app_state[-1])
            if states:
                detail += '; screens ' + ', '.join(name + '=' + value for name, value in states[-14:])
            picker = re.findall(r'Analytics iOS picker control: (Cancel|Close|Done|Save|Browse|Locations|On My iPhone|Downloads) total=([0-9]{1,2}) web=([0-9]{1,2})', text)
            if picker:
                detail += '; picker total/web ' + ','.join(name + '=' + total + '/' + web for name, total, web in picker[-8:])
            picker_types = re.findall(r'Analytics iOS picker types: (Cancel|Close|Done|Save|Browse|Locations|On My iPhone|Downloads) all=([0-9,]{0,30})', text)
            if picker_types:
                detail += '; picker types ' + ','.join(name + '=' + types for name, types in picker_types[-8:])
            navigation = re.findall(r'Analytics iOS picker navigation: (Cancel|Close|Done|Save|Browse|Locations|On My iPhone|Downloads) count=([0-9]{1,2})', text)
            if navigation:
                detail += '; picker navigation ' + ','.join(name + '=' + count for name, count in navigation[-8:])
            taps = re.findall(r'Analytics iOS tap state: type=([0-9]{1,3}) exists=(true|false) ready=(true|false) stable=([0-9]{1,3})', text)
            if taps:
                detail += '; tap type/exists/ready/stable ' + ', '.join('/'.join(row) for row in taps[-3:])
            viewports = re.findall(r'Analytics iOS tap viewport: views=([0-9]{1,2}) roots=([0-9]{1,2}) targets=([0-9]{1,2})', text)
            if viewports:
                detail += '; tap viewport views/roots/targets ' + '/'.join(viewports[-1])
            reveal_scrolls = re.findall(r'Analytics iOS reveal scroll: moved=(true|false) stable=(true|false) region=(true|false)', text)
            if reveal_scrolls:
                detail += '; reveal scroll moved/stable/region ' + ','.join('/'.join(row) for row in reveal_scrolls[-12:])
            rectangles = re.findall(r'Analytics iOS tap rect: ([0-9.,{} -]{1,100})', text)
            if rectangles:
                detail += '; tap rect ' + rectangles[-1]
            traversals = re.findall(r'Analytics iOS voice traversal: reverse=(true|false) visited=([0-9]{1,2}) entered=([0-9]{1,2}) initialExits=([0-4]) matched=(true|false)', text)
            if traversals:
                detail += '; voice traversal reverse/visited/entered/initialExits/matched ' + ','.join('/'.join(row) for row in traversals[-4:])
            speech = re.findall(r'Analytics iOS spoken control: role=(true|false) disabled=(true|false) state=(on|off|unknown)', text)
            if speech:
                detail += '; spoken role/disabled/state ' + ', '.join('/'.join(row) for row in speech[-4:])
            unlabeled = re.findall(r'Analytics iOS spoken unlabeled choice: on=(true|false) off=(true|false)', text)
            if unlabeled:
                detail += '; unlabeled spoken choice on/off ' + ','.join('/'.join(row) for row in unlabeled[-4:])
            containers = re.findall(r'Analytics iOS spoken container: privacy=(true|false) label=(true|false)', text)
            if containers:
                detail += '; spoken container privacy/label ' + ', '.join('/'.join(row) for row in containers[-4:])
            candidates = re.findall(r'Analytics iOS spoken candidate without role: on=(true|false) off=(true|false)', text)
            if candidates:
                detail += '; spoken candidate without role on/off ' + ', '.join('/'.join(row) for row in candidates[-4:])
            containers = re.findall(r'Analytics iOS spoken container: privacy=(true|false) label=(true|false) depth=([0-3])', text)
            if containers:
                detail += '; container privacy/label/depth ' + ','.join('/'.join(row) for row in containers[-6:])
            controllers = re.findall(r'Analytics iOS controller: kind=(restart|operation) status=([0-9]{1,3}) '
                                     r'network=(0|-999|-1001|-1003|-1004|-1005|-1009|-1022|other)\b', text)
            if controllers:
                detail += '; controller kind/status/network ' + ','.join('/'.join(row) for row in controllers[-3:])
            repeats = re.findall(r'Analytics iOS spoken container repeat: kind=(web|privacy|label)', text)
            if repeats:
                detail += '; container repeats ' + ','.join(repeats[-6:])
            voice_anchors = re.findall(r'Analytics iOS voice anchor: region=(true|false) ready=(true|false)', text)
            if voice_anchors:
                detail += '; voice anchor region/ready ' + '/'.join(voice_anchors[-1])
            voice_scrolls = re.findall(r'Analytics iOS VoiceOver scroll: acknowledged=(true|false) code=([0-5])', text)
            if voice_scrolls:
                detail += '; native three-finger scroll acknowledged/code ' + ','.join('/'.join(row) for row in voice_scrolls[-6:])
            scroll_modes = re.findall(r'Analytics iOS VoiceOver scroll mode: precise=(true|false)', text)
            if scroll_modes:
                detail += '; native scroll precise modes ' + ','.join(scroll_modes[-6:])
            scroll_geometry = re.findall(r'Analytics iOS VoiceOver scroll geometry: moved=(true|false) region=(true|false)', text)
            if scroll_geometry:
                detail += '; native scroll moved/region ' + ','.join('/'.join(row) for row in scroll_geometry[-6:])
            scroll_settling = re.findall(r'Analytics iOS VoiceOver scroll settling: stable=(true|false) toward=(true|false) crossed=(true|false)', text)
            if scroll_settling:
                detail += '; native scroll stable/toward/crossed ' + ','.join('/'.join(row) for row in scroll_settling[-6:])
            voice_frames = re.findall(r'Analytics iOS voice anchor frame: valid=(true|false) above=(true|false) below=(true|false) keyboard=(true|false)', text)
            if voice_frames:
                detail += '; voice frame valid/above/below/keyboard ' + '/'.join(voice_frames[-1])
            voice_errors = re.findall(r'Analytics iOS voice API error: ([0-9]{1,2}|other)', text)
            if voice_errors:
                detail += '; voice API error codes ' + ','.join(sorted(set(voice_errors)))
            voice_categories = re.findall(r'Analytics iOS voice error category: (no-speech|not-running|other)', text)
            if voice_categories:
                detail += '; voice error categories ' + ','.join(sorted(set(voice_categories)))
            vocabularies = re.findall(r'Analytics iOS spoken vocabulary: ([a-z ,]{0,200})', text)
            allowed = {'help improve mychef', 'analytics is off', 'unavailable', 'check box', 'checkbox', 'checked', 'button', 'web content', 'create an account', 'privacy'}
            for vocabulary in vocabularies[-2:]:
                tokens = set(vocabulary.split(',')) - {''}
                if tokens <= allowed:
                    detail += '; spoken vocabulary ' + ','.join(sorted(tokens))
            diagnostic = safe_diagnostic(text, [*account.values(), control['token'], str(input_path), str(container)])
            if diagnostic:
                detail += '; ' + diagnostic
            raise RuntimeError('Isolated iOS analytics probe failed: ' + detail)
        if voiceover_only:
            if not voice_navigation(result.stdout.decode(errors='replace')):
                raise RuntimeError('Native VoiceOver traversal was not verified')
            (root / 'analytics-voice-navigation.json').write_text(json.dumps(
                {'method': 'native-container-traversal', 'focusOrderObserved': True}))
    if prepared_plan is None:
        prepared_plan = prepare(root, identifier)
    plans = list((root / 'ios-probe-build/Build/Products').glob('*.xctestrun'))
    if (len(plans) != 1 or Path(prepared_plan).resolve() != plans[0].resolve()
            or any(not (root / 'ios-analytics-probe' / name).is_file()
                   or (root / 'ios-analytics-probe' / name).read_bytes()
                      != (ROOT / 'mobile/tests/ios' / name).read_bytes() for name in PROBE_SOURCES)):
        raise RuntimeError('Expected the current isolated XCTest plan')
    plan = plistlib.loads(plans[0].read_bytes())
    # Xcode may emit either the modern TestConfigurations form or the legacy
    # target dictionary. Update only the separate probe's execution parameters.
    targets = [target for config in plan.get('TestConfigurations', []) for target in config.get('TestTargets', [])]
    if not targets:
        targets = [value for key, value in plan.items() if not key.startswith('__') and isinstance(value, dict)]
    if len(targets) != 1:
        raise RuntimeError('Unexpected XCTest target set')
    target = targets[0]
    target['UITargetAppPath'] = str(root / 'App.app')
    target['UITargetAppBundleIdentifier'] = native.APP
    target.setdefault('EnvironmentVariables', {})['MYCHEF_ANALYTICS_PROBE_INPUT'] = str(input_path)
    target['SystemAttachmentLifetime'] = 'keepNever'
    target['UserAttachmentLifetime'] = 'keepNever'
    plans[0].write_bytes(plistlib.dumps(plan))
    if before_test is not None:
        before_test()
        if not layout_only and not voiceover_only:
            # This is the only capture point, before XCTest can enter data.
            # Missing optional media never supplies or invalidates an observation.
            capture_anonymous_start(identifier)
    # A measured full run completed both exports and reached final deletion
    # at 30 minutes. Allow five more only for this full functional path;
    # keep speech/layout budgets and the overall 65-minute job unchanged.
    seconds = 1500 if layout_only or voiceover_only else 2100
    execute(['xcodebuild', 'test-without-building', '-xctestrun', str(plans[0]), '-destination', 'id=' + identifier,
             '-parallel-testing-enabled', 'NO', '-maximum-concurrent-test-simulator-destinations', '1'], seconds)
    value = json.loads(output.read_text())
    if type(value) is not dict or set(value) != {'checks'}:
        raise RuntimeError('Incomplete iOS analytics observations')
    return value['checks']
