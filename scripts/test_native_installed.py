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


def validate_analytics_probe(value, platform, expected):
    """Strict shape/binding check, not authentication of untrusted JSON claims."""
    contract = json.loads((ROOT / 'mobile/tests/analytics-contract.json').read_text())
    binding_keys = {'sourceCommit', 'apiSha256', 'bundleSha256', 'artifactSha256', 'fixtureConfigSha256', 'contractVersion'}
    if (type(value) is not dict or set(value) != {'schemaVersion', 'platform', 'binding', 'checks'}
            or type(value['schemaVersion']) is not int or value['schemaVersion'] != contract['schemaVersion']
            or platform not in ('android', 'ios-simulator') or value['platform'] != platform
            or type(value['binding']) is not dict or set(value['binding']) != binding_keys
            or type(expected) is not dict or set(expected) != binding_keys):
        return False
    binding = value['binding']
    for key in binding_keys:
        actual = binding[key]
        if actual != expected[key]:
            return False
        if key == 'contractVersion':
            if type(actual) is not int or actual != contract['contractVersion']:
                return False
        elif type(actual) is not str or not re.fullmatch('[a-f0-9]{40}' if key == 'sourceCommit' else '[a-f0-9]{64}', actual):
            return False
    checks = value['checks']
    if type(checks) is not dict or set(checks) != set(contract['checks']):
        return False
    return all(type(checks[group]) is dict and set(checks[group]) == set(names)
               and all(checks[group][name] is True for name in names)
               for group, names in contract['checks'].items())


def validate_analytics_receipts(receipts, expected):
    platforms = {'android', 'ios-simulator'}
    if (type(receipts) is not list or len(receipts) != 2 or type(expected) is not dict
            or set(expected) != platforms):
        return False
    seen = set()
    for receipt in receipts:
        if type(receipt) is not dict or type(receipt.get('platform')) is not str:
            return False
        platform = receipt['platform']
        if platform not in platforms or platform in seen or not validate_analytics_probe(receipt, platform, expected[platform]):
            return False
        seen.add(platform)
    # Artifacts, bundles and private fixture paths differ by platform. Their
    # measured hashes must match each runner's expected binding, while source
    # and API must still identify the same candidate across both OS runs.
    shared = ('sourceCommit', 'apiSha256', 'contractVersion')
    return all(expected['android'][key] == expected['ios-simulator'][key] for key in shared)


def command(*argv, timeout=90, data=None):
    result = subprocess.run(argv, cwd=ROOT, env=ENV, input=data, text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout)
    if result.returncode:
        raise RuntimeError(f"{Path(argv[0]).name} {argv[1]} failed: {result.stdout[-3000:]}")
    return result.stdout.strip()


def account_api_summary(events):
    """Closed diagnostic vocabulary; never request paths, headers or bodies."""
    records = [(method, kind, status) for method, kind, status in events
               if method == 'POST' and kind in ('account-export', 'account-deletion')
               and type(status) is int and 100 <= status <= 599]
    return ', '.join(method + '/' + kind + '/' + str(status) for method, kind, status in records[-8:])


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


def analytics_fixture_configuration(state, enabled):
    """Fictional assessment, bound to fresh loopback-only storage, never live."""
    state = Path(state)
    if (type(enabled) is not bool or not state.is_absolute() or state.is_symlink()
            or not re.fullmatch(r'mychef-installed-[A-Za-z0-9_-]+', state.name)
            or state.stat().st_mode & 0o077):
        raise ValueError('Expected a private disposable native fixture')
    if any(state.iterdir()):
        raise FileExistsError('Fictional analytics requires a fresh empty fixture')
    contract = (ROOT / 'internal/analytics/contract.go').read_text()
    notice = (ROOT / 'internal/analytics/notice.go').read_text()
    scope = re.search(r'^const Scope = ("[^\n]+")$', contract, re.M)
    text = re.search(r'^const NoticeText = ("[^\n]+")$', notice, re.M)
    if not scope or not text:
        raise RuntimeError('Fixture contract constants changed')
    now = int(time.time())
    config = dict(root=str(state / 'analytics-data'), privacyPath=str(state / 'privacy.sqlite'),
                  anchorPath=str(state / 'privacy.anchor'), backupKeyFile=str(state / 'analytics.key'),
                  collection=dict(Enabled=enabled, NoticeVersion='fictional-native-v1',
                                  NoticeHash=hashlib.sha256(json.loads(text[1]).encode()).hexdigest(), OutboxCap=100,
                                  Assessment=dict(status='approved', basis='legitimate_interest', version='fictional-native-v1',
                                                  scope=hashlib.sha256(json.loads(scope[1]).encode()).hexdigest(),
                                                  noticeVersion='fictional-native-v1', jurisdiction='BE-fictional',
                                                  audience='fictional@example.test', deviceAccess='fictional-native-fixture',
                                                  validUntil=now + 30 * 86400)))
    for path, data in ((state / 'analytics.key', os.urandom(32)),
                       (state / 'analytics-clock', str(now).encode()),
                       (state / 'analytics-config.json', json.dumps(config, sort_keys=True).encode())):
        with path.open('xb') as output:
            output.write(data)
        path.chmod(0o600)
    return state / 'analytics-config.json'


def analytics_fixture_overlays(state):
    """Clock/scheduler replacement for tests only; no production test endpoint."""
    runtime = ROOT / 'internal/analytics/runtime_config.go'
    source = runtime.read_text().replace('"os"', '"os"\n"path/filepath"\n"strconv"\n"strings"\n"time"')
    anchor = 'o := OpenOptions{Root: r.Root, PrivacyPath: r.PrivacyPath, AnchorPath: r.AnchorPath, Config: r.Collection}'
    if source.count(anchor) != 1:
        raise RuntimeError('Fixture runtime injection point changed')
    source = source.replace(anchor, anchor + '''
    clockPath := filepath.Join(filepath.Dir(path), "analytics-clock")
    o.Now = func() time.Time {
        raw, err := os.ReadFile(clockPath)
        if err != nil { return time.Time{} }
        unix, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
        if err != nil { return time.Time{} }
        return time.Unix(unix, 0).UTC()
    }
''')
    runtime_overlay = state / 'analytics-runtime-fixture.go'
    runtime_overlay.write_text(source)
    worker = ROOT / 'internal/analytics/worker.go'
    source = worker.read_text().replace('"database/sql"', '"database/sql"\n"encoding/json"')
    start, end = source.index('func (s *Service) Run('), source.index('func (s *Service) Tick(')
    source = source[:start] + 'func (s *Service) Run(ctx context.Context, observe func(Health)) error { return nativeFixtureRun(ctx, s) }\n' + source[end:]
    source += '\n' + (ROOT / 'mobile/tests/analytics-fixture-worker.go.txt').read_text()
    worker_overlay = state / 'analytics-worker-fixture.go'
    worker_overlay.write_text(source)
    return {str(runtime): str(runtime_overlay), str(worker): str(worker_overlay)}


def fixture(state, stack, journeys=False, analytics=None, controls=None):
    print("Building disposable Go fixture", flush=True)
    flags = []
    replacements = {}
    if analytics not in (None, 'disabled', 'approved'):
        raise ValueError('Unknown analytics fixture mode')
    if analytics is not None:
        config_path = analytics_fixture_configuration(state, analytics == 'approved')
        replacements.update(analytics_fixture_overlays(state))
    if sys.platform == "darwin":
        # This fixture deliberately never runs the Linux-only import worker.
        # Refuse its listener on macOS; leave the real source and UID checks alone.
        stub = state / "worker_socket_fixture.go"
        stub.write_text('package imports\nimport "net"\n'
                        'func ListenPrivateWorker(string, uint32) (net.Listener, error) {\n'
                        'return nil, ErrExtractionUnavailable\n}\n')
        replacements[str(ROOT / "internal/imports/worker_socket.go")] = str(stub)
    if replacements:
        overlay = state / "go-overlay.json"
        overlay.write_text(json.dumps({"Replace": replacements}))
        flags = ["-overlay", str(overlay)]
    for binary in (("mychef", "mychef-migrate", "mychef-analytics") if analytics is not None else ("mychef", "mychef-migrate")):
        command("go", "build", *flags, "-trimpath", "-o", str(state / binary), "./cmd/" + binary, timeout=450)
    email, _ = fixture_email(state)
    db = str(state / "app.db")
    app_env = dict(ENV, **email, MYCHEF_ADDR="127.0.0.1:18137", MYCHEF_ORIGIN=ORIGIN,
                   MYCHEF_DB=db, MYCHEF_LIFECYCLE_DB=db + ".lifecycle", MYCHEF_DEV="1",
                   MYCHEF_PROVIDER="fixture", MYCHEF_E2E="1", MYCHEF_FEEDBACK_TO="operator@example.test")
    if journeys:
        app_env.update(stack.enter_context(fixture_imports(ROOT, state, app_env)))
    subprocess.run([str(state / "mychef-migrate"), "up", "--db", db], env=app_env, check=True, timeout=60)
    if analytics is not None:
        subprocess.run([str(state / 'mychef-analytics'), 'live', 'init', '--app', db, '--config', str(config_path)],
                       env=app_env, check=True, timeout=60, stdout=subprocess.DEVNULL)
        app_env['MYCHEF_ANALYTICS_CONFIG'] = str(config_path)
    log = stack.enter_context((state / "server.log").open("w"))
    app = subprocess.Popen([str(state / "mychef")], cwd=ROOT, env=app_env, stdout=log, stderr=log)
    stack.callback(lambda: terminate(app))

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
    if controls is not None:
        def restart():
            nonlocal app
            terminate(app)
            app = subprocess.Popen([str(state / 'mychef')], cwd=ROOT, env=app_env, stdout=log, stderr=log)
            wait_for(ready, 'restarted disposable Go backend', 35)
        controls['restart'] = restart
    events = []
    if analytics is not None:
        (state / 'analytics-client-boundary.json').write_text(json.dumps(dict(observed=0, passed=True)))
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
                headers = {k: self.headers[k] for k in ("Authorization", "Content-Type", "Cookie", "Accept", "Idempotency-Key", "X-MyChef-Analytics-Revision", "If-Match", "If-None-Match") if k in self.headers}
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
                boundary = state / 'analytics-client-boundary.json'
                if boundary.exists() and response.getheader('Content-Type', '').startswith('application/json'):
                    with shopping_lock:
                        value = json.loads(boundary.read_text())
                        value['observed'] += 1
                        value['passed'] = value['passed'] and re.search(rb'\b(?:sub|evt|per)_[a-f0-9]{32}\b', body) is None
                        boundary.write_text(json.dumps(value))
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
                if route == "analytics/notice" and response.status == 200:
                    notice = json.loads(body)
                    if notice.get("available") is False and notice.get("defaultEnabled") is False:
                        event_kind = "analytics-notice-disabled"
                    elif notice.get("available") is True and notice.get("defaultEnabled") is True:
                        event_kind = "analytics-notice-assessed"
                if route in ("account/export", "account/deletion"):
                    event_kind = route.replace("/", "-")
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
    if ENV.get("NATIVE_ANALYTICS_SPEECH") == "1":
        avd = state / "avd"
        avd.mkdir(mode=0o700)
        ENV["ANDROID_AVD_HOME"] = str(avd)
        command(avdmanager, "create", "avd", "--name", "mychef-ci", "--package", image, "--device", "pixel_6", "--path", str(avd / "mychef-ci.avd"))
    else:
        command(avdmanager, "create", "avd", "--name", "mychef-ci", "--package", image, data="no\n")
    adb_path = str(sdk / "platform-tools/adb")
    emu_path = str(sdk / "emulator/emulator")
    print(command(emu_path, "-accel-check"), flush=True)
    boot_log = state / "emulator.log"
    boot_output = stack.enter_context(boot_log.open("w"))
    subprocess.run([adb_path, "start-server"], env=ENV, check=True, timeout=25,
                   stdout=boot_output, stderr=boot_output)
    # TalkBack gestures need touchscreen input, rather than an emulated mouse
    # or tablet. Restrict this override to the disposable speech fixture.
    audio_flags = ["-feature", "-VirtioMouse,-VirtioTablet,-VirtioDualModeMouse"] if ENV.get("NATIVE_ANALYTICS_SPEECH") == "1" else ["-no-window", "-no-audio"]
    emulator = subprocess.Popen([emu_path, "-avd", "mychef-ci", "-port", "5554", *audio_flags,
                                "-no-snapshot", "-writable-system", "-gpu", "software", "-no-boot-anim",
                                "-camera-back", "none", "-memory", "2048", "-partition-size", "2048"], env=ENV,
                                stdout=boot_output, stderr=boot_output)
    stack.callback(terminate, emulator)
    if ENV.get("NATIVE_ANALYTICS_SPEECH") == "1":
        ENV["MYCHEF_ANALYTICS_EMULATOR_PID"] = str(emulator.pid)
    adb = lambda *args: command(adb_path, "-s", "emulator-5554", *args)
    def root_adb():
        def ready():
            try:
                adb("root")
            except RuntimeError:
                # Rooting restarts adbd. A closed connection is not success;
                # require the reconnected disposable device's actual UID.
                pass
            adb("wait-for-device")
            return adb("shell", "id", "-u") == "0"
        wait_for(ready, "rooted disposable Android transport", seconds=30)


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
    root_adb()
    adb("wait-for-device")
    remount = adb("remount")
    if "remount succeeded" not in remount.lower():
        adb("disable-verity")
        adb("reboot")
        adb("wait-for-device")
        wait_for(lambda: adb("shell", "getprop", "sys.boot_completed") == "1", "Android remount reboot", 120)
        root_adb()
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
    def display_ready():
        # SettingsProvider may still be applying boot defaults after installation.
        # Require a fresh read-back; never continue with an unconfirmed timeout.
        adb("shell", "settings", "put", "system", "screen_off_timeout", "1800000")
        return adb("shell", "settings", "get", "system", "screen_off_timeout") == "1800000"
    wait_for(display_ready, "disposable Android display timeout", seconds=20)
    version = adb("shell", "getprop", "ro.build.version.sdk")
    webview = next((x.strip() for x in adb("shell", "dumpsys", "webviewupdate").splitlines() if "Current WebView package" in x), "unreported")
    signature = command(str(sdk / "build-tools/36.0.0/apksigner"), "verify", "--print-certs", str(apk))
    metadata = {"api": version, "systemImage": image, "webView": webview,
                "signing": next(line for line in signature.splitlines() if "certificate SHA-256 digest:" in line)}
    for key, package in (("talkBackInstalled", "com.google.android.marvin.talkback"),
                         ("googleTTSInstalled", "com.google.android.tts")):
        try:
            metadata[key] = adb("shell", "pm", "path", package).startswith("package:")
        except RuntimeError:
            metadata[key] = False
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


def ios_host_resources(state):
    """Coarse ephemeral-runner diagnostics; no command lines or private paths."""
    result = {"diskFreeMiB": shutil.disk_usage(state).free // 1024**2}
    try:
        memory = subprocess.run(["/usr/bin/vm_stat"], env=ENV, capture_output=True, text=True, timeout=10)
        size = re.search(r"page size of ([0-9]+) bytes", memory.stdout)
        if size:
            for name, field in (("free", "Pages free"), ("active", "Pages active"),
                                ("wired", "Pages wired down"), ("compressed", "Pages occupied by compressor")):
                value = re.search(re.escape(field) + r":\s*([0-9]+)", memory.stdout)
                if value:
                    result[name + "MiB"] = int(value[1]) * int(size[1]) // 1024**2
    except (OSError, subprocess.SubprocessError):
        pass
    print("Simulator runner resources: " + json.dumps(result, sort_keys=True), flush=True)


def start_ios_simulator(sim, identifier, open_ui):
    # Open the selected host before boot, as required by the UI client startup
    # sequence. A host may initiate boot itself; inspect the exact owned UUID
    # and still require bootstatus/HTTPS afterward.
    def owned_state():
        devices = json.loads(sim('list', 'devices', '-j', timeout=30))['devices']
        matches = [entry for group in devices.values() for entry in group if entry.get('udid') == identifier]
        if len(matches) != 1 or matches[0].get('state') not in ('Shutdown', 'Booting', 'Booted'):
            raise RuntimeError('The owned simulator has an unexpected boot state')
        return matches[0]['state']
    if open_ui is not None:
        open_ui()
    if owned_state() == 'Shutdown':
        try:
            sim('boot', identifier)
        except RuntimeError as error:
            message = str(error)
            already_started = re.search(r'\bcode=405\b', message) and re.search(
                r'Unable to boot device in current state: (?:Booted|Booting)\b', message)
            if not already_started:
                raise
            # Enumerating every device can block while the UI is completing
            # this boot. Corroborate with actual completion for this UUID.
            sim('bootstatus', identifier, timeout=240)
            print('Owned simulator boot corroborated after host UI startup', flush=True)


def compile_ios_ui_observer(state):
    observer = Path(state) / 'simulator-host-readiness'
    command('xcrun', '--sdk', 'macosx', 'swiftc', '-framework', 'AppKit',
            str(ROOT / 'mobile/tests/ios/SimulatorHost.swift'), '-o', str(observer), timeout=90)
    if command(str(observer), '--identity-self-test', timeout=15) != 'Simulator host file identity checks passed':
        raise RuntimeError('Simulator host file identity checks failed')
    print('Simulator host file identity checks passed', flush=True)
    return observer


def start_ios_ui(state, stack, arguments, observer=None):
    bundle = Path(arguments[2])
    info = bundle / 'Contents/Info.plist'
    if not info.is_file() or info.stat().st_size > 65536:
        raise RuntimeError('Selected Xcode UI metadata is unavailable')
    name = plistlib.loads(info.read_bytes()).get('CFBundleExecutable')
    if (not isinstance(name, str) or not 1 <= len(name) <= 128 or name in ('.', '..')
            or any(value in name for value in ('/', '\\', '\0'))):
        raise RuntimeError('Selected Xcode UI metadata is unavailable')
    executable = bundle / 'Contents/MacOS' / name
    if not executable.is_file():
        raise RuntimeError('Selected Xcode UI executable is unavailable')
    # AppKit launches the explicit selected bundle with substitution disabled,
    # then verifies the returned app's actual executable. Do not launch a
    # second instance through open or scan unrelated processes by bundle ID.
    if observer is None:
        observer = compile_ios_ui_observer(state)
    result = subprocess.run([str(observer), str(bundle)], env=ENV, text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=75)
    if result.returncode != 0 or result.stdout.strip() != 'Selected Xcode UI process is running':
        summary = result.stdout.strip()
        if re.fullmatch(r'Selected Xcode UI readiness: application=(?:true|false) otherExecutable=(?:true|false) sameBundle=(?:true|false) sameFile=(?:true|false) launchCompleted=(?:true|false) launchFailed=(?:true|false) expected=(?:devicehub|trampoline|simulator|other) actual=(?:devicehub|trampoline|simulator|other) candidates=[12]', summary):
            print(summary, flush=True)
        raise RuntimeError('Selected Xcode UI process did not become ready')
    print('Selected Xcode UI process is running', flush=True)


def ios_ui_command(developer, model, identifier):
    developer = Path(developer)
    if model == 'iPhone 17':
        # Xcode 27 ships Device Hub beside Developer, replacing Simulator.app.
        # Do not silently skip that host when testing the new VoiceOver runtime.
        hub = developer.parent / 'Applications/DeviceHub.app'
        if not hub.is_dir():
            raise RuntimeError('Selected Xcode Device Hub is unavailable')
        return ['/usr/bin/open', '-Fn', str(hub)]
    legacy = developer / 'Applications/Simulator.app'
    if legacy.is_dir():
        # Keep the software keyboard available for the actual phone-layout
        # checks. NSArgumentDomain overrides this launch without host writes.
        return ['/usr/bin/open', '-Fn', str(legacy), '--args', '-CurrentDeviceUDID', identifier,
                '-ConnectHardwareKeyboard', 'NO']
    return None


def ios(state, stack, ca, package, model="iPhone 16", fixture_name="MyChef fixture"):
    if (model, fixture_name) not in (("iPhone 16", "MyChef fixture"),
                                    ("iPhone SE (3rd generation)", "MyChef analytics small fixture"),
                                    ("iPhone 17", "MyChef analytics voice fixture")):
        raise ValueError("Unsupported disposable simulator selection")
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
    ios_host_resources(state)
    def sim(*args, timeout=150):
        try:
            return command(str(simctl), *args, timeout=timeout)
        except subprocess.TimeoutExpired:
            print("Simulator operation timed out: " + args[0], flush=True)
            ios_host_resources(state)
            raise

    runtimes = json.loads(sim("list", "devices", "available", "-j"))["devices"]
    required_runtime = "iOS-27-0" if model == "iPhone 17" else "iOS-18-6"
    runtime, device = next((runtime, device) for runtime, devices in runtimes.items()
                           if runtime.endswith(required_runtime) for device in devices if device["name"] == model)
    identifier = sim("create", fixture_name, device["deviceTypeIdentifier"], runtime)
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
    ui_command = ios_ui_command(ENV["DEVELOPER_DIR"], model, identifier)
    # Compile the small observer before CoreSimulator's boot workload. It must
    # establish the selected UI process before device boot starts.
    ui_observer = compile_ios_ui_observer(state) if (model == 'iPhone 17' or
        model == 'iPhone 16' and os.environ.get('NATIVE_ANALYTICS_JOURNEYS') == '1') else None
    def open_ui():
        if ui_command:
            if model == "iPhone 17":
                start_ios_ui(state, stack, ui_command, observer=ui_observer)
            else:
                command(*ui_command, timeout=30)
            print("Opened selected Xcode " + ("Device Hub" if model == "iPhone 17" else "Simulator"), flush=True)
        else:
            print("Legacy Simulator GUI unavailable; continuing with actual headless runtime checks", flush=True)
    start_ios_simulator(sim, identifier, open_ui)
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
    try:
        sim("keychain", identifier, "add-root-cert", ca)
    except subprocess.TimeoutExpired:
        # A freshly booted simulator can finish security-service startup after
        # reporting Booted. Retry the same run-owned CA idempotently; success
        # still requires simctl's actual completion, installation and HTTPS.
        print("Simulator trust service not ready; retrying the same fixture CA", flush=True)
        sim("bootstatus", identifier, timeout=120)
        sim("keychain", identifier, "add-root-cert", ca, timeout=120)
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
                    if re.fullmatch(r"INSTRUMENTATION_(?:STATUS|RESULT): (?:probe=(?:ready|failed)|probeReason=(?:launch|webview|pinch-disabled|probe-timeout|instrumentation|native-control|native-window-(?:unfocused|bounds)|control-timeout|input-denied|picker-(?:window|target|activation|confirmation|return)|accessibility-controls))|INSTRUMENTATION_CODE: -?[0-9]+", line)]
            print("Disposable probe status: " + "; ".join(safe[-8:]), flush=True)

    groups = (("core", 7), ("media", 5), ("pages", 1), ("account", 4))
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
                    or not all(isinstance(check, str) and re.fullmatch(r"[A-Za-z0-9 /-]{1,100}", check) for check in checks)
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
