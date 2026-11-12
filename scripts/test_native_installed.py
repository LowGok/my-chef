"""Exercise the real native session bridge on disposable hosted devices."""
from contextlib import ExitStack, closing
import hashlib
import http.client
import http.server
import json
import os
from pathlib import Path
import plistlib
import re
import shutil
import ssl
import subprocess
import sys
import tempfile
import threading
import time

from e2e_server import fixture_email, fixture_imports

ROOT = Path(__file__).resolve().parent.parent
GENERATED = ROOT / "mobile/.generated"
ARTIFACTS = GENERATED / "artifacts"
APP = "com.gkldevelopment.mychef.preview"
PLATFORM = os.environ.get("NATIVE_PLATFORM")
ORIGIN = "https://localhost:8977"
ENV = {k: v for k, v in os.environ.items() if k in {
    "PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "LANG", "ANDROID_HOME", "ANDROID_SDK_ROOT"
}}
ENV.update(DEVELOPER_DIR="/Applications/Xcode_26.3.app/Contents/Developer", GOTOOLCHAIN="go1.27.4", CGO_ENABLED="0")


def command(*argv, timeout=90, data=None):
    result = subprocess.run(argv, cwd=ROOT, env=ENV, input=data, text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout)
    if result.returncode:
        raise RuntimeError(f"{Path(argv[0]).name} {argv[1]} failed: {result.stdout[-3000:]}")
    return result.stdout.strip()


def wait_for(check, description, seconds=100):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        if check():
            return
        time.sleep(1)
    raise RuntimeError(f"Timed out waiting for {description}")


def verify_stage(check, description, events, rejected_tls, diagnostics, *, launch, event_start=0, tls_start=0):
    try:
        launch()
        wait_for(check, description)
    except (RuntimeError, subprocess.SubprocessError):
        print(f"Synthetic session response statuses: {events[event_start:][-12:]}", flush=True)
        print(f"Fixture TLS rejection reasons: {sorted(set(rejected_tls[tls_start:]))}", flush=True)
        try:
            diagnostics()
        except (OSError, RuntimeError, subprocess.SubprocessError):
            print("Device diagnostics could not be collected", flush=True)
        raise


def terminate(process):
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=15)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()


def android_request_diagnostics(adb_path):
    logs = subprocess.run([adb_path, "-s", "emulator-5554", "logcat", "-d", "-s", "MyChefNativeCheck:I", "*:S"],
                          env=ENV, capture_output=True, text=True, timeout=20)
    allowed = re.compile(
        r"(?:session (?:GET|POST)|profile PUT|favorite-week GET|menus GET|generations POST) "
        r"(?:bridge|rejected|queued|io-(?:tls|connect|timeout|eof|protocol|socket|other)|response(?:-rejected)?|enqueue-failure)$"
    )
    stages = [match.group() for line in logs.stdout.splitlines() if (match := allowed.search(line))]
    print(f"Disposable Android bridge stages: {stages[-24:]}", flush=True)


def fixture(state, stack, journeys=False):
    print("Building disposable Go fixture", flush=True)
    flags = []
    if sys.platform == "darwin":
        # This fixture deliberately never runs the Linux-only import worker.
        # Refuse its listener on macOS; leave the real source and UID checks alone.
        stub = state / "worker_socket_fixture.go"
        stub.write_text('package imports\nimport "net"\n'
                        'func ListenPrivateWorker(string, uint32) (net.Listener, error) {\n'
                        'return nil, ErrExtractionUnavailable\n}\n')
        overlay = state / "go-overlay.json"
        overlay.write_text(json.dumps({"Replace": {str(ROOT / "internal/imports/worker_socket.go"): str(stub)}}))
        flags = ["-overlay", str(overlay)]
    for binary in ("mychef", "mychef-migrate"):
        command("go", "build", *flags, "-trimpath", "-o", str(state / binary), "./cmd/" + binary, timeout=450)
    email, _ = fixture_email(state)
    db = str(state / "app.db")
    app_env = dict(ENV, **email, MYCHEF_ADDR="127.0.0.1:18137", MYCHEF_ORIGIN=ORIGIN,
                   MYCHEF_DB=db, MYCHEF_LIFECYCLE_DB=db + ".lifecycle", MYCHEF_DEV="1",
                   MYCHEF_PROVIDER="fixture", MYCHEF_E2E="1", MYCHEF_FEEDBACK_TO="operator@example.test")
    if journeys:
        app_env.update(stack.enter_context(fixture_imports(ROOT, state, app_env)))
    subprocess.run([str(state / "mychef-migrate"), "up", "--db", db], env=app_env, check=True, timeout=60)
    log = stack.enter_context((state / "server.log").open("w"))
    app = subprocess.Popen([str(state / "mychef")], cwd=ROOT, env=app_env, stdout=log, stderr=log)
    stack.callback(terminate, app)

    def ready():
        if app.poll() is not None:
            messages = []
            for line in (state / "server.log").read_text(errors="replace").splitlines()[-8:]:
                try:
                    item = json.loads(line)
                except ValueError:
                    continue
                message = item.get("msg", "")
                if item.get("level") == "ERROR" and re.fullmatch(r"[A-Za-z0-9 ;,-]{1,120}", message):
                    messages.append(message)
            raise RuntimeError("Disposable app stopped during startup: " + ("; ".join(messages) or "no startup error recorded"))
        try:
            with closing(http.client.HTTPConnection("127.0.0.1", 18137, timeout=2)) as client:
                client.request("GET", "/healthz")
                return client.getresponse().status == 200
        except OSError:
            return False
    wait_for(ready, "disposable Go backend", 35)
    events = []
    rejected_tls = []
    shopping_sync = {}
    shopping_lock = threading.Lock()

    class Proxy(http.server.BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, *_):
            pass  # No request headers, bodies, tokens or proofs in evidence.

        def advance_shopping_revision(self, client, headers):
            # Same disposable actor, same loopback server. Preserve the
            # original request below so its now-stale revision is rejected.
            client.request("GET", self.path, headers=headers)
            result = client.getresponse()
            body = result.read(1_048_577)
            if result.status != 200 or len(body) > 1_048_576:
                return False
            latest = json.loads(body)
            latest["items"].append({"id": "native-remote-salt", "name": "Native remote salt",
                                    "state": "buy", "manual": True, "contributions": []})
            client.request("PUT", self.path, json.dumps(latest).encode(), headers)
            saved = client.getresponse()
            return saved.status == 200 and len(saved.read(1_048_577)) <= 1_048_576

        def forward(self):
            if not re.fullmatch(r"/api/native/v1/[A-Za-z0-9_/?=&%.+~-]{1,4096}", self.path):
                self.send_error(404)
                return
            if (state / "native-network-offline").exists():
                # Loopback remains reachable through ADB with radios disabled.
                # Drop the connection so the native client sees transport loss.
                self.close_connection = True
                return
            size = int(self.headers.get("Content-Length", "0"))
            if not 0 <= size <= 24 * 1024 * 1024:
                self.send_error(413)
                return
            request_body = self.rfile.read(size)
            with closing(http.client.HTTPConnection("127.0.0.1", 18137, timeout=8)) as client:
                headers = {k: self.headers[k] for k in ("Authorization", "Content-Type", "Cookie", "Accept", "Idempotency-Key", "If-Match", "If-None-Match") if k in self.headers}
                if journeys and self.command == "PUT" and re.fullmatch(r"/api/native/v1/shopping/\d{4}-\d{2}-\d{2}", self.path):
                    try:
                        (state / "native-shopping-conflict").unlink()
                    except FileNotFoundError:
                        pass
                    else:
                        if not self.advance_shopping_revision(client, headers):
                            self.send_error(502)
                            return
                client.request(self.command, self.path, request_body, headers)
                response = client.getresponse()
                body = response.read(32 * 1024 * 1024 + 1)
                if len(body) > 32 * 1024 * 1024:
                    self.send_error(502)
                    return
                if journeys and self.command == "POST" and self.path == "/api/native/v1/personal-recipes" and response.status in (200, 201):
                    try:
                        (state / "native-personal-save-drop").unlink()
                    except FileNotFoundError:
                        pass
                    else:
                        # The server committed the operation. Lose only this
                        # response so the actual native client must retry it.
                        self.close_connection = True
                        return
                self.send_response(response.status)
                for name in ("Content-Type", "Content-Disposition", "ETag", "X-MyChef-User", "Retry-After"):
                    if response.getheader(name):
                        self.send_header(name, response.getheader(name))
                self.send_header("Content-Length", str(len(body)))
                if PLATFORM == "android":
                    # ADB reverse can leave an idle pooled socket stale after
                    # the fixture's bounded read timeout closes it. Announce
                    # closure while the complete response is still available.
                    self.send_header("Connection", "close")
                    self.close_connection = True
                self.end_headers()
                self.wfile.write(body)
                # Fixed route groups only: no IDs, query values or response bodies.
                route = self.path.split("?", 1)[0].removeprefix("/api/native/v1/")
                event_kind = route if route in ("session", "refresh", "login", "profile", "menus", "generations", "personal-recipes", "feedback") else "feature"
                if re.fullmatch(r"favorite-weeks/\d{4}-\d{2}-\d{2}", route):
                    event_kind = "favorite-week"
                events.append((self.command, event_kind, response.status))
                if journeys and self.command == "PUT" and response.status == 200 and re.fullmatch(r"/api/native/v1/shopping/\d{4}-\d{2}-\d{2}", self.path):
                    value = json.loads(request_body)
                    names = [item.get("name") for item in value.get("items", [])]
                    with shopping_lock:
                        for name in ("Native offline coffee", "Native offline tea"):
                            if names.count(name) == 1:
                                shopping_sync[name] = self.path
                        both = len(shopping_sync) == 2 and len(set(shopping_sync.values())) == 2
                        (state / "native-shopping-synced.json").write_text(json.dumps({"bothWeeks": both}, separators=(",", ":")))
        do_POST = do_GET = do_PUT = do_PATCH = do_DELETE = forward

    ca, key, cert = (str(state / name) for name in ("ca.pem", "leaf.key", "leaf.pem"))
    ca_key, csr = str(state / "ca.key"), str(state / "leaf.csr")
    ca_config = state / "ca.cnf"
    ca_config.write_text("[req]\ndistinguished_name=dn\nx509_extensions=root\n[dn]\n"
                         "[root]\nbasicConstraints=critical,CA:TRUE\n"
                         "keyUsage=critical,keyCertSign,cRLSign\nsubjectKeyIdentifier=hash\n")
    command("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", ca_key, "-out", ca,
            "-days", "1", "-sha256", "-config", str(ca_config), "-subj", "/CN=MyChef disposable device fixture")
    command("openssl", "req", "-newkey", "rsa:2048", "-nodes", "-keyout", key, "-out", csr, "-subj", "/CN=localhost")
    ext = state / "leaf.ext"
    ext.write_text("basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\n"
                   "extendedKeyUsage=serverAuth\nauthorityKeyIdentifier=keyid\nsubjectKeyIdentifier=hash\n"
                   "subjectAltName=DNS:localhost,IP:127.0.0.1\n")
    command("openssl", "x509", "-req", "-in", csr, "-CA", ca, "-CAkey", ca_key, "-CAcreateserial", "-out", cert,
            "-days", "1", "-sha256", "-extfile", str(ext))
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(cert, key)

    class FixtureHTTPS(http.server.ThreadingHTTPServer):
        def get_request(self):
            raw, peer = super().get_request()
            raw.settimeout(5)
            try:
                return context.wrap_socket(raw, server_side=True), peer
            except ssl.SSLError as exc:
                rejected_tls.append(exc.reason or "unspecified")
                raw.close()
                raise
            except OSError:
                raw.close()
                raise

    server = FixtureHTTPS(("127.0.0.1", 8977), Proxy)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    stack.callback(server.server_close)
    stack.callback(server.shutdown)
    return ca, events, rejected_tls


def android(state, stack, ca, apk):
    from native_android_probe import SYSTEM_UI_ANR, dismiss_system_ui_anr, focused_window

    print("Booting Android API 33 emulator", flush=True)
    sdk = Path(ENV["ANDROID_HOME"])
    manager = str(sdk / "cmdline-tools/latest/bin/sdkmanager")
    avdmanager = str(sdk / "cmdline-tools/latest/bin/avdmanager")
    image = "system-images;android-33;google_apis;x86_64"
    subprocess.run(["sudo", "chmod", "0666", "/dev/kvm"], check=True, timeout=20)
    # The hosted image includes several large NDKs. The Java-only APK is
    # already built, and neither its emulator nor apksigner uses an NDK.
    print(f"Android fixture disk before cleanup: {shutil.disk_usage(sdk).free // 1024**2} MiB", flush=True)
    installed = command(manager, "--list_installed", timeout=90)
    ndks = re.findall(r"^\s*(ndk;[0-9.]+)\s*\|", installed, flags=re.MULTILINE)
    if ndks:
        command(manager, "--uninstall", *ndks, timeout=180)
    print(f"Android fixture disk after cleanup: {shutil.disk_usage(sdk).free // 1024**2} MiB", flush=True)
    command(manager, "--install", image, timeout=240)
    command(avdmanager, "create", "avd", "--name", "mychef-ci", "--package", image, data="no\n")
    adb_path = str(sdk / "platform-tools/adb")
    emu_path = str(sdk / "emulator/emulator")
    print(command(emu_path, "-accel-check"), flush=True)
    boot_log = state / "emulator.log"
    boot_output = stack.enter_context(boot_log.open("w"))
    subprocess.run([adb_path, "start-server"], env=ENV, check=True, timeout=25,
                   stdout=boot_output, stderr=boot_output)
    emulator = subprocess.Popen([emu_path, "-avd", "mychef-ci", "-port", "5554", "-no-window", "-no-audio",
                                "-no-snapshot", "-writable-system", "-gpu", "software", "-no-boot-anim",
                                "-camera-back", "none", "-memory", "2048", "-partition-size", "2048"], env=ENV,
                                stdout=boot_output, stderr=boot_output)
    stack.callback(terminate, emulator)
    adb = lambda *args: command(adb_path, "-s", "emulator-5554", *args)

    def attached():
        if emulator.poll() is not None:
            raise RuntimeError("Emulator process exited before attaching")
        return "emulator-5554\tdevice" in command(adb_path, "devices")

    try:
        wait_for(attached, "Android ADB transport", 180)
        wait_for(lambda: adb("shell", "getprop", "sys.boot_completed") == "1", "Android boot", 150)
    except RuntimeError:
        print(boot_log.read_text(errors="replace")[-5000:].replace(str(ROOT), "[workspace]"), flush=True)
        raise
    print("Android booted; installing the fixture CA", flush=True)
    adb("root")
    adb("wait-for-device")
    remount = adb("remount")
    if "remount succeeded" not in remount.lower():
        adb("disable-verity")
        adb("reboot")
        adb("wait-for-device")
        wait_for(lambda: adb("shell", "getprop", "sys.boot_completed") == "1", "Android remount reboot", 120)
        adb("root")
        adb("wait-for-device")
        adb("remount")
    fingerprint = command("openssl", "x509", "-in", ca, "-subject_hash_old", "-noout")
    if not re.fullmatch(r"[a-f0-9]{8}", fingerprint):
        raise RuntimeError("Unexpected fixture CA name")
    target = f"/system/etc/security/cacerts/{fingerprint}.0"
    adb("push", ca, target)
    adb("shell", "chmod", "644", target)
    adb("shell", "restorecon", target)
    adb("reverse", "tcp:8977", "tcp:8977")
    adb("install", str(apk))
    # Instrumentation sends real input, so a background WebView is insufficient.
    # Keep this disposable emulator's screen on for the bounded 25-minute job.
    adb("shell", "settings", "put", "system", "screen_off_timeout", "1800000")
    if adb("shell", "settings", "get", "system", "screen_off_timeout") != "1800000":
        raise RuntimeError("Disposable Android display timeout was not applied")
    version = adb("shell", "getprop", "ro.build.version.sdk")
    webview = next((x.strip() for x in adb("shell", "dumpsys", "webviewupdate").splitlines() if "Current WebView package" in x), "unreported")
    signature = command(str(sdk / "build-tools/36.0.0/apksigner"), "verify", "--print-certs", str(apk))
    metadata = {"api": version, "systemImage": image, "webView": webview,
                "signing": next(line for line in signature.splitlines() if "certificate SHA-256 digest:" in line)}
    system_ui_recovered = False

    def launch():
        adb("shell", "input", "keyevent", "KEYCODE_WAKEUP")
        adb("shell", "wm", "dismiss-keyguard")
        adb("shell", "am", "start", "-W", "-n", APP + "/.MainActivity")

        def focused():
            nonlocal system_ui_recovered
            window = focused_window(adb)
            if window == SYSTEM_UI_ANR and not system_ui_recovered and dismiss_system_ui_anr(adb):
                system_ui_recovered = True
                metadata["systemUiRecovered"] = True
                print("Closed one unresponsive System UI dialog on the disposable emulator", flush=True)
            return window.startswith(APP + "/") and not window.endswith(" EXITING")

        wait_for(focused, "MyChef Android window focus", 40)

    stop = lambda: adb("shell", "am", "force-stop", APP)

    def diagnostics():
        result = subprocess.run([adb_path, "-s", "emulator-5554", "exec-out", "screencap", "-p"],
                                env=ENV, capture_output=True, timeout=20)
        if result.returncode == 0 and result.stdout.startswith(b"\x89PNG\r\n\x1a\n"):
            (ARTIFACTS / "anonymous-launch.png").write_bytes(result.stdout)
        android_request_diagnostics(adb_path)

    return metadata, launch, stop, diagnostics


def ios(state, stack, ca, package):
    # Verify the archive before booting the simulator. The host Mach-O tools
    # do not need CoreSimulator, and can otherwise contend with its first boot.
    command("/usr/bin/ditto", "-x", "-k", str(package), str(state))
    app = state / "App.app"
    info = plistlib.loads((app / "Info.plist").read_bytes())
    if info.get("CFBundleIdentifier") != APP or info.get("CFBundleSupportedPlatforms") != ["iPhoneSimulator"]:
        raise RuntimeError("Unexpected simulator app identity")
    access_group = "MYCHEF0000." + APP
    binary = app / "App"
    sections = command("otool", "-l", str(binary))
    expected = (GENERATED / "simulator-keychain.plist").read_bytes()
    if (not re.search(r"sectname __entitlements\s+segname __TEXT\b", sections) or expected not in binary.read_bytes()
            or plistlib.loads(expected) != {"application-identifier": access_group, "keychain-access-groups": [access_group]}):
        raise RuntimeError("Unexpected simulator Keychain identity")
    print("Verified the linked simulator Keychain identity", flush=True)

    simctl = Path(command("xcrun", "--find", "simctl", timeout=30)).resolve()
    if not simctl.is_relative_to(Path(ENV["DEVELOPER_DIR"]).resolve()) or not simctl.is_file():
        raise RuntimeError("Could not resolve the selected Xcode simulator tool")
    print("Booting iPhone simulator", flush=True)
    def sim(*args, timeout=150):
        return command(str(simctl), *args, timeout=timeout)

    runtimes = json.loads(sim("list", "devices", "available", "-j"))["devices"]
    runtime, device = next((runtime, device) for runtime, devices in runtimes.items()
                           if runtime.endswith("iOS-18-6") for device in devices if device["name"] == "iPhone 16")
    identifier = sim("create", "MyChef fixture", device["deviceTypeIdentifier"], runtime)
    if not re.fullmatch(r"[0-9A-Fa-f]{8}(?:-[0-9A-Fa-f]{4}){3}-[0-9A-Fa-f]{12}", identifier):
        raise RuntimeError("CoreSimulator did not return a device identifier")

    def cleanup():
        for operation in ("shutdown", "delete"):
            try:
                subprocess.run([str(simctl), operation, identifier], env=ENV,
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30)
            except subprocess.TimeoutExpired:
                print("Disposable simulator cleanup timed out: " + operation, flush=True)

    stack.callback(cleanup)
    sim("boot", identifier)
    try:
        sim("bootstatus", identifier, timeout=240)
    except subprocess.TimeoutExpired as error:
        output = error.stdout or b""
        if isinstance(output, bytes):
            output = output.decode(errors="replace")
        phases = re.findall(r"\bStatus=(\d{1,3}),\s*isTerminal=(?:YES|NO)", output)
        devices = json.loads(sim("list", "devices", "booted", "-j", timeout=30))["devices"]
        if not any(entry.get("udid") == identifier and entry.get("state") == "Booted"
                   for entry in devices.get(runtime, [])):
            print("iPhone simulator did not boot. CoreSimulator status: "
                  + (phases[-1] if phases else "unreported"), flush=True)
            raise
        # bootstatus also waits for simulator setup tasks. Check the exact
        # booted device directly; trust, installation and launch still must pass.
        sim("spawn", identifier, "launchctl", "print", "system", timeout=30)
        print("CoreSimulator reports Booted; continuing with the installed app check", flush=True)
    sim("keychain", identifier, "add-root-cert", ca)
    sim("install", identifier, str(app))
    print("Installed the verified simulator package", flush=True)

    def diagnostics():
        # Only this fresh simulator and its anonymous fixture are visible.
        try:
            sim("io", identifier, "screenshot", str(ARTIFACTS / "anonymous-launch.png"), timeout=20)
        except (RuntimeError, subprocess.SubprocessError):
            print("Simulator screenshot unavailable", flush=True)
        output = subprocess.run([str(simctl), "spawn", identifier, "log", "show", "--last", "5m",
                                 "--style", "compact", "--predicate",
                                 'process == "App" AND (subsystem == "com.apple.CFNetwork" OR subsystem == "com.apple.WebKit")'],
                                env=ENV, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, timeout=25).stdout
        # Never emit raw OS logs, which can include headers or unrelated paths.
        codes = sorted(set(re.findall(r"(?:NSURLErrorDomain Code=|error\s+)(-\d{2,6})\b", output)))
        print("Simulator network error codes: " + (", ".join(codes) or "none recorded"), flush=True)

    return ({"runtime": runtime, "model": device["name"], "hostArchitecture": os.uname().machine,
             "signing": "simulator linker entitlements only", "keychainIdentity": access_group},
            lambda: sim("launch", identifier, APP), lambda: sim("terminate", identifier, APP), diagnostics)


def android_journeys(state, receipt, metadata, events):
    from native_android_probe import build
    from native_fixture_account import seed

    capability = json.loads((state / "fixture-inbox.json").read_text())["capability"]
    sdk = Path(ENV["ANDROID_HOME"])
    apk = build(state, sdk, command)
    adb_path = str(sdk / "platform-tools/adb")
    command(adb_path, "-s", "emulator-5554", "install", str(apk))
    # Preserve actual hosted identity; never manufacture a GitHub event/context.
    for name in ("GITHUB_ACTIONS", "GITHUB_REPOSITORY", "NATIVE_PLATFORM"):
        if name in os.environ:
            ENV[name] = os.environ[name]
    def diagnose():
        print("Disposable native route statuses: " + str(events[-20:]), flush=True)
        try:
            android_request_diagnostics(adb_path)
        except (OSError, RuntimeError, subprocess.SubprocessError):
            print("Disposable Android bridge stages unavailable", flush=True)
        probe = state / "probe-private.log"
        if probe.is_file():
            safe = [line for line in probe.read_text(errors="replace").splitlines()
                    if re.fullmatch(r"INSTRUMENTATION_(?:STATUS|RESULT): (?:probe=(?:ready|failed)|probeReason=(?:launch|webview|pinch-disabled|probe-timeout|instrumentation|native-control|native-window-(?:unfocused|bounds)|control-timeout|input-denied|picker-(?:window|target|activation|return)|accessibility-controls))|INSTRUMENTATION_CODE: -?[0-9]+", line)]
            print("Disposable probe status: " + "; ".join(safe[-8:]), flush=True)

    groups = (("core", 7), ("media", 6), ("account", 4))
    passed = {}
    failed = []
    adb = lambda *args: command(adb_path, "-s", "emulator-5554", *args)
    for group, count in groups:
        print("Starting installed Android group: " + group, flush=True)
        try:
            if group != "core":
                for marker in ("native-network-offline", "native-shopping-conflict", "native-personal-save-drop"):
                    (state / marker).unlink(missing_ok=True)
                adb("shell", "svc", "wifi", "enable")
                adb("shell", "svc", "data", "enable")
                adb("shell", "am", "force-stop", APP)
                if adb("shell", "pm", "clear", APP) != "Success":
                    raise RuntimeError("Disposable Android app state could not be cleared")
            account = seed(18137, ORIGIN, capability, "nativeandroid" + group)
            (state / "native-android-account.json").write_text(json.dumps(account))
            output = command("bun", "scripts/native-android-journeys.ts", str(state), group, timeout=270)
            print(output, flush=True)
            result = json.loads((state / f"native-android-journeys-{group}.json").read_text())
            checks = result.get("passed") if isinstance(result, dict) else None
            if (not isinstance(result, dict) or result.get("group") != group
                    or not isinstance(checks, list) or len(checks) != count
                    or not all(isinstance(check, str) and re.fullmatch(r"[A-Za-z0-9 -]{1,100}", check) for check in checks)
                    or len(set(checks)) != count):
                raise RuntimeError("Installed Android group receipt is incomplete")
            passed[group] = checks
        except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
            failed.append(group)
            if isinstance(error, RuntimeError):
                print(str(error), flush=True)
            print("Installed Android group failed: " + group, flush=True)
            diagnose()
    if failed:
        raise RuntimeError("Installed Android groups failed: " + ", ".join(failed))
    result = dict(source=receipt["candidate"], artifactSha256=receipt["artifactSha256"], device=metadata,
                  groups=passed, passed=[name for names in passed.values() for name in names],
                  limitations="Disposable API 33 emulator; no physical Android device or audible TalkBack validation.")
    (ARTIFACTS / "installed-journeys.json").write_text(json.dumps(result, indent=2) + "\n")


def main():
    os.umask(0o077)
    if os.environ.get("GITHUB_ACTIONS") != "true" or os.environ.get("GITHUB_REPOSITORY") != "LowGok/my-chef" or PLATFORM not in ("android", "ios-simulator"):
        raise SystemExit("This check requires the private hosted native workflow.")
    event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
    receipt = json.loads((ARTIFACTS / "compilation.json").read_text())
    package = ARTIFACTS / receipt["artifact"]
    if (event.get("label", {}).get("name") not in ("native-installed", f"native-installed-{PLATFORM}")
            or event.get("sender", {}).get("login") != "LowGok"
            or receipt["platform"] != PLATFORM or receipt["candidate"]["sourceCommit"] != event["pull_request"]["head"]["sha"]
            or hashlib.sha256(package.read_bytes()).hexdigest() != receipt["artifactSha256"]):
        raise SystemExit("Installed verification source or artifact did not match the authorized run.")
    journeys = PLATFORM == "android" and os.environ.get("NATIVE_ANDROID_JOURNEYS") == "1"
    with tempfile.TemporaryDirectory(prefix="mychef-installed-") as directory, ExitStack() as stack:
        state = Path(directory).resolve()
        ca, events, rejected_tls = fixture(state, stack, journeys=journeys)
        metadata, launch, stop, diagnostics = android(state, stack, ca, package) if PLATFORM == "android" else ios(state, stack, ca, package)
        verify_stage(lambda: ("POST", "session", 201) in events and ("GET", "session", 200) in events,
                     "native HTTPS session", events, rejected_tls, diagnostics, launch=launch)
        print("Installed app opened a native HTTPS session", flush=True)
        stop()
        before = len(events)
        tls_before = len(rejected_tls)
        verify_stage(lambda: ("POST", "refresh", 200) in events[before:] and ("GET", "session", 200) in events[before:],
                     "refresh after process relaunch", events, rejected_tls, diagnostics,
                     launch=launch, event_start=before, tls_start=tls_before)
        print("Relaunch refreshed the native session from platform storage", flush=True)
        result = dict(platform=PLATFORM, source=receipt["candidate"], artifactSha256=receipt["artifactSha256"],
                      device=metadata, passed=["install and launch", "native session over verified HTTPS", "refresh after process relaunch"],
                      limitations="Anonymous session smoke only; signed-in journeys, accessibility and iPhone TestFlight not exercised.")
        (ARTIFACTS / "installed-smoke.json").write_text(json.dumps(result, indent=2) + "\n")
        if journeys:
            android_journeys(state, receipt, metadata, events)


if __name__ == "__main__":
    main()
