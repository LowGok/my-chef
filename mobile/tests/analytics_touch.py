"""Fixed host gestures through the run-owned emulator's actual X11 window."""
from contextlib import contextmanager
from pathlib import Path
import os
import re
import subprocess
import tempfile
import time


def touch_facts(text, contacts=1):
    if type(contacts) is not int or contacts not in (1, 2) or not isinstance(text, str) or len(text) > 65536:
        raise ValueError('Bounded input trace required')
    x = len(re.findall(r'\bEV_ABS\s+ABS_MT_POSITION_X\s+[0-9a-f]{8}\b', text))
    y = len(re.findall(r'\bEV_ABS\s+ABS_MT_POSITION_Y\s+[0-9a-f]{8}\b', text))
    ids = re.findall(r'\bEV_ABS\s+ABS_MT_TRACKING_ID\s+([0-9a-f]{8})\b', text)
    down = len(re.findall(r'\bEV_KEY\s+BTN_TOUCH\s+(?:DOWN|00000001)\b', text))
    up = len(re.findall(r'\bEV_KEY\s+BTN_TOUCH\s+(?:UP|00000000)\b', text))
    mouse = len(re.findall(r'\bEV_KEY\s+BTN_(?:LEFT|MOUSE)\s+(?:DOWN|00000001)\b', text))
    return dict(touchscreen=bool(x and y
                                and (down >= contacts or sum(int(value, 16) < 0x80000000 for value in ids) >= contacts)
                                and (up >= contacts or ids.count('ffffffff') >= contacts)),
                positions=x + y, releases=up + ids.count('ffffffff'), mouseButtons=mouse)


@contextmanager
def touch_trace(native, contacts):
    # getevent disables stdout buffering. The remote timeout terminates this
    # bounded observer on the run-owned device even if the host command fails.
    adb = str(Path(native.ENV['ANDROID_HOME']) / 'platform-tools/adb')
    with tempfile.TemporaryFile() as output:
        process = subprocess.Popen([adb, '-s', 'emulator-5554', 'shell', 'timeout', '2', 'getevent', '-lt'],
                                   env=native.ENV, stdout=output, stderr=subprocess.DEVNULL)
        try:
            deadline = time.monotonic() + 2
            while time.monotonic() < deadline:
                # pread leaves the child writer's shared file offset intact.
                header = os.pread(output.fileno(), 16384, 0)
                if re.search(rb'add device [0-9]+: /dev/input/event[0-9]+', header):
                    break
                if process.poll() is not None:
                    raise RuntimeError('Emulator touchscreen observer unavailable')
                time.sleep(.02)
            else:
                raise RuntimeError('Emulator touchscreen observer unavailable')
            yield
            process.wait(timeout=4)
        except subprocess.TimeoutExpired:
            raise RuntimeError("Emulator touchscreen observer unavailable") from None
        finally:
            native.terminate(process)
        output.seek(0)
        raw = output.read(65537)
        if len(raw) > 65536:
            raise RuntimeError('Emulator touchscreen observer unavailable')
        facts = touch_facts(raw.decode(errors='replace'), contacts)
    print('Analytics hardware input: touchscreen=' + str(facts['touchscreen']).lower()
          + ' positions=' + str(facts['positions']) + ' releases=' + str(facts['releases'])
          + ' mouseButtons=' + str(facts['mouseButtons']), flush=True)
    if not facts['touchscreen']:
        raise RuntimeError('Emulator touchscreen input was not observed')


def focus_display(native):
    """Activate only the run-owned emulator window without sending input."""
    pid = native.ENV.get('MYCHEF_ANALYTICS_EMULATOR_PID', '')
    if native.ENV.get('DISPLAY') != ':91' or not re.fullmatch(r'[1-9][0-9]{0,8}', pid):
        raise RuntimeError('Owned emulator display unavailable')
    def command(*args):
        return native.command('xdotool', *args, timeout=10)
    windows = command('search', '--onlyvisible', '--name', '^Android Emulator - mychef-ci(:5554)?$').split()
    if not 1 <= len(windows) <= 8 or any(not re.fullmatch(r'[1-9][0-9]{0,12}', value) for value in windows):
        raise RuntimeError('Owned emulator display unavailable')
    candidates = []
    for window in windows:
        owner = command('getwindowpid', window).strip()
        # The SDK launcher may retain a separate QEMU child. Accept only this
        # Popen process or its bounded, current descendant chain.
        for _ in range(8):
            if owner == pid: break
            if not re.fullmatch(r'[1-9][0-9]{0,8}', owner):
                raise RuntimeError('Owned emulator display unavailable')
            owner = native.command('ps', '-o', 'ppid=', '-p', owner, timeout=10).strip()
        if owner != pid:
            raise RuntimeError('Owned emulator display unavailable')
        geometry = dict(re.findall(r'^(WIDTH|HEIGHT)=([0-9]{1,5})$', command('getwindowgeometry', '--shell', window), re.M))
        width, height = int(geometry.get('WIDTH', 0)), int(geometry.get('HEIGHT', 0))
        if 240 <= width <= 1280 and 360 <= height <= 800:
            candidates.append((width * height, window, width, height))
    candidates.sort(reverse=True)
    if not candidates or len(candidates) > 1 and candidates[0][0] == candidates[1][0]:
        raise RuntimeError('Owned emulator display unavailable')
    _, window, width, height = candidates[0]
    command('windowfocus', '--sync', window)
    time.sleep(.15)
    return window, width, height


def gesture(kind, native):
    if kind not in ('double-tap', 'next'):
        raise ValueError('Closed hardware gesture required')
    window, width, height = focus_display(native)
    def command(*args):
        return native.command('xdotool', *args, timeout=10)
    def move(fraction, vertical=.5):
        return ['mousemove', '--window', window, str(round(width * fraction)), str(round(height * vertical))]
    with touch_trace(native, 2 if kind == 'double-tap' else 1):
        if kind == 'double-tap':
            # One process preserves timing; nearby distinct host positions
            # avoid Qt folding the second press into a host double-click.
            command(*(move(.5 - 12 / width) + ['click', '1', 'sleep', '.12']
                      + move(.5 + 12 / width) + ['click', '1']))
        else:
            # A slight vertical change keeps the gesture horizontally dominant
            # while making both absolute axes observable in this fresh trace.
            sequence = move(.3, .48) + ['mousedown', '1']
            for step in range(1, 11):
                sequence += ['sleep', '.018'] + move(.3 + .04 * step, .48 + .004 * step)
            sequence += ['mouseup', '1']
            try:
                command(*sequence)
            finally:
                command('mouseup', '1')
    print('Analytics hardware gesture: owned emulator X11 pointer', flush=True)
    return {'passed': True}
