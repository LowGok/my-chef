"""Offline native evidence and ordinary-check wiring contracts; standard library only."""
from __future__ import annotations

import ast
import http.server
import json
import re
import threading
import types
from contextlib import closing
import hashlib
import os
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


class NativeVerificationTest(unittest.TestCase):
    def test_ios_notification_actions_reach_the_same_handler_on_scene_connection(self):
        # The simulator fixture injects an opaque reference after launch and
        # cannot detect a missing UIKit cold-start callback.
        ios = ROOT / "mobile/ios/App/App"
        with (ios / "Info.plist").open("rb") as file:
            manifest = plistlib.load(file)["UIApplicationSceneManifest"]
        self.assertIn("UIWindowSceneSessionRoleApplication", manifest["UISceneConfigurations"])
        scene = (ios / "SceneDelegate.swift").read_text()
        app = (ios / "AppDelegate.swift").read_text()
        self.assertRegex(scene, r"receiveReminderResponse\(connectionOptions\.notificationResponse\)")
        self.assertRegex(app, r"receiveReminderResponse\(response\)")

    def test_ios_reminder_journey_requires_the_rendered_week_and_controls(self):
        with tempfile.TemporaryDirectory() as temporary:
            result = Path(temporary) / "mychef-reminder-result.json"
            self.assertFalse(installed.ios_reminder_ui_passed(result))
            for value in ({"weekAndMeal": True}, {"weekAndMeal": True, "scaledControls": False}):
                result.write_text(json.dumps(value))
                self.assertFalse(installed.ios_reminder_ui_passed(result))
            result.write_text('{"weekAndMeal":true,"scaledControls":true}')
            self.assertFalse(installed.ios_reminder_ui_passed(result))
            result.write_text('{"weekAndMeal":true,"scaledControls":true,"systemTextSize":false}')
            self.assertFalse(installed.ios_reminder_ui_passed(result))
            result.write_text('{"weekAndMeal":true,"scaledControls":true,"systemTextSize":true}')
            self.assertTrue(installed.ios_reminder_ui_passed(result))

    def test_subscription_install_authorization_requires_public_pr_and_exact_artifact(self):
        source = "b" * 40
        repo = {"id": 1454481532, "full_name": "LowGok/my-chef", "private": False}
        event = {
            "action": "labeled", "number": 41, "repository": repo, "sender": {"login": "LowGok"},
            "label": {"name": "native-subscription-fixture"},
            "pull_request": {"number": 41, "base": {"ref": "main"},
                             "head": {"ref": "feat/subscription-billing", "repo": repo, "sha": source}},
        }
        self.assertTrue(installed.subscription_authorized(event, "41", "ios-simulator", source))
        self.assertTrue(installed.subscription_authorized(event, "41", "android", source))
        self.assertFalse(installed.subscription_authorized(event, "41", "ios-device", source))
        self.assertFalse(installed.subscription_authorized(event, "41", "android", "a" * 40))
        self.assertFalse(installed.subscription_authorized(event, "", "android", source))
        self.assertFalse(installed.subscription_authorized(event, "42", "android", source))
        for changed in [
            {**event, "repository": {**repo, "private": True}},
            {**event, "sender": {"login": "another-owner"}},
            {**event, "label": {"name": "native-reminder-fixture"}},
            {**event, "pull_request": {**event["pull_request"], "number": 42}},
            {**event, "pull_request": {**event["pull_request"], "head": {**event["pull_request"]["head"], "repo": {**repo, "id": 1}}}},
        ]:
            self.assertFalse(installed.subscription_authorized(changed, "41", "android", source))

    def test_subscription_installed_run_dispatches_the_signed_in_journey(self):
        source = "b" * 40
        repo = {"id": 1454481532, "full_name": "LowGok/my-chef", "private": False}
        event = {
            "action": "labeled", "number": 41, "repository": repo, "sender": {"login": "LowGok"},
            "label": {"name": "native-subscription-fixture"},
            "pull_request": {"number": 41, "base": {"ref": "main"},
                             "head": {"ref": "feat/subscription-billing", "repo": repo, "sha": source}},
        }
        for platform, name in (("android", "mychef-compile.apk"), ("ios-simulator", "mychef-compile-simulator.app.zip")):
            with self.subTest(platform=platform), tempfile.TemporaryDirectory() as directory:
                folder = Path(directory)
                (folder / "event.json").write_text(json.dumps(event))
                sample = b"disposable installed candidate"
                (folder / name).write_bytes(sample)
                (folder / "compilation.json").write_text(json.dumps({
                    "candidate": {"sourceCommit": source}, "platform": platform,
                    "artifact": name, "artifactSha256": hashlib.sha256(sample).hexdigest(),
                }))
                environment = {
                    "GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": "LowGok/my-chef",
                    "GITHUB_EVENT_NAME": "pull_request", "GITHUB_EVENT_PATH": str(folder / "event.json"),
                    "NATIVE_SUBSCRIPTION_PR": "41", "NATIVE_ANDROID_JOURNEYS": "1",
                }
                noop = lambda: None
                device = ({"fixture": True}, noop, noop, noop)
                with patch.dict(os.environ, environment), patch.object(installed, "ARTIFACTS", folder), \
                        patch.object(installed, "PLATFORM", platform), patch.object(installed, "command", return_value=source), \
                        patch.object(installed, "fixture", return_value=("fake-ca", [], [])) as backend, \
                        patch.object(installed, "android", return_value=device), \
                        patch.object(installed, "ios", return_value=(*device, noop, noop)), \
                        patch.object(installed, "verify_stage"), \
                        patch.object(installed, "android_journeys") as android_check, \
                        patch.object(installed, "ios_billing_journey") as ios_check, redirect_stdout(io.StringIO()):
                    installed.main()
                self.assertTrue(backend.call_args.kwargs["billing"])
                if platform == "android":
                    android_check.assert_called_once()
                    self.assertIs(android_check.call_args.kwargs["billing"], True)
                    ios_check.assert_not_called()
                else:
                    ios_check.assert_called_once()
                    android_check.assert_not_called()

    def test_public_billing_receipt_filters_raw_metadata_and_requires_both_journeys(self):
        from verify_native_subscription_evidence import evidence

        source = "b" * 40
        candidate = {
            "sourceCommit": source, "appId": "com.gkldevelopment.mychef.preview",
            "environment": "preview", "version": "0.1.0", "build": 2,
            "bundleSha256": "c" * 64, "dependenciesSha256": "d" * 64,
            "nativeResolutionRecorded": True, "nativeResolutionVerified": False,
        }
        smoke_steps = ["install and launch", "native session over verified HTTPS", "refresh after process relaunch"]
        for platform, name, model, checks in (
            ("android", "mychef-compile.apk", {"api": "33", "privateNotes": "unused metadata"},
             ["Play simulated purchase has accessible terms", "Play restores the lost simulated result before completion"]),
            ("ios-simulator", "mychef-compile-simulator.app.zip",
             {"runtime": "com.apple.CoreSimulator.SimRuntime.iOS-18-6", "model": "iPhone 16"},
             ["enlarged price controls", "verified simulated Apple purchase", "simulated Apple restore"]),
        ):
            with self.subTest(platform=platform), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                binary = b"synthetic fixture"
                (root / name).write_bytes(binary)
                digest = hashlib.sha256(binary).hexdigest()
                compile_data = {"candidate": candidate, "platform": platform, "artifact": name,
                                "artifactSha256": digest, "compilation": "passed",
                                "dependencyVerification": "locked", "distribution": "not authorized",
                                "tool": "unfiltered compiler information"}
                run = {"source": candidate, "artifactSha256": digest, "device": model}
                smoke = {**run, "platform": platform, "passed": smoke_steps}
                journey = {**run, "groups": {"billing": checks}, "passed": checks}
                for file, value in (("compilation.json", compile_data), ("installed-smoke.json", smoke),
                                    ("installed-journeys.json", journey)):
                    (root / file).write_text(json.dumps(value))
                value = evidence(root, platform, source)
                self.assertEqual(value["sourceCommit"], source)
                self.assertEqual(value["artifactSha256"], digest)
                self.assertEqual(value["billing"], checks)
                self.assertNotIn("privateNotes", json.dumps(value))
                self.assertNotIn("compiler information", json.dumps(value))
                for invalid in (
                    {**journey, "passed": checks[:-1]},
                    {**journey, "artifactSha256": "a" * 64},
                    {**journey, "notes": "test-account@example.test"},
                    {**journey, "notes": "fixture." + "a" * 32 + "." + "b" * 43},
                    {**journey, "notes": "https://private-preview.taildemo.ts.net:8977"},
                    {**journey, "notes": "Authorization: Bearer syntheticprivate"},
                ):
                    (root / "installed-journeys.json").write_text(json.dumps(invalid))
                    with self.assertRaises(ValueError):
                        evidence(root, platform, source)
                (root / "installed-journeys.json").write_text(json.dumps(journey))
                with self.assertRaises(ValueError):
                    evidence(root, platform, "a" * 40)
                (root / name).write_bytes(binary + b"tampered")
                with self.assertRaises(ValueError):
                    evidence(root, platform, source)

    def test_ios_billing_receipt_is_exact_and_never_accepts_proofs(self):
        routes = [("POST", "billing/checkouts", 201), ("POST", "billing/launch", 204),
                  ("POST", "billing/verify", 200), ("POST", "billing/restore", 200)]
        accepted = {"terms": True, "checkout": True, "restore": True}
        self.assertTrue(installed.ios_billing_verified(accepted, routes))
        self.assertFalse(installed.ios_billing_verified({**accepted, "receipt": "synthetic"}, routes))
        self.assertFalse(installed.ios_billing_verified({**accepted, "terms": False}, routes))
        self.assertFalse(installed.ios_billing_verified({**accepted, "terms": 1}, routes))
        self.assertFalse(installed.ios_billing_verified(accepted, routes[:-1]))
        self.assertFalse(installed.ios_billing_verified(accepted, [routes[0], routes[2], routes[1], routes[3]]))

    def test_android_reminder_text_restores_the_emulator_setting_on_failure(self):
        for initial in ("null", "1.15"):
            with self.subTest(initial=initial):
                scale = initial
                changes = []

                def adb(*args):
                    nonlocal scale
                    if args == ("shell", "settings", "get", "system", "font_scale"):
                        return scale
                    changes.append(args)
                    scale = args[-1] if args[2] == "put" else "null"
                    return ""

                with self.assertRaisesRegex(RuntimeError, "synthetic journey failure"):
                    with installed.android_reminder_text(adb):
                        self.assertEqual(scale, "1.5")
                        raise RuntimeError("synthetic journey failure")
                self.assertEqual(scale, initial)
                self.assertEqual(changes, [
                    ("shell", "settings", "put", "system", "font_scale", "1.5"),
                    ("shell", "settings", "delete", "system", "font_scale") if initial == "null" else
                    ("shell", "settings", "put", "system", "font_scale", initial),
                ])

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

    def test_android_reminder_permission_precedes_separate_large_text_session(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "fixture-inbox.json").write_text(json.dumps({"capability": "x" * 43}))
            scale = "1.0"
            cleared = False
            events = []
            checked = []
            installation = [("DELETE", "planning-reminders/installation", 204),
                            ("PUT", "planning-reminders/installation", 204),
                            ("DELETE", "planning-reminders/installation", 204)]
            baseline = ["native reminders start disabled and require explicit consent",
                        "installed app registers only after notification permission and removes on opt out"]
            text = "Android system large text and reminder accessibility nodes"

            def execute(*args, **kwargs):
                nonlocal cleared, scale
                if args[0] == "bun":
                    group = args[-1]
                    checked.append((group, scale, cleared))
                    events.extend(installation if group == "reminders" else [])
                    (root / f"native-android-journeys-{group}.json").write_text(json.dumps({
                        "group": group, "passed": baseline if group == "reminders" else [text],
                    }))
                elif args[4:6] == ("pm", "clear"):
                    cleared = True
                elif args[4:6] == ("settings", "get"):
                    return scale
                elif args[4:6] == ("settings", "put"):
                    scale = args[-1]
                return "Success"

            build = {"candidate": {}, "artifactSha256": "c" * 64}
            with patch.object(installed, "command", side_effect=execute), \
                    patch.object(installed, "ARTIFACTS", root), \
                    patch.dict(installed.ENV, {"ANDROID_HOME": "/synthetic-sdk"}), \
                    patch.object(probe, "build", return_value=root / "probe.apk"), \
                    patch.object(native_fixture_account, "seed", return_value={}), \
                    patch.object(installed, "android_request_diagnostics"), redirect_stdout(io.StringIO()):
                installed.android_journeys(root, build, {}, events, reminders=True)
            self.assertEqual(checked, [("reminders", "1.0", False),
                                       ("reminder-text", "1.5", True)])
            self.assertEqual(scale, "1.0")
            result = json.loads((root / "installed-journeys.json").read_text())
            self.assertEqual(result["groups"], {"reminders": baseline + [text]})

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
                journeys=False, billing=False, json=json, shopping_lock=threading.Lock(),
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

            namespace['billing'] = True
            Response.status = 200
            handler.path = '/api/native/v1/billing/checkouts/' + 'd' * 36 + '/verify'
            handler.command = 'POST'
            handler.rfile = io.BytesIO(b'{}')
            handler.forward()
            self.assertEqual(namespace['events'][-1], ('POST', 'billing/verify', 200))
            self.assertNotIn('d' * 36, repr(namespace['events']))


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
