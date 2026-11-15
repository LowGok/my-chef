"""Run-owned emulator hardware events, through InputReader and TalkBack."""
from pathlib import Path
import re
import socket
import time


def axes(text):
    found = []
    for block in re.split(r'(?=add device [0-9]+: /dev/input/event[0-9]+)', text):
        if 'BTN_TOUCH' not in block:
            continue
        values = []
        for axis in ('X', 'Y'):
            match = re.search(r'ABS_MT_POSITION_' + axis + r'\s*:\s*value\s+-?[0-9]+,\s*min\s+(-?[0-9]+),\s*max\s+([0-9]+)', block)
            if match is None:
                break
            values.extend(map(int, match.groups()))
        if len(values) == 4 and all(0 <= values[i] < values[i+1] <= 65535 and values[i+1] - values[i] >= 64 for i in (0, 2)):
            found.append((*values, 'ABS_MT_SLOT' in block))
    if len(found) != 1:
        raise ValueError('Expected one emulator touch device')
    return found[0]


def frames(kind, touch):
    if (kind not in ('double-tap', 'next') or type(touch) is not tuple or len(touch) != 5
            or any(type(value) is not int for value in touch[:4]) or type(touch[4]) is not bool
            or any(not 0 <= touch[i] < touch[i+1] <= 65535 or touch[i+1] - touch[i] < 64 for i in (0, 2))):
        raise ValueError('Closed hardware gesture required')
    low_x, high_x, low_y, high_y, slots = touch
    def position(fraction):
        return round(low_x + (high_x - low_x) * fraction)
    y = round((low_y + high_y) / 2)
    def frame(x=None, tracking=None, pressed=None):
        result = ['EV_ABS:ABS_MT_SLOT:0'] if slots else []
        if tracking is not None:
            result.append('EV_ABS:ABS_MT_TRACKING_ID:' + str(tracking))
        if x is not None:
            result.extend(('EV_ABS:ABS_MT_POSITION_X:' + str(x), 'EV_ABS:ABS_MT_POSITION_Y:' + str(y)))
        if pressed is not None:
            result.append('EV_KEY:BTN_TOUCH:' + str(pressed))
        if not slots:
            result.append('EV_SYN:SYN_MT_REPORT:0')
        return tuple(result + ['EV_SYN:0:0'])
    if kind == 'double-tap':
        return [(0, frame(position(.5), 3, 1)), (.04, frame(tracking=-1, pressed=0)),
                (.08, frame(position(.5), 4, 1)), (.04, frame(tracking=-1, pressed=0))]
    return [(0, frame(position(.3), 5, 1))] + [(.018, frame(position(.3 + .04 * step)))
            for step in range(1, 11)] + [(.018, frame(tracking=-1, pressed=0))]


def gesture(kind, native):
    if kind not in ('double-tap', 'next'):
        raise ValueError('Closed hardware gesture required')
    adb = str(Path(native.ENV['ANDROID_HOME']) / 'platform-tools/adb')
    touch = axes(native.command(adb, '-s', 'emulator-5554', 'shell', 'getevent', '-lp', timeout=15))
    token_path = Path(native.ENV['HOME']) / '.emulator_console_auth_token'
    if token_path.stat().st_size > 256:
        raise RuntimeError('Emulator console authentication refused')
    token = token_path.read_text().strip()
    if not re.fullmatch(r'[A-Za-z0-9_-]{16,128}', token):
        raise RuntimeError('Emulator console authentication refused')
    # Port/commands are fixed to this CI fixture. Never emit the console
    # banner, token, raw device capabilities, command output or private paths.
    with socket.create_connection(('127.0.0.1', 5554), timeout=5) as connection:
        connection.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        with connection.makefile('rb') as reader:
            def response():
                size = 0
                while size < 16384:
                    line = reader.readline(1024)
                    size += len(line)
                    if line.strip() == b'OK':
                        return
                    if not line or line.startswith(b'KO'):
                        break
                raise RuntimeError('Emulator hardware command refused')
            response()
            connection.sendall(('auth ' + token + '\n').encode('ascii'))
            response()
            for delay, events in frames(kind, touch):
                time.sleep(delay)
                connection.sendall(('event send ' + ' '.join(events) + '\n').encode('ascii'))
                response()
            connection.sendall(b'quit\n')
    return {'passed': True}
