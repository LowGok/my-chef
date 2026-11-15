"""Runner-local TalkBack speaker evidence. No audio or transcript is an artifact."""
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import time
import urllib.request
import wave

TARGETS = ('on', 'off', 'stale', 'offline', 'order', 'signup-on', 'signup-off', 'cleanup', 'backup')
MODEL_SHA = 'a03779c86df3323075f5e796cb2ce5029f00ec8869eee3fdfb897afe36c6d002'
RECOGNIZER_SOURCE = 'd1be6fde11ac6e0407606b4e42fe72d34add8037'


def matches(target, transcript):
    text = ' '.join(re.findall(r'[a-z]+', transcript.lower()))
    if target in ('signup-on', 'signup-off'):
        target = target.removeprefix('signup-')
    if target in ('on', 'off'):
        label = re.search(r'help improve my\s?chef', text)
        if not label:
            return False
        # TalkBack may speak the checked state before the label. Require
        # adjacency to this control, rather than accepting earlier UI speech
        # or the action hint as its current state.
        before = re.search(r'\b(not checked|unchecked|not selected|checked|selected)(?: (?:check ?box|switch|button))? $',
                           text[:label.start()])
        after = re.match(r' *(?:(?:check ?box|switch|button) )?(not checked|unchecked|not selected|checked|selected|on|off)\b',
                         text[label.end():])
        states = {('off' if match[1] in ('not checked', 'unchecked', 'not selected', 'off') else 'on')
                  for match in (before, after) if match is not None}
        return states == {target}
    phrase = {'stale': 'your setting changed elsewhere', 'offline': 'turning off was not confirmed',
              'order': 'refresh analytics setting', 'cleanup': 'active deletion is in progress',
              'backup': 'isolated backup cleanup is pending'}.get(target)
    return phrase is not None and phrase in text


def audible(path):
    try:
        if not 1000 < path.stat().st_size < 16000 * 2 * 45 + 100:
            return False
        with wave.open(str(path), 'rb') as sound:
            if (sound.getnchannels(), sound.getsampwidth(), sound.getframerate()) != (1, 2, 16000):
                return False
            if not 0.5 <= sound.getnframes() / 16000 <= 45:
                return False
            pcm = memoryview(sound.readframes(sound.getnframes())).cast('h')
            return bool(pcm) and max(map(abs, pcm)) >= 64
    except (OSError, ValueError, EOFError, wave.Error):
        return False


def tools():
    return Path(os.environ['RUNNER_TEMP']) / 'analytics-speech-tools'


def prepare():
    from analytics_installed import authorized
    event = json.loads(Path(os.environ['GITHUB_EVENT_PATH']).read_text())
    if not authorized(event, os.environ) or os.environ.get('NATIVE_PLATFORM') != 'android':
        raise SystemExit('Speech tooling authorization refused')
    root = tools()
    root.mkdir(mode=0o700)
    def run(*args):
        subprocess.run(args, cwd=root, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=240)
    run('sudo', 'apt-get', 'update', '-qq')
    run('sudo', 'apt-get', 'install', '-y', '-qq', '--no-install-recommends',
        'ffmpeg', 'cmake', 'g++', 'pulseaudio', 'pulseaudio-utils', 'xvfb')
    source = root / 'recognizer'
    run('git', 'init', str(source))
    run('git', '-C', str(source), 'fetch', '--depth=1', 'https://github.com/ggml-org/whisper.cpp.git', RECOGNIZER_SOURCE)
    run('git', '-C', str(source), 'checkout', '--detach', 'FETCH_HEAD')
    actual = subprocess.check_output(['git', '-C', str(source), 'rev-parse', 'HEAD'], text=True).strip()
    if actual != RECOGNIZER_SOURCE:
        raise RuntimeError('Offline recognizer source drift')
    run('cmake', '-S', str(source), '-B', str(source / 'build'), '-DCMAKE_BUILD_TYPE=Release',
        '-DWHISPER_BUILD_TESTS=OFF', '-DWHISPER_BUILD_SERVER=OFF', '-DWHISPER_CURL=OFF')
    run('cmake', '--build', str(source / 'build'), '--target', 'whisper-cli', '-j', '4')
    model = root / 'model.bin'
    with urllib.request.urlopen('https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.en.bin', timeout=90) as response, model.open('xb') as output:
        digest, size = hashlib.sha256(), 0
        while chunk := response.read(1024 * 1024):
            size += len(chunk)
            if size > 200 * 1024 * 1024:
                raise RuntimeError('Offline model exceeds bound')
            digest.update(chunk)
            output.write(chunk)
    if digest.hexdigest() != MODEL_SHA:
        raise RuntimeError('Offline model digest mismatch')
    print('Pinned offline speech tools prepared; no external recognition service')


class Speech:
    def __init__(self, state, stack, native):
        self.state, self.native, self.stack = state, native, stack
        self.active, self.capture = None, None
        self.passed = set()
        self.attempts = {}
        socket = state / 'speaker.sock'
        pulse_root = state / 'pulse'
        pulse_root.mkdir(mode=0o700)
        self.sink = 'mychef_analytics_speech'
        self.env = {**native.ENV, 'XDG_RUNTIME_DIR': str(pulse_root),
                    'PULSE_SERVER': 'unix:' + str(socket), 'PULSE_SINK': self.sink,
                    'QEMU_AUDIO_DRV': 'pa', 'QEMU_AUDIO_OUT_DRV': 'pa',
                    'QT_QPA_PLATFORM': 'xcb', 'DISPLAY': ':91'}
        log = stack.enter_context((state / 'speaker-private.log').open('wb'))
        display = subprocess.Popen(['Xvfb', ':91', '-screen', '0', '1280x800x24', '-nolisten', 'tcp'],
                                   env=self.env, stdout=log, stderr=log)
        stack.callback(native.terminate, display)
        native.wait_for(lambda: Path('/tmp/.X11-unix/X91').is_socket() and display.poll() is None,
                        'isolated speech display', 12)
        pulse = subprocess.Popen(['pulseaudio', '--daemonize=no', '--use-pid-file=no', '--exit-idle-time=-1',
                                  '--disallow-module-loading', '-n', '-L',
                                  'module-native-protocol-unix socket=' + str(socket) + ' auth-anonymous=1',
                                  '-L', 'module-null-sink sink_name=' + self.sink + ' rate=48000 channels=2'],
                                 env=self.env, stdout=log, stderr=log)
        stack.callback(native.terminate, pulse)
        native.wait_for(lambda: socket.is_socket() or pulse.poll() is not None, 'private speech socket', 12)
        if pulse.poll() is not None:
            raise RuntimeError('Private speech sink unavailable')
        native.ENV.update(self.env)
        if self.sink + '.monitor' not in native.command('pactl', 'list', 'short', 'sources'):
            raise RuntimeError('Private speaker monitor unavailable')
        self.log = log
        stack.callback(self.close)

    def close(self):
        if self.capture is not None:
            self.native.terminate(self.capture)
            self.capture = None

    def operate(self, operation, target):
        if target not in TARGETS:
            raise ValueError('Unknown closed speech target')
        if operation == 'speech-start':
            if self.active is not None or target in self.passed:
                raise ValueError('Duplicate speech capture')
            attempt = self.attempts.get(target, 0) + 1
            if attempt > 3:
                raise ValueError('Speech capture attempt limit')
            self.attempts[target] = attempt
            self.active, self.started = target, time.monotonic()
            path = self.state / ('speech-' + target + '-' + str(attempt) + '.wav')
            self.path = path
            self.capture = subprocess.Popen(['ffmpeg', '-nostdin', '-v', 'error', '-f', 'pulse', '-i',
                self.sink + '.monitor', '-t', '40', '-vn', '-ac', '1', '-ar', '16000', '-c:a', 'pcm_s16le', str(path)],
                env=self.env, stdout=self.log, stderr=self.log)
            self.native.wait_for(lambda: path.exists() or self.capture.poll() is not None, 'private speech capture', 12)
            if self.capture.poll() is not None:
                raise RuntimeError('Speaker capture failed')
            return {'passed': True}
        if operation != 'speech-stop' or self.active != target or not 1 < time.monotonic() - self.started < 40:
            raise ValueError('Speech capture timeline mismatch')
        self.capture.send_signal(signal.SIGINT)
        self.capture.wait(timeout=12)
        self.capture, self.active = None, None
        path = self.path
        if not audible(path):
            path.unlink(missing_ok=True)
            raise RuntimeError('Speaker capture is silent or outside bounds')
        root = tools()
        model = root / 'model.bin'
        with model.open('rb') as stream:
            if hashlib.file_digest(stream, 'sha256').hexdigest() != MODEL_SHA:
                raise RuntimeError('Offline model changed')
        prefix = path.with_suffix('')
        result = subprocess.run([str(root / 'recognizer/build/bin/whisper-cli'), '-m', str(model), '-f', str(path),
            '-l', 'en', '-t', '4', '-np', '-nt', '-otxt', '-of', str(prefix)],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=60)
        transcript = prefix.with_suffix('.txt')
        text = transcript.read_text() if result.returncode == 0 and transcript.is_file() and transcript.stat().st_size < 16384 else ''
        passed = matches(target, text)
        if not passed:
            words = ' '.join(re.findall(r'[a-z]+', text.lower()))
            label = re.search(r'help improve my\s?chef', words) is not None
            state = 'on' if matches('on', text) else 'off' if matches('off', text) else 'unknown'
            print('Analytics speech evidence: target=' + target + ' audible=true label=' + str(label).lower() + ' state=' + state, flush=True)
            # Fixed UI vocabulary only. Never print arbitrary recognized words,
            # the transcript, audio, account fields or provider output.
            vocabulary = ('help', 'improve', 'chef', 'checked', 'unchecked',
                          'checkbox', 'talkback', 'tutorial', 'keyboard', 'welcome')
            flags = {word: word in words.split() for word in vocabulary}
            print('Analytics speech vocabulary: ' + json.dumps(flags, sort_keys=True), flush=True)
        path.unlink(missing_ok=True)
        transcript.unlink(missing_ok=True)
        if not passed:
            raise RuntimeError('Required TalkBack speech was not recognized: ' + target)
        self.passed.add(target)
        return {'passed': True}


if __name__ == '__main__':
    if sys.argv[1:] != ['prepare']:
        raise SystemExit('Expected guarded speech tooling preparation')
    prepare()
