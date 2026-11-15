"""Offline native evidence and ordinary-check wiring contracts; standard library only."""
from __future__ import annotations

import ast
import copy
import http.server
import json
import re
import threading
import types
from contextlib import closing
from email.message import Message
import importlib.util
import io
from contextlib import redirect_stdout
from pathlib import Path
import plistlib
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "scripts"))
spec = importlib.util.spec_from_file_location("native_verification_menu", ROOT / "scripts/test_rewrite_menu.py")
menu = importlib.util.module_from_spec(spec)
spec.loader.exec_module(menu)
import test_native_installed as installed
import native_android_probe as probe
import native_fixture_account


class AnalyticsFullReceiptTest(unittest.TestCase):
    """A JSON claim is bounded evidence, never a substitute for an installed run."""

    def setUp(self):
        self.checks = {
            'signup': ['disabledNotice', 'disabledNoCapture', 'assessedNotice', 'offBeforeCompletion', 'completedOffNoCapture'],
            'participation': ['existingOff', 'explicitEnable', 'committedCoreAction', 'restarted', 'exactlyOneEvent', 'noClientAnalyticsIdentifiers'],
            'conflict': ['staleConflict', 'currentState', 'offlineOffUnconfirmed', 'reconnectFenceAcknowledged'],
            'queue': ['originalRevision', 'productEditCommitted', 'noOldEpochEvent', 'retryNoDuplicate', 'relaunchNoDuplicate'],
            'lifecycle': ['activeCleanup', 'isolatedBackupPending', 'nativeSave', 'nativeCancel', 'ownExportOnly', 'foreignExportRefused', 'accountDeleted', 'relaunchSessionCacheCleared'],
            'accessibility': ['platformScreenReader', 'spokenStatesAndErrors', 'focusOrder', 'enlargedOSText', 'textScalePropagated', 'phoneWidths', 'keyboardUnobstructed', 'noClipping'],
        }
        self.expected = {}
        self.receipts = []
        for platform, artifact in (('android', 'd'), ('ios-simulator', 'f')):
            binding = dict(sourceCommit='a' * 40, apiSha256='b' * 64, bundleSha256='c' * 64,
                           artifactSha256=artifact * 64, fixtureConfigSha256='e' * 64, contractVersion=1)
            self.expected[platform] = binding
            self.receipts.append(dict(schemaVersion=1, platform=platform, binding=dict(binding),
                                      checks={group: dict.fromkeys(names, True) for group, names in self.checks.items()}))

    def test_both_platforms_and_all_six_journeys_are_required(self):
        self.assertTrue(installed.validate_analytics_receipts(self.receipts, self.expected))
        for platform in range(2):
            self.assertFalse(installed.validate_analytics_receipts([self.receipts[platform]], self.expected))
            for group, names in self.checks.items():
                for name in names:
                    receipts = copy.deepcopy(self.receipts)
                    receipts[platform]['checks'][group][name] = False
                    with self.subTest(platform=platform, group=group, name=name):
                        self.assertFalse(installed.validate_analytics_receipts(receipts, self.expected))
        self.assertFalse(installed.validate_analytics_receipts([self.receipts[0]] * 2, self.expected))

    def test_candidate_api_bundle_artifact_configuration_and_contract_must_match(self):
        for platform in range(2):
            for key in self.expected['android']:
                receipts = copy.deepcopy(self.receipts)
                receipts[platform]['binding'][key] = 2 if key == 'contractVersion' else '0' * (40 if key == 'sourceCommit' else 64)
                with self.subTest(platform=platform, key=key):
                    self.assertFalse(installed.validate_analytics_receipts(receipts, self.expected))

    def test_platform_specific_private_fixture_paths_have_their_own_bound_configuration(self):
        expected = copy.deepcopy(self.expected)
        receipts = copy.deepcopy(self.receipts)
        expected['ios-simulator']['fixtureConfigSha256'] = '0' * 64
        receipts[1]['binding']['fixtureConfigSha256'] = '0' * 64
        self.assertTrue(installed.validate_analytics_receipts(receipts, expected))
        expected['ios-simulator']['sourceCommit'] = '1' * 40
        receipts[1]['binding']['sourceCommit'] = '1' * 40
        self.assertFalse(installed.validate_analytics_receipts(receipts, expected))

    def test_incomplete_coerced_private_or_invented_probe_receipts_are_refused(self):
        for payload in (None, {}, [], [True], self.receipts + [self.receipts[0]]):
            self.assertFalse(installed.validate_analytics_receipts(payload, self.expected))
        for field, value in (('email', 'fixture@example.test'), ('subject', 'private'),
                             ('path', '/private/operator'), ('token', 'Bearer private')):
            receipts = copy.deepcopy(self.receipts)
            receipts[0][field] = value
            self.assertFalse(installed.validate_analytics_receipts(receipts, self.expected))
        for value in (1, 'true', None, {'passed': True}):
            receipts = copy.deepcopy(self.receipts)
            receipts[0]['checks']['signup']['disabledNoCapture'] = value
            self.assertFalse(installed.validate_analytics_receipts(receipts, self.expected))
        receipts = copy.deepcopy(self.receipts)
        receipts[0]['checks']['accessibility'].pop('spokenStatesAndErrors')
        self.assertFalse(installed.validate_analytics_receipts(receipts, self.expected))


class AnalyticsFixtureTest(unittest.TestCase):
    def test_assessed_fixture_has_separate_fresh_private_paths_and_no_external_provider(self):
        with tempfile.TemporaryDirectory(prefix='mychef-installed-') as directory:
            root = Path(directory)
            config = installed.analytics_fixture_configuration(root, True)
            document = json.loads(config.read_text())
            self.assertTrue(document['collection']['Enabled'])
            self.assertEqual(document['collection']['Assessment']['audience'], 'fictional@example.test')
            self.assertEqual(document['collection']['Assessment']['jurisdiction'], 'BE-fictional')
            self.assertEqual(len((root / 'analytics.key').read_bytes()), 32)
            self.assertFalse(Path(document['root']).exists())
            self.assertEqual(set(document), {'root', 'privacyPath', 'anchorPath', 'backupKeyFile', 'collection'})
            for file in root.iterdir():
                self.assertEqual(file.stat().st_mode & 0o077, 0)
            with self.assertRaises(FileExistsError):
                installed.analytics_fixture_configuration(root, True)

    def test_external_nonprivate_or_invalid_fixture_requests_are_refused(self):
        with tempfile.TemporaryDirectory(prefix='mychef-installed-') as directory:
            root = Path(directory)
            for path, enabled in ((Path('/outside'), True), (Path('relative'), True), (root, 'approved')):
                with self.assertRaises((ValueError, FileNotFoundError)):
                    installed.analytics_fixture_configuration(path, enabled)
            root.chmod(0o755)
            with self.assertRaises(ValueError):
                installed.analytics_fixture_configuration(root, True)
            root.chmod(0o700)

    def test_fictional_approval_cannot_be_applied_to_existing_storage(self):
        with tempfile.TemporaryDirectory(prefix='mychef-installed-') as directory:
            root = Path(directory)
            (root / 'app.db').write_bytes(b'existing storage must not be touched')
            before = {path.name: path.read_bytes() for path in root.iterdir()}
            with self.assertRaises(FileExistsError):
                installed.analytics_fixture_configuration(root, True)
            self.assertEqual(before, {path.name: path.read_bytes() for path in root.iterdir()})


class NativeVerificationTest(unittest.TestCase):
    def test_android_groups_continue_on_failure_without_issuing_a_full_receipt(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "fixture-inbox.json").write_text(json.dumps({"capability": "x" * 43}))
            native_groups = []
            incomplete = True
            counts = {"core": 7, "media": 5, "pages": 1, "account": 4}
            page_check = "system recipe-page picker consent and source/dish separation"
            source = {"sourceCommit": "b" * 40}
            build = {"candidate": source, "artifactSha256": "c" * 64}

            def execute(*args, **kwargs):
                if args[0] == "bun":
                    group = args[-1]
                    native_groups.append(group)
                    if incomplete and group == "media":
                        raise RuntimeError("Synthetic picker failure")
                    (root / f"native-android-journeys-{group}.json").write_text(json.dumps({
                        "group": group, "passed": [page_check] if group == "pages"
                        else [f"{group} check {i}" for i in range(counts[group])],
                    }))
                return "Success"

            with patch.object(installed, "command", side_effect=execute), \
                    patch.object(installed, "ARTIFACTS", root), \
                    patch.dict(installed.ENV, {"ANDROID_HOME": "/synthetic-sdk"}), \
                    patch.object(probe, "build", return_value=root / "probe.apk"), \
                    patch.object(native_fixture_account, "seed", return_value={}), \
                    patch.object(installed, "android_request_diagnostics"), redirect_stdout(io.StringIO()):
                with self.assertRaisesRegex(RuntimeError, "groups failed: media"):
                    installed.android_journeys(root, build, {}, [])
                self.assertEqual(native_groups, ["core", "media", "pages", "account"])
                output = root / "installed-journeys.json"
                self.assertFalse(output.exists())
                incomplete = False
                installed.android_journeys(root, build, {}, [])
            result = json.loads(output.read_text())
            self.assertEqual(result["source"], source)
            self.assertEqual(result["artifactSha256"], build["artifactSha256"])
            self.assertEqual({group: len(names) for group, names in result["groups"].items()}, counts)
            self.assertIn(page_check, result["passed"])

    def test_wrong_simulator_archive_stops_before_a_device_is_opened(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            app = root / "App.app"
            app.mkdir()
            (app / "Info.plist").write_bytes(plistlib.dumps({
                "CFBundleIdentifier": installed.APP,
                "CFBundleSupportedPlatforms": ["iPhoneOS"],
            }))
            with patch.object(installed, "command") as command:
                with self.assertRaisesRegex(RuntimeError, "Unexpected simulator app identity"):
                    installed.ios(root, None, root / "fake-ca.pem", root / "fake-package.zip")
            command.assert_called_once_with("/usr/bin/ditto", "-x", "-k", str(root / "fake-package.zip"), str(root))

    def test_ui_driver_uses_the_same_sdk_adb_as_the_installed_fixture(self):
        with patch.dict(probe.os.environ, {"ANDROID_HOME": "/synthetic-sdk"}), \
                patch.object(probe.subprocess, "check_output", return_value="") as output:
            probe._adb("shell", "dumpsys", "window", "displays")
        self.assertEqual(output.call_args.args[0], [
            "/synthetic-sdk/platform-tools/adb", "-s", "emulator-5554",
            "shell", "dumpsys", "window", "displays",
        ])

    def test_recover_only_the_focused_system_ui_dialog(self):
        displays = ("  mCurrentFocus=Window{42 u0 "
                    "Application Not Responding: com.android.systemui}")
        close = ('<node package="android" resource-id="android:id/aerr_close" '
                 'enabled="true" clickable="true" bounds="[20,40][120,80]"/>')
        adb = unittest.mock.Mock(side_effect=[displays, "", "", '<?xml version="1.0"?><hierarchy>' + close
                                              + "</hierarchy>", displays, ""])
        self.assertTrue(probe.dismiss_system_ui_anr(adb))
        adb.assert_called_with("shell", "input", "tap", "70", "60")

        for foreground in (
                "  mCurrentFocus=Window{42 u0 Application Not Responding: " + probe.APP + "}",
                "  mCurrentFocus=Window{42 u0 com.android.systemui/.SomeActivity}"):
            with self.subTest(foreground=foreground):
                adb = unittest.mock.Mock(return_value=foreground)
                self.assertFalse(probe.dismiss_system_ui_anr(adb))
                adb.assert_called_once_with("shell", "dumpsys", "window", "displays")

    def test_launch_failure_keeps_original_error_and_captures_device_evidence(self):
        output = io.StringIO()

        def fail_launch():
            raise RuntimeError("device refused launch")

        with redirect_stdout(output):
            with self.assertRaisesRegex(RuntimeError, "device refused launch"):
                installed.verify_stage(lambda: True, "native HTTPS session", [], [],
                                       lambda: print("device evidence captured"), launch=fail_launch)
        self.assertIn("device evidence captured", output.getvalue())

    def test_relaunch_timeout_retains_only_second_launch_diagnostics(self):
        output = io.StringIO()
        events = [("POST", "session", 201), ("POST", "refresh", 200)]
        tls = ["first launch TLS state", "second launch TLS state"]
        failure = RuntimeError("Timed out waiting for refresh after process relaunch")
        with patch.object(installed, "wait_for", side_effect=failure), redirect_stdout(output):
            with self.assertRaisesRegex(RuntimeError, "after process relaunch"):
                installed.verify_stage(lambda: False, "refresh after process relaunch", events, tls,
                                       lambda: print("device evidence captured"), launch=lambda: None,
                                       event_start=1, tls_start=1)
        self.assertIn("('POST', 'refresh', 200)", output.getvalue())
        self.assertIn("second launch TLS state", output.getvalue())
        self.assertIn("device evidence captured", output.getvalue())
        self.assertNotIn("('POST', 'session', 201)", output.getvalue())
        self.assertNotIn("first launch TLS state", output.getvalue())

    def test_retained_evidence_rejects_native_authority(self):
        for kind, payload in {
            "reset-link": "/app/reset-password#" + "a" * 43,
            "native-access": '{"accessToken":"' + "b" * 64 + '"}',
            "native-refresh": '{"refreshToken":"' + "c" * 64 + '"}',
            "native-bearer": "Authorization: Bearer " + "d" * 64,
            "proof-json": '{"proof":"' + "f" * 43 + '"}',
        }.items():
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                evidence = root / "evidence"
                evidence.mkdir()
                (evidence / "result.log").write_text(payload)
                with self.assertRaisesRegex(RuntimeError, "credential canary"):
                    menu.audit_credential_artifacts(evidence, root, root / "missing.db", "synthetic-password")

    def test_bundle_hashes_and_route_names_are_valid_evidence(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            evidence = root / "evidence"
            evidence.mkdir()
            (evidence / "result.json").write_text('{"bundleSha256":"' + "e" * 64 + '","path":"/app/reset-password"}')
            menu.audit_credential_artifacts(evidence, root, root / "missing.db", "synthetic-password")

    def test_source_check_runs_native_guards_without_hosted_execution(self):
        source = ROOT / "scripts/rewrite.py"
        spec = importlib.util.spec_from_file_location("native_wiring_rewrite", source)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        commands = []
        with patch.object(module, "environment", return_value={}), patch.object(sys, "argv", [str(source), "test"]), patch.object(module.subprocess, "run", side_effect=lambda argv, **kwargs: commands.append(list(argv))):
            module.main()
        bun_tests = [command for command in commands if command[:2] == ["bun", "test"]]
        self.assertEqual(len(bun_tests), 1)
        for name in ("mobile-build", "mobile-java", "mobile-delivery", "mobile-hosted"):
            self.assertIn(f"../scripts/{name}.test.ts", bun_tests[0])
        type_check = next(command for command in commands if "tsc" in " ".join(command))
        for name in ("mobile-build", "mobile-delivery", "mobile-hosted"):
            self.assertIn(f"../scripts/{name}.ts", type_check)
        self.assertIn(["python3", "scripts/test_native_verification.py"], commands)
        self.assertFalse(any("xcodebuild" in command or "gradlew" in command for command in commands))


class NativeFixtureProxyTest(unittest.TestCase):
    def test_offline_proxy_breaks_transport_instead_of_returning_an_http_error(self):
        module = ast.parse((ROOT / 'scripts/test_native_installed.py').read_text())
        fixture = next(node for node in module.body
                       if isinstance(node, ast.FunctionDef) and node.name == 'fixture')
        proxy = next(node for node in fixture.body
                     if isinstance(node, ast.ClassDef) and node.name == 'Proxy')
        with tempfile.TemporaryDirectory(prefix='mychef-offline-proxy-') as scratch:
            state = Path(scratch)
            (state / 'native-network-offline').touch()
            namespace = dict(http=http, re=re, state=state)
            exec(compile(ast.Module(body=[proxy], type_ignores=[]),
                         'actual-offline-fixture-proxy', 'exec'), namespace)
            with http.server.ThreadingHTTPServer(('127.0.0.1', 0), namespace['Proxy']) as server:
                worker = threading.Thread(target=lambda: server.serve_forever(poll_interval=.01), daemon=True)
                worker.start()
                try:
                    with closing(http.client.HTTPConnection(*server.server_address, timeout=2)) as client:
                        client.request('GET', '/api/native/v1/session')
                        with self.assertRaises(http.client.RemoteDisconnected):
                            client.getresponse()
                finally:
                    server.shutdown()
                    worker.join(timeout=2)

    def test_actual_proxy_preserves_native_headers_and_bounds_photo_response_loss(self):
        module = ast.parse((ROOT / 'scripts/test_native_installed.py').read_text())
        fixture = next(node for node in module.body
                       if isinstance(node, ast.FunctionDef) and node.name == 'fixture')
        proxy = next(node for node in fixture.body
                     if isinstance(node, ast.ClassDef) and node.name == 'Proxy')
        recorded = []
        upstream = []
        replies = []

        class Response:
            status = 202

            def __init__(self, status=None, body=b'{}'):
                if status is not None:
                    self.status = status
                self.body = body

            def read(self, size):
                return self.body[:size]

            def getheader(self, name):
                return 'application/json' if name == 'Content-Type' else None

        class Client:
            def __init__(self, *args, **kwargs):
                pass

            def request(self, method, path, body=None, headers=None):
                recorded.append(dict(headers))
                upstream.append((method, path, body))

            def getresponse(self):
                return replies.pop(0) if replies else Response()

            def close(self):
                pass

        with tempfile.TemporaryDirectory(prefix='mychef-review-proxy-') as scratch:
            namespace = dict(
                http=types.SimpleNamespace(
                    server=http.server,
                    client=types.SimpleNamespace(HTTPConnection=Client)),
                re=re, state=Path(scratch), closing=closing, events=[], PLATFORM="android",
                journeys=False, json=json, shopping_lock=threading.Lock(),
                shopping_sync={})
            exec(compile(ast.Module(body=[proxy], type_ignores=[]),
                         'actual-installed-fixture-proxy', 'exec'), namespace)
            handler = namespace['Proxy'].__new__(namespace['Proxy'])
            handler.path = '/api/native/v1/generations'
            handler.command = 'POST'
            handler.rfile = io.BytesIO(b'{}')
            handler.wfile = io.BytesIO()
            handler.headers = Message()
            expected = {
                'Authorization': 'Bearer synthetic',
                'Content-Type': 'application/json',
                'Accept': 'application/json',
                'Idempotency-Key': 'synthetic-native-operation',
                'X-MyChef-Analytics-Revision': '7',
                'If-Match': 'synthetic-version',
                'If-None-Match': 'synthetic-previous-version',
            }
            for name, value in {'Content-Length': '2', **expected}.items():
                handler.headers[name] = value
            handler.send_response = lambda *args: None
            sent_headers = {}
            handler.send_header = lambda name, value: sent_headers.update({name: value})
            handler.end_headers = lambda: None
            handler.forward()
            self.assertEqual(len(recorded), 1)
            self.assertEqual(sent_headers.get("Connection"), "close")
            self.assertTrue(handler.close_connection)
            for name, value in expected.items():
                with self.subTest(header=name):
                    self.assertEqual(recorded[0].get(name), value)
            # The photo retry fault must happen after an upstream success and
            # affect exactly one response, without forwarding an HTTP error.
            marker = Path(scratch) / 'native-personal-save-drop'
            marker.touch()
            namespace['journeys'] = True
            Response.status = 201
            handler.path = '/api/native/v1/personal-recipes'
            handler.rfile = io.BytesIO(b'{}')
            handler.wfile = io.BytesIO()
            sent = []
            handler.send_response = lambda status: sent.append(status)
            handler.forward()
            self.assertEqual(len(recorded), 2)
            self.assertFalse(marker.exists())
            self.assertEqual(sent, [])
            self.assertTrue(handler.close_connection)
            handler.close_connection = False
            handler.rfile = io.BytesIO(b'{}')
            handler.forward()
            self.assertEqual(sent, [201])
            self.assertEqual(handler.wfile.getvalue(), b'{}')
            # A concurrent actor update must advance the real upstream list
            # before the original stale PUT is sent unchanged.
            handler.path = '/api/native/v1/shopping/2026-11-09'
            handler.command = 'PUT'
            handler.rfile = io.BytesIO(b'{}')
            handler.wfile = io.BytesIO()
            sent.clear()
            (Path(scratch) / 'native-shopping-conflict').touch()
            replies.extend([Response(200, b'{"revision":0,"items":[]}'), Response(200), Response(409)])
            handler.forward()
            self.assertEqual([method for method, _, _ in upstream[-3:]], ['GET', 'PUT', 'PUT'])
            self.assertEqual(json.loads(upstream[-2][2])['items'][0]['name'], 'Native remote salt')
            self.assertEqual(upstream[-1][2], b'{}')
            self.assertEqual(sent, [409])

            # A loading/disabled checkbox alone is not a server result. Only
            # the real unavailable notice can corroborate the OS UI probe.
            handler.path = '/api/native/v1/analytics/notice'
            handler.command = 'GET'
            handler.rfile = io.BytesIO()
            handler.wfile = io.BytesIO()
            replies.append(Response(200, b'{"available":false,"defaultEnabled":false}'))
            handler.forward()
            self.assertEqual(namespace['events'][-1], ('GET', 'analytics-notice-disabled', 200))
            handler.rfile = io.BytesIO()
            handler.wfile = io.BytesIO()
            replies.append(Response(200, b'{"available":true,"defaultEnabled":true}'))
            handler.forward()
            self.assertEqual(namespace['events'][-1], ('GET', 'feature', 200))


class NativeSystemPickerTest(unittest.TestCase):
    def test_cancel_repeats_back_only_while_the_system_picker_is_still_active(self):
        windows = iter(['com.android.documentsui/.Pick', '', 'com.android.documentsui/.Pick', probe.APP + '/.Main'])
        commands = []

        def output(*argv):
            commands.append(argv)
            if 'dumpsys' in argv:
                window = next(windows)
                return f'mCurrentFocus=Window{{42 u0 {window}}}' if window else 'mCurrentFocus=null'
            return ''

        with patch.object(probe, '_adb', side_effect=output), patch.object(probe.time, 'sleep'), \
                patch.object(probe, '_snapshot', side_effect=probe.subprocess.CalledProcessError(1, 'dump')) as dump:
            probe.cancel_picker()
        self.assertEqual(sum(argv[-3:] == ('input', 'keyevent', '4') for argv in commands), 2)
        dump.assert_not_called()

    def test_cancel_waits_for_the_system_picker_then_returns_to_mychef(self):
        windows = iter([probe.APP + '/.Main', 'com.google.android.documentsui/.Pick', probe.APP + '/.Main'])
        commands = []

        def output(*argv):
            commands.append(argv)
            if 'dumpsys' in argv:
                return f'mCurrentFocus=Window{{42 u0 {next(windows)}}}'
            return ''

        with patch.object(probe, '_adb', side_effect=output), patch.object(probe.time, 'sleep'):
            probe.cancel_picker()
        back = next(i for i, argv in enumerate(commands) if argv[-3:] == ('input', 'keyevent', '4'))
        self.assertEqual(sum('dumpsys' in argv for argv in commands[:back]), 2)
        self.assertEqual(sum('dumpsys' in argv for argv in commands[back:]), 1)


if __name__ == "__main__":
    unittest.main()
