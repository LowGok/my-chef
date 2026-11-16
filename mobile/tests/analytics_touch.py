"""Measured kernel touch input for the run-owned emulator and actual TalkBack."""
from contextlib import contextmanager
from pathlib import Path
import os
import re
import subprocess
import tempfile
import time


def touch_facts(text, contacts=1, *, device=None):
    if type(contacts) is not int or contacts not in (1, 2) or not isinstance(text, str) or len(text) > 65536:
        raise ValueError('Bounded input trace required')
    if device is not None:
        if not isinstance(device, str) or not re.fullmatch(r'/dev/input/event[0-9]{1,3}', device):
            raise ValueError('Bounded input trace required')
        text = '\n'.join(line for line in text.splitlines() if re.search(r'(?:^|\s)' + re.escape(device) + r':\s', line))
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
def touch_trace(native, contacts, *, device=None):
    # A keyboard header precedes opening the touch device on some boots. Wait
    # for every measured MT device's own header before releasing the gesture.
    adb = str(Path(native.ENV['ANDROID_HOME']) / 'platform-tools/adb')
    expected = touch_devices(native.command(adb, '-s', 'emulator-5554', 'shell', 'getevent', '-lp', timeout=10))
    if device is not None and device not in expected:
        raise RuntimeError('Emulator touchscreen observer unavailable')
    with tempfile.TemporaryFile() as output:
        # The remote cap also cleans up if the host connection disappears.
        process = subprocess.Popen([adb, '-s', 'emulator-5554', 'shell', 'timeout', '20', 'getevent', '-lt'],
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
                if touch_facts(raw.decode(errors='replace'), contacts, device=device)['touchscreen'] or process.poll() is not None:
                    break
                time.sleep(.02)
        finally:
            native.terminate(process)
        output.seek(0)
        raw = output.read(65537)
        if len(raw) > 65536:
            raise RuntimeError('Emulator touchscreen observer unavailable')
        facts = touch_facts(raw.decode(errors='replace'), contacts, device=device)
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


def reader_touch_devices(snapshot):
    """Map Android 13's configured touchscreen sources to enabled kernel nodes.

    InputReader IDs are not EventHub IDs. Only its explicit EventHub Devices
    list establishes that mapping; names and coincidentally equal IDs do not.
    The raw dump stays in memory because other sections can contain history.
    """
    message = 'Expected configured emulator touchscreen'
    if not isinstance(snapshot, str) or len(snapshot) > 524288:
        raise ValueError(message)
    sections = re.split(r'(?m)^(?=\S)', snapshot)
    hubs = [part for part in sections if part.startswith('Event Hub State:\n')]
    readers = [part for part in sections if re.match(r'Input Reader State(?: \([^\n]*\))?:\n', part)]
    if len(hubs) != 1 or len(readers) != 1:
        raise ValueError(message)
    paths = {}
    for block in re.split(r'(?m)(?=^    [0-9]+: )', hubs[0])[1:]:
        header = re.match(r'    ([0-9]{1,6}): [^\n]*\n', block)
        if header is None or int(header[1]) in paths or len(paths) >= 128:
            raise ValueError(message)
        fields = re.findall(r'^      Path: (/dev/input/event[0-9]{1,3})$', block, re.M)
        enabled = re.findall(r'^      Enabled: (true|false)$', block, re.M)
        # Virtual/non-event devices are excluded; duplicate IDs remain refused.
        paths[int(header[1])] = fields[0] if len(fields) == 1 and enabled == ['true'] else None
    result, seen = set(), set()
    for block in re.split(r'(?m)(?=^  Device [0-9]+: )', readers[0])[1:]:
        header = re.match(r'  Device ([0-9]{1,6}): [^\n]*\n', block)
        if header is None or int(header[1]) in seen or len(seen) >= 128:
            raise ValueError(message)
        seen.add(int(header[1]))
        sources = re.findall(r'^    Sources: 0x([0-9a-fA-F]{8})$', block, re.M)
        if len(sources) != 1:
            raise ValueError(message)
        if int(sources[0], 16) & 0x00001002 != 0x00001002:
            continue
        mappings = re.findall(r'^    EventHub Devices: \[([ 0-9,]+)\] *$', block, re.M)
        if len(mappings) != 1 or not re.fullmatch(r'\s*[0-9]{1,6}(?:[ ,]+[0-9]{1,6})*\s*', mappings[0]):
            raise ValueError(message)
        ids = [int(value) for value in re.findall(r'[0-9]+', mappings[0])]
        if len(ids) != len(set(ids)) or any(paths.get(value) is None for value in ids):
            raise ValueError(message)
        result.update(paths[value] for value in ids)
    if not 1 <= len(result) <= 16:
        raise ValueError(message)
    return result


def select_touch_device(snapshot, *, reader_paths=None, diagnostics=False):
    """Select one measured protocol-B touchscreen; never guess a node."""
    if not isinstance(snapshot, str) or len(snapshot) > 65536:
        raise ValueError('Expected one emulator touch device')
    if reader_paths is not None and (type(reader_paths) is not set or not 1 <= len(reader_paths) <= 16
            or any(not isinstance(path, str) or not re.fullmatch(r'/dev/input/event[0-9]{1,3}', path)
                   for path in reader_paths)):
        raise ValueError('Expected one emulator touch device')
    devices = []
    counts = dict(configured=0, axes=0, slots=0, tracking=0)
    for block in re.split(r'(?m)(?=^add device [0-9]+: )', snapshot):
        header = re.match(r'add device [0-9]+: (/dev/input/event[0-9]{1,3})\r?\n', block)
        if header is None or (header[1] not in reader_paths if reader_paths is not None
                              else not re.search(r'\bINPUT_PROP_DIRECT\b', block)):
            continue
        def extent(name):
            match = re.search(r'\b' + name + r'\b[^\n]*?\bmin\s+(-?[0-9]+),?\s+max\s+([0-9]+)', block)
            return tuple(map(int, match.groups())) if match else None
        counts['configured'] += 1
        x, y = extent('ABS_MT_POSITION_X'), extent('ABS_MT_POSITION_Y')
        slots, tracking = extent('ABS_MT_SLOT'), extent('ABS_MT_TRACKING_ID')
        if any(value is None or not 0 <= value[0] < value[1] <= 65535 or value[1] - value[0] < 64 for value in (x, y)):
            continue
        counts['axes'] += 1
        if slots is None or slots[0] != 0 or not 0 <= slots[1] <= 31:
            continue
        counts['slots'] += 1
        if tracking is None or tracking[0] != 0 or not 1 <= tracking[1] <= 65535:
            continue
        counts['tracking'] += 1
        pressure = extent('ABS_MT_PRESSURE')
        if 'ABS_MT_PRESSURE' in block and (pressure is None or pressure[0] != 0 or not 1 <= pressure[1] <= 65535):
            continue
        devices.append(dict(path=header[1], x=x, y=y, pressure=pressure,
                            button=bool(re.search(r'\bBTN_TOUCH\b', block))))
    if diagnostics:
        print('Analytics kernel touch candidates: ' + ' '.join(name + '=' + str(value) for name, value in counts.items())
              + ' selected=' + str(len(devices)), flush=True)
    if len(devices) != 1:
        raise ValueError('Expected one emulator touch device')
    return devices[0]


def kernel_frames(kind, device):
    """Closed Linux MT frames, not application events or accessibility actions."""
    if (kind not in ('double-tap', 'next', 'release') or type(device) is not dict
            or set(device) != {'path', 'x', 'y', 'pressure', 'button'}
            or not isinstance(device['path'], str) or not re.fullmatch(r'/dev/input/event[0-9]{1,3}', device['path'])
            or type(device['button']) is not bool):
        raise ValueError('Closed hardware gesture required')
    for name in ('x', 'y'):
        value = device[name]
        if (type(value) is not tuple or len(value) != 2 or any(type(n) is not int for n in value)
                or not 0 <= value[0] < value[1] <= 65535 or value[1] - value[0] < 64):
            raise ValueError('Closed hardware gesture required')
    pressure = device['pressure']
    if pressure is not None and (type(pressure) is not tuple or len(pressure) != 2
            or any(type(n) is not int for n in pressure) or pressure[0] != 0 or not 1 <= pressure[1] <= 65535):
        raise ValueError('Closed hardware gesture required')
    def point(axis, fraction):
        low, high = device[axis]
        return round(low + (high - low) * fraction)
    def frame(x=None, y=None, tracking=None, pressed=None):
        events = [(3, 47, 0)]
        if tracking is not None: events.append((3, 57, tracking))
        if x is not None: events.extend(((3, 53, point('x', x)), (3, 54, point('y', y))))
        if pressed is not None:
            if pressure is not None: events.append((3, 58, max(1, pressure[1] // 2) if pressed else 0))
            if device['button']: events.append((1, 330, int(pressed)))
        return tuple(events + [(0, 0, 0)])
    release = frame(tracking=-1, pressed=False)
    if kind == 'release': return [(0, release)]
    if kind == 'double-tap':
        return [(0, frame(.48, .49, 0, True)), (.06, release),
                (.12, frame(.52, .51, 1, True)), (.06, release)]
    return [(0, frame(.3, .48, 0, True))] + [
        (.05 if step == 1 else .018, frame(.3 + .04 * step, .48 + .004 * step))
        for step in range(1, 11)] + [(.05, release)]


def kernel_script(kind, device):
    lines = ['set -eu']
    for delay, events in kernel_frames(kind, device):
        if delay: lines.append('sleep ' + format(delay, '.3f'))
        lines.extend('sendevent ' + device['path'] + ' ' + ' '.join(map(str, event)) for event in events)
    return '\n'.join(lines) + '\n'


def gesture(kind, native):
    if kind not in ('double-tap', 'next'):
        raise ValueError('Closed hardware gesture required')
    # Keep the same owned-emulator display/process gate. Kernel input removes
    # the Qt mouse translation, while InputReader/TalkBack and real UI results
    # remain mandatory. Toybox sendevent writes Linux input_event records.
    focus_display(native)
    adb = str(Path(native.ENV['ANDROID_HOME']) / 'platform-tools/adb')
    if native.command(adb, '-s', 'emulator-5554', 'shell', 'id', '-u', timeout=10) != '0':
        raise RuntimeError('Emulator touchscreen writer is not rooted')
    reader_paths = reader_touch_devices(native.command(
        adb, '-s', 'emulator-5554', 'shell', 'dumpsys', '-t', '5', 'input', timeout=10))
    device = select_touch_device(native.command(adb, '-s', 'emulator-5554', 'shell', 'getevent', '-lp', timeout=10),
                                 reader_paths=reader_paths, diagnostics=True)
    print('Analytics kernel touchscreen gesture: ' + kind, flush=True)
    def write(target):
        try:
            native.command(adb, '-s', 'emulator-5554', 'shell', 'sh', '-s',
                           data=kernel_script(target, device), timeout=10)
        except (OSError, RuntimeError, subprocess.SubprocessError):
            raise RuntimeError('Emulator touchscreen injection failed') from None
    with touch_trace(native, 2 if kind == 'double-tap' else 1, device=device['path']):
        try:
            write(kind)
        finally:
            # Release only; a failed injection never retries the activation.
            write('release')
    return {'passed': True}
