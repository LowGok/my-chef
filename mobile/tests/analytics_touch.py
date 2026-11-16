"""Measured kernel touch input for the run-owned emulator and actual TalkBack."""
from contextlib import contextmanager
from pathlib import Path
import os
import re
import struct
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


def contact_timing(text, device):
    """Summarize completed selected-node SYN frames; never publish timestamps."""
    if (not isinstance(text, str) or len(text) > 65536 or not isinstance(device, str)
            or not re.fullmatch(r'/dev/input/event[0-9]{1,3}', device)):
        raise ValueError('Bounded input trace required')
    pattern = (r'^\[\s*([0-9]{1,10}\.[0-9]{1,9})\]\s+' + re.escape(device)
               + r':\s+(EV_ABS\s+ABS_MT_TRACKING_ID|EV_SYN\s+SYN_REPORT)\s+([0-9a-f]{8})\b')
    pending, start, pairs = None, None, []
    for line in text.splitlines():
        match = re.match(pattern, line)
        if match is None:
            continue
        if match[2].startswith('EV_ABS'):
            value = int(match[3], 16)
            if pending is not None or (value >= 0x80000000 and value != 0xffffffff):
                return None
            pending = value != 0xffffffff
        elif pending is not None:
            at = float(match[1])
            if pending:
                if start is not None:
                    return None
                start = at
            else:
                if start is None or not 0 <= at - start <= 10:
                    return None
                pairs.append((start, at)); start = None
            pending = None
    if start is not None or pending is not None or not 1 <= len(pairs) <= 2:
        return None
    gaps = [pairs[i][0] - pairs[i - 1][1] for i in range(1, len(pairs))]
    if any(not 0 <= gap <= 10 for gap in gaps):
        return None
    return dict(holds=[round((end - begin) * 1000) for begin, end in pairs],
                gaps=[round(gap * 1000) for gap in gaps])


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
    if device is not None:
        timing = contact_timing(raw.decode(errors='replace'), device)
        print('Analytics contact timing: ' + ('unavailable' if timing is None else
              'holdsMs=' + ','.join(map(str, timing['holds']))
              + ' gapsMs=' + ','.join(map(str, timing['gaps']))), flush=True)
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


def reader_touch_devices(snapshot, *, diagnostics=False):
    bounded = isinstance(snapshot, str) and len(snapshot) <= 524288
    counts = dict(bytes=len(snapshot) if bounded else 0,
                  hubHeaders=snapshot.count('Event Hub State:') if bounded else 0,
                  readerHeaders=snapshot.count('Input Reader State') if bounded else 0,
                  stage=0, hubs=0, readers=0, enabled=0, touch=0, mapped=0)
    try:
        return _reader_touch_devices(snapshot, counts, diagnostics=diagnostics)
    finally:
        if diagnostics:
            print('Analytics input reader: ' + ' '.join(key + '=' + str(value) for key, value in counts.items()), flush=True)


def _reader_touch_devices(snapshot, counts, *, diagnostics=False):
    """Map Android 13's configured touchscreen sources to enabled kernel nodes.

    InputReader IDs are not EventHub IDs. Only its explicit EventHub Devices
    list establishes that mapping; names and coincidentally equal IDs do not.
    The raw dump stays in memory because other sections can contain history.
    """
    message = 'Expected configured emulator touchscreen'
    if not isinstance(snapshot, str) or len(snapshot) > 524288:
        raise ValueError(message)
    counts['stage'] = 1
    sections = re.split(r'(?m)^(?=\S)', snapshot)
    hubs = [part for part in sections if part.startswith('Event Hub State:\n')]
    readers = [part for part in sections if re.match(r'Input Reader State(?: \([^\n]*\))?:\n', part)]
    if len(hubs) != 1 or len(readers) != 1:
        raise ValueError(message)
    counts['stage'] = 2
    paths, disabled, aliases = {}, set(), set()
    for block in re.split(r'(?m)(?=^    -?[0-9]+: )', hubs[0])[1:]:
        header = re.match(r'    (-?[0-9]{1,6}): [^\n]*\n', block)
        if header is None or int(header[1]) in paths or len(paths) >= 128:
            raise ValueError(message)
        if int(header[1]) < 0:
            continue  # Reserved virtual records have no writable kernel node.
        fields = re.findall(r'^      Path: (/dev/input/event[0-9]{1,3})$', block, re.M)
        enabled = re.findall(r'^      Enabled: (true|false)$', block, re.M)
        if header[0].endswith(' (aka device 0 - built-in keyboard)\n'):
            aliases.add(int(header[1]))
        if len(fields) == 1 and enabled == ['false']:
            disabled.add(int(header[1]))
        # Virtual/non-event devices are excluded; duplicate IDs remain refused.
        paths[int(header[1])] = fields[0] if len(fields) == 1 and enabled == ['true'] else None
        counts['hubs'] += 1
        counts['enabled'] += int(paths[int(header[1])] is not None)
    builtins = re.findall(r'^  BuiltInKeyboardId: (-?[0-9]{1,6})$', hubs[0], re.M)
    if len(builtins) > 1:
        raise ValueError(message)
    if builtins and int(builtins[0]) >= 0:
        builtin = int(builtins[0])
        # EventHub explicitly reports built-in keyboard events as device 0.
        # Require both its root ID and the corresponding node's alias label.
        if builtin not in paths or builtin not in aliases or (0 in paths and paths[0] != paths[builtin]):
            raise ValueError(message)
        paths[0] = paths[builtin]
        if builtin in disabled:
            disabled.add(0)
    counts['stage'] = 3
    result, seen = set(), set()
    for block in re.split(r'(?m)(?=^  Device -?[0-9]+: )', readers[0])[1:]:
        header = re.match(r'  Device (-?[0-9]{1,6}): [^\n]*\n', block)
        if header is None or int(header[1]) in seen or len(seen) >= 128:
            raise ValueError(message)
        if int(header[1]) < 0:
            continue
        seen.add(int(header[1]))
        counts['readers'] += 1
        sources = re.findall(r'^    Sources: ([^\n]{1,256})$', block, re.M)
        if len(sources) != 1:
            raise ValueError(message)
        source = sources[0]
        if re.fullmatch(r'0x[0-9a-fA-F]{8}', source):
            mask = int(source, 16)
            touchscreen = mask != 0xffffffff and mask & 0x00001002 == 0x00001002
        elif source in ('UNKNOWN', 'ANY'):
            touchscreen = False
        else:
            # Patched Android 13 prints inputEventSourceToString's closed
            # source names. They are metadata, not arbitrary device names.
            labels = source.split(' | ')
            known = {'KEYBOARD', 'DPAD', 'GAMEPAD', 'TOUCHSCREEN', 'MOUSE', 'STYLUS',
                     'BLUETOOTH_STYLUS', 'TRACKBALL', 'MOUSE_RELATIVE', 'TOUCHPAD',
                     'TOUCH_NAVIGATION', 'JOYSTICK', 'HDMI', 'SENSOR', 'ROTARY_ENCODER'}
            if len(labels) != len(set(labels)) or not set(labels) <= known:
                raise ValueError(message)
            touchscreen = 'TOUCHSCREEN' in labels
        if not touchscreen:
            continue
        counts['touch'] += 1
        mappers = [part for part in re.split(r'(?m)(?=^    \S)', block)
                   if part.startswith('    Touch Input Mapper (mode - DIRECT):\n')]
        viewports = re.findall(r'^      Viewport (INTERNAL|EXTERNAL|VIRTUAL): displayId=(-?[0-9]{1,6}), '
                               r'[^\n]{0,4096}isActive=\[([01])\]$', mappers[0], re.M) if len(mappers) == 1 else []
        primary = viewports == [('INTERNAL', '0', '1')]
        if diagnostics:
            print('Analytics reader display: direct=' + str(int(len(mappers) == 1))
                  + ' primary=' + str(int(primary)), flush=True)
        if not primary:
            continue
        counts['stage'] = 4
        mappings = re.findall(r'^    EventHub Devices: \[([ 0-9,]+)\] *$', block, re.M)
        if len(mappings) != 1 or not re.fullmatch(r'\s*[0-9]{1,6}(?:[ ,]+[0-9]{1,6})*\s*', mappings[0]):
            raise ValueError(message)
        ids = [int(value) for value in re.findall(r'[0-9]+', mappings[0])]
        counts['stage'] = 5
        if not 1 <= len(ids) <= 128 or len(ids) != len(set(ids)):
            raise ValueError(message)
        missing = sum(value not in paths for value in ids)
        unusable = sum(value in paths and paths[value] is None and value not in disabled for value in ids)
        disabled_count = sum(value in disabled for value in ids)
        if diagnostics:
            print('Analytics reader mapping: nodes=' + str(len(ids)) + ' missing=' + str(missing)
                  + ' unusable=' + str(unusable) + ' disabled=' + str(disabled_count), flush=True)
        # Only complete, enabled kernel mappings qualify. Virtual/unmappable
        # readers cannot supply a writer candidate; exclude the entire mapping
        # instead of borrowing a source from its other nodes or guessing IDs.
        if missing or unusable or disabled_count:
            counts['stage'] = 3
            continue
        result.update(paths[value] for value in ids)
        counts['mapped'] = len(result)
        counts['stage'] = 3
    counts['stage'] = 6
    if not 1 <= len(result) <= 16:
        raise ValueError(message)
    counts['stage'] = 7
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
        if diagnostics:
            def bounded(value):
                if value is None:
                    return 'missing'
                if any(not -2147483648 <= number <= 2147483647 for number in value):
                    return 'out-of-range'
                return ':'.join(map(str, value))
            print('Analytics kernel touch extents: x=' + bounded(x) + ' y=' + bounded(y)
                  + ' slots=' + bounded(slots) + ' tracking=' + bounded(tracking), flush=True)
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
        return [(0, frame(.498, .499, 0, True)), (.025, release),
                (.08, frame(.502, .501, 1, True)), (.025, release)]
    return [(0, frame(.3, .48, 0, True))] + [
        (.05 if step == 1 else .018, frame(.3 + .04 * step, .48 + .004 * step))
        for step in range(1, 11)] + [(.05, release)]


def kernel_script(kind, device):
    frames = kernel_frames(kind, device)
    # Pin the existing character device in the parent. Keep one dd open for
    # the whole gesture, so process startup cannot lengthen every contact.
    # It buffers partial pipe reads into whole 24-byte x86_64 input records.
    lines = ['set -eu', "printf 'ANALYTICS_TOUCH_STAGE=start\\n' >&2", 'exec 3< ' + device['path'],
             "printf 'ANALYTICS_TOUCH_STAGE=open\\n' >&2", 'test -c /proc/$$/fd/3',
             "printf 'ANALYTICS_TOUCH_STAGE=character\\n' >&2", '{']
    for index, (delay, events) in enumerate(frames):
        if delay: lines.append('sleep ' + format(delay, '.3f'))
        raw = b''.join(struct.pack('<qqHHi', 0, 0, *event) for event in events)
        encoded = ''.join('\\0' + format(value, '03o') for value in raw)
        lines.append("printf 'ANALYTICS_TOUCH_STAGE=frame-start-" + str(index) + "\\n' >&2")
        lines.append("printf '%b' '" + encoded + "'")
        lines.append("printf 'ANALYTICS_TOUCH_STAGE=frame-done-" + str(index) + "\\n' >&2")
    lines.append('} | dd of=/proc/$$/fd/3 ibs=24 obs=24 conv=notrunc status=none')
    return '\n'.join(lines) + '\n'


def writer_failure(error):
    """Expose only fixed writer steps/reasons, never captured command output."""
    raw = str(error)[-4096:]
    stages = re.findall(r'(?m)^ANALYTICS_TOUCH_STAGE=(start|open|character|frame-(?:start|done)-(?:[0-9]|1[01]))$', raw)
    reason = next((code for phrase, code in (
        ('Permission denied', 'permission'), ('No such file or directory', 'missing'),
        ('Bad file descriptor', 'descriptor'), ('unknown operand', 'unsupported-option'),
        ('Unknown option', 'unsupported-option'), ('Invalid argument', 'argument')) if phrase in raw), 'unknown')
    return dict(stage=stages[-1] if stages else 'unknown', reason=reason)


def gesture(kind, native):
    if kind not in ('double-tap', 'next'):
        raise ValueError('Closed hardware gesture required')
    # Keep the same owned-emulator display/process gate. Kernel input removes
    # the Qt mouse translation, while InputReader/TalkBack and real UI results
    # remain mandatory. Buffer each frame rather than forking per input record.
    focus_display(native)
    adb = str(Path(native.ENV['ANDROID_HOME']) / 'platform-tools/adb')
    if native.command(adb, '-s', 'emulator-5554', 'shell', 'id', '-u', timeout=10) != '0':
        raise RuntimeError('Emulator touchscreen writer is not rooted')
    if native.command(adb, '-s', 'emulator-5554', 'shell', 'uname', '-m', timeout=10) != 'x86_64':
        raise RuntimeError('Unsupported emulator touch ABI')
    reader_paths = reader_touch_devices(native.command(
        adb, '-s', 'emulator-5554', 'shell', 'dumpsys', '-t', '5', 'input', timeout=10), diagnostics=True)
    device = select_touch_device(native.command(adb, '-s', 'emulator-5554', 'shell', 'getevent', '-lp', timeout=10),
                                 reader_paths=reader_paths, diagnostics=True)
    print('Analytics kernel touchscreen gesture: ' + kind, flush=True)
    def write(target):
        try:
            native.command(adb, '-s', 'emulator-5554', 'shell', 'sh', '-s',
                           data=kernel_script(target, device), timeout=10)
        except (OSError, RuntimeError, subprocess.SubprocessError) as error:
            facts = writer_failure(error)
            print('Analytics kernel writer refusal: target=' + target + ' stage=' + facts['stage']
                  + ' reason=' + facts['reason'], flush=True)
            raise RuntimeError('Emulator touchscreen injection failed') from None
    with touch_trace(native, 2 if kind == 'double-tap' else 1, device=device['path']):
        try:
            write(kind)
        finally:
            # Release only; a failed injection never retries the activation.
            write('release')
    return {'passed': True}
