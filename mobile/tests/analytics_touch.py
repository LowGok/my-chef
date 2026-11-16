"""Fixed host gestures through the run-owned emulator's actual X11 window."""
import re
import time


def gesture(kind, native):
    if kind not in ('double-tap', 'next'):
        raise ValueError('Closed hardware gesture required')
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
    def move(fraction):
        command('mousemove', '--sync', '--window', window, str(round(width * fraction)), str(round(height / 2)))
    # QEMU's actual host pointer path chooses the correct touchscreen device.
    # Generic console event-send and UiAutomation injection do not prove that.
    move(.5 if kind == 'double-tap' else .3)
    if kind == 'double-tap':
        command('click', '--repeat', '2', '--delay', '120', '1')
    else:
        command('mousedown', '1')
        try:
            for step in range(1, 11):
                time.sleep(.018)
                move(.3 + .04 * step)
        finally:
            command('mouseup', '1')
    print('Analytics hardware gesture: owned emulator X11 pointer', flush=True)
    return {'passed': True}
