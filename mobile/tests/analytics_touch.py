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


def touch_devices(snapshot):
    if not isinstance(snapshot, str) or len(snapshot) > 65536:
        raise RuntimeError('Emulator touchscreen observer unavailable')
    devices = set()
    for block in re.split(r'(?m)(?=^add device [0-9]+: )', snapshot):
        header = re.match(r'add device [0-9]+: (/dev/input/event[0-9]{1,3})\r?\n', block)
        if header and all(re.search(r'\b' + axis + r'\b', block)
                          for axis in ('ABS_MT_POSITION_X', 'ABS_MT_POSITION_Y')):
            devices.add(header[1])
    if not 1 <= len(devices) <= 16:
        raise RuntimeError('Emulator touchscreen observer unavailable')
    return devices


@contextmanager
def touch_trace(native, contacts):
    # A keyboard header precedes opening the touch device on some boots. Wait
    # for every measured MT device's own header before releasing the gesture.
    adb = str(Path(native.ENV['ANDROID_HOME']) / 'platform-tools/adb')
    expected = touch_devices(native.command(adb, '-s', 'emulator-5554', 'shell', 'getevent', '-lp', timeout=10))
    with tempfile.TemporaryFile() as output:
        # The remote cap also cleans up if the host connection disappears.
        process = subprocess.Popen([adb, '-s', 'emulator-5554', 'shell', 'timeout', '8', 'getevent', '-lt'],
                                   env=native.ENV, stdout=output, stderr=subprocess.DEVNULL)
        try:
            deadline = time.monotonic() + 4
            while time.monotonic() < deadline:
                # pread leaves the child writer's shared file offset intact.
                header = os.pread(output.fileno(), 65537, 0)
                if len(header) > 65536:
                    raise RuntimeError('Emulator touchscreen observer unavailable')
                opened = {path.decode() for path in re.findall(
                    rb'(?m)^add device [0-9]+: (/dev/input/event[0-9]+)\r?\n', header)}
                if expected <= opened:
                    break
                if process.poll() is not None:
                    raise RuntimeError('Emulator touchscreen observer unavailable')
                time.sleep(.02)
            else:
                raise RuntimeError('Emulator touchscreen observer unavailable')
            yield
            # Stop after observing the complete required contacts, rather than
            # expiring a short timer that began before device enumeration.
            deadline = time.monotonic() + 3
            while time.monotonic() < deadline:
                raw = os.pread(output.fileno(), 65537, 0)
                if len(raw) > 65536:
                    raise RuntimeError('Emulator touchscreen observer unavailable')
                if touch_facts(raw.decode(errors='replace'), contacts)['touchscreen'] or process.poll() is not None:
                    break
                time.sleep(.02)
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
    # The controlled Android display is portrait1080x1920. Qt can keep a
    # wider canvas around that phone. Scale swipes to the fitted portrait
    # width, keeping the initial press around the already-tested centre.
    portrait_width = min(width, height * 9 / 16)
    print('Analytics host touch viewport: width=' + str(width) + ' height=' + str(height)
          + ' portraitWidth=' + str(round(portrait_width)), flush=True)
    def swipe_point(step):
        return move(.5 + (-.2 + .04 * step) * portrait_width / width, .48 + .004 * step)
    # Let Qt receive the initial pointer position before opening the guest
    # observer. This sends no contact; the single press still follows actual
    # observer readiness and must produce complete measured MT contacts.
    initial = move(.5 - 12 / width, .5 - 6 / height) if kind == 'double-tap' else swipe_point(0)
    command(*(initial + ['sleep', '.1']))
    print('Analytics hardware gesture plan: ' + kind, flush=True)
    with touch_trace(native, 2 if kind == 'double-tap' else 1):
        if kind == 'double-tap':
            # One process preserves timing; nearby distinct host positions
            # avoid Qt folding the second press into a host double-click.
            # Change both axes: the kernel omits unchanged absolute values,
            # including across separate gestures in a new observer window.
            # Hold each contact across emulator input polling; xdotool's
            # click primitive has no useful finger-down duration here.
            press = ['mousedown', '1', 'sleep', '.05', 'mouseup', '1']
            try:
                command(*(move(.5 - 12 / width, .5 - 6 / height) + press + ['sleep', '.12']
                          + move(.5 + 12 / width, .5 + 6 / height) + press))
            finally:
                command('mouseup', '1')
        else:
            # A slight vertical change keeps the gesture horizontally dominant
            # while making both absolute axes observable in this fresh trace.
            sequence = swipe_point(0) + ['mousedown', '1', 'sleep', '.05']
            for step in range(1, 11):
                sequence += ['sleep', '.018'] + swipe_point(step)
            sequence += ['sleep', '.05', 'mouseup', '1']
            try:
                command(*sequence)
            finally:
                command('mouseup', '1')
    print('Analytics hardware gesture: owned emulator X11 pointer', flush=True)
    return {'passed': True}
