"""Build a separate XCTest probe; never rebuild or replace the tested app."""
import json
from pathlib import Path
import plistlib
import re
import shutil
import subprocess

import sys
ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'scripts'))
import test_native_installed as native


def safe_diagnostic(text, secrets):
    selected = [line for line in text.splitlines() if re.search(r'\berror:\s|\breason:\s|Assertion|Exception', line, re.I)]
    value = '\n'.join(selected[:8])
    for secret in secrets:
        if secret:
            value = value.replace(secret, '[private]')
    value = re.sub(r'https?://\S+', '[url]', value)
    value = re.sub(r'[^\s<>"\']+@[^\s<>"\']+', '[address]', value)
    value = re.sub(r'/[^\s"\'<>]+', '[path]', value)
    value = re.sub(r'\b[A-Za-z0-9_-]{43,}\b', '[opaque]', value)
    return value[:1200]


def project(state):
    root = Path(state) / 'ios-analytics-probe'
    root.mkdir(mode=0o700)
    shutil.copyfile(ROOT / 'mobile/tests/ios/AnalyticsProbe.swift', root / 'AnalyticsProbe.swift')
    identifiers = {name: f'A19{index:021X}' for index, name in enumerate((
        'project', 'group', 'products', 'target', 'product', 'source', 'build', 'sources',
        'frameworks', 'resources', 'projectList', 'projectDebug', 'targetList', 'targetDebug'), 1)}
    objects = {}
    def put(key, **value):
        objects[identifiers[key]] = value
    def ref(name):
        return identifiers[name]
    put('source', isa='PBXFileReference', lastKnownFileType='sourcecode.swift', path='AnalyticsProbe.swift', sourceTree='<group>')
    put('product', isa='PBXFileReference', explicitFileType='wrapper.cfbundle', path='AnalyticsProbe.xctest', sourceTree='BUILT_PRODUCTS_DIR')
    put('build', isa='PBXBuildFile', fileRef=ref('source'))
    put('sources', isa='PBXSourcesBuildPhase', buildActionMask=2147483647, files=[ref('build')], runOnlyForDeploymentPostprocessing=0)
    for name, kind in (('frameworks', 'PBXFrameworksBuildPhase'), ('resources', 'PBXResourcesBuildPhase')):
        put(name, isa=kind, buildActionMask=2147483647, files=[], runOnlyForDeploymentPostprocessing=0)
    put('products', isa='PBXGroup', children=[ref('product')], name='Products', sourceTree='<group>')
    put('group', isa='PBXGroup', children=[ref('source'), ref('products')], sourceTree='<group>')
    put('projectDebug', isa='XCBuildConfiguration', name='Debug', buildSettings=dict(
        SDKROOT='iphonesimulator', IPHONEOS_DEPLOYMENT_TARGET='17.0', SWIFT_VERSION='5.0', CODE_SIGNING_ALLOWED='NO'))
    put('targetDebug', isa='XCBuildConfiguration', name='Debug', buildSettings=dict(
        PRODUCT_NAME='AnalyticsProbe', PRODUCT_BUNDLE_IDENTIFIER='com.gkldevelopment.mychef.analyticsprobe',
        GENERATE_INFOPLIST_FILE='YES', SDKROOT='iphonesimulator', SUPPORTED_PLATFORMS='iphonesimulator',
        IPHONEOS_DEPLOYMENT_TARGET='17.0', SWIFT_VERSION='5.0', TARGETED_DEVICE_FAMILY='1',
        CODE_SIGNING_ALLOWED='NO', CODE_SIGN_IDENTITY='', ENABLE_TESTING_SEARCH_PATHS='YES',
        TEST_TARGET_BUNDLE_IDENTIFIER=native.APP,
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


def run(state, control, account, identifier, container, layout_only=False, voiceover_only=False):
    root = Path(state)
    output = root / 'analytics-ios-observations.json'
    input_path = root / 'analytics-ios-input.json'
    input_path.write_text(json.dumps({**control, **account, 'appContainer': str(container),
                                     'contract': str(ROOT / 'mobile/tests/analytics-contract.json'), 'output': str(output),
                                     'layoutOnly': 'true' if layout_only else 'false',
                                     'voiceOverOnly': 'true' if voiceover_only else 'false',
                                     'anonymousScreenshot': str(native.ARTIFACTS / 'analytics-anonymous.png')}))
    input_path.chmod(0o600)
    source = project(root)
    derived = root / 'ios-probe-build'
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
            roles = re.findall(r'Analytics iOS accessibility type: ([0-9]{1,3}) state: (off|on|other) enabled: (true|false)', text)
            if roles:
                detail += '; observed roles ' + ', '.join('/'.join(role) for role in roles[-8:])
            known = re.findall(r'Analytics iOS known control: ([A-Za-z ]{1,30}) types: ([0-9,]{0,30})', text)
            if known:
                detail += '; known roles ' + ', '.join(name + '=' + types for name, types in known[-5:])
            states = re.findall(r'Analytics iOS screen state: (signed out|profile|shopping|startup|inbox) present=(true|false)', text)
            app_state = re.findall(r'Analytics iOS app state: ([0-9]) webview=([0-9]{1,2}) alert=([0-9]{1,2})', text)
            if app_state:
                detail += '; app state/webview/alert ' + '/'.join(app_state[-1])
            if states:
                detail += '; screens ' + ', '.join(name + '=' + value for name, value in states[-5:])
            taps = re.findall(r'Analytics iOS tap state: type=([0-9]{1,3}) exists=(true|false) hittable=(true|false) stable=([0-9]{1,3})', text)
            if taps:
                detail += '; tap type/exists/hittable/stable ' + ', '.join('/'.join(row) for row in taps[-3:])
            speech = re.findall(r'Analytics iOS spoken control: role=(true|false) disabled=(true|false) state=(on|off|unknown)', text)
            if speech:
                detail += '; spoken role/disabled/state ' + ', '.join('/'.join(row) for row in speech[-4:])
            diagnostic = safe_diagnostic(text, [*account.values(), control['token'], str(input_path), str(container)])
            if diagnostic:
                detail += '; ' + diagnostic
            raise RuntimeError('Isolated iOS analytics probe failed: ' + detail)
    execute(['xcodebuild', '-project', str(source), '-scheme', 'AnalyticsProbe', '-sdk', 'iphonesimulator',
             '-destination', 'id=' + identifier, '-derivedDataPath', str(derived), 'CODE_SIGNING_ALLOWED=NO',
             'CODE_SIGN_IDENTITY=', 'build-for-testing'], 240)
    plans = list((derived / 'Build/Products').glob('*.xctestrun'))
    if len(plans) != 1:
        raise RuntimeError('Expected one isolated XCTest plan')
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
    target['SystemAttachmentLifetime'] = 'deleteAlways'
    target['UserAttachmentLifetime'] = 'deleteAlways'
    plans[0].write_bytes(plistlib.dumps(plan))
    execute(['xcodebuild', 'test-without-building', '-xctestrun', str(plans[0]), '-destination', 'id=' + identifier,
             '-parallel-testing-enabled', 'NO', '-maximum-concurrent-test-simulator-destinations', '1'], 900)
    value = json.loads(output.read_text())
    if type(value) is not dict or set(value) != {'checks'}:
        raise RuntimeError('Incomplete iOS analytics observations')
    return value['checks']
