"""Closed emulator hardware gestures; no emulator or socket needed."""
import unittest
import io
import tempfile
from contextlib import redirect_stdout
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch
from analytics_touch import axes, frames, gesture

CAPS = '''add device 1: /dev/input/event1
  name: "virtio_input_multi_touch_1"
  events:
    BTN_TOUCH
    ABS_MT_SLOT : value 0, min 0, max 9
    ABS_MT_POSITION_X : value 0, min 0, max 32767
    ABS_MT_POSITION_Y : value 0, min 0, max 32767
'''

class HardwareGestureTest(unittest.TestCase):
    def test_touch_devices_must_share_one_consistent_geometry_profile(self):
        self.assertEqual(axes(CAPS), (0, 32767, 0, 32767, True))
        self.assertEqual(axes(CAPS + CAPS), axes(CAPS))
        self.assertEqual(axes(CAPS.replace('BTN_TOUCH', 'KEY_POWER')), axes(CAPS))
        self.assertEqual(axes(CAPS.replace('value 0, ', '')), axes(CAPS))
        for text in ('', CAPS.replace('max 32767', 'max 1'),
                     CAPS + CAPS.replace('max 32767', 'max 1080')):
            with self.subTest(text=text), self.assertRaises(ValueError):
                axes(text)

    def test_double_tap_and_swipe_are_bounded_complete_hardware_frames(self):
        touch = axes(CAPS)
        taps = frames('double-tap', touch)
        self.assertEqual(len(taps), 4)
        self.assertLess(sum(delay for delay, _ in taps), .3)
        self.assertEqual(sum('EV_KEY:BTN_TOUCH:1' in event for _, event in taps), 2)
        self.assertEqual(sum('EV_KEY:BTN_TOUCH:0' in event for _, event in taps), 2)
        self.assertTrue(all(event[-1] == 'EV_SYN:0:0' for _, event in taps))
        swipe = frames('next', touch)
        self.assertGreater(len(swipe), 5)
        self.assertIn('EV_ABS:ABS_MT_TRACKING_ID:-1', swipe[-1][1])
        self.assertIn('EV_KEY:BTN_TOUCH:0', swipe[-1][1])
        protocol_a = frames('next', (*touch[:4], False))
        self.assertTrue(all('EV_ABS:ABS_MT_TRACKING_ID:5' in event for _, event in protocol_a[:-1]))
        self.assertTrue(all('EV_SYN:SYN_MT_REPORT:0' in event for _, event in protocol_a))
        for kind in ('shell', 'click:1:2', '', None):
            with self.assertRaises(ValueError):
                frames(kind, touch)
        for bad in ((False, 32767, 0, 32767, True), (0, 1, 0, 1, True),
                    (0, 32767, 0, 32767, 'true')):
            with self.assertRaises(ValueError):
                frames('next', bad)

class ConsoleHandshakeTest(unittest.TestCase):
    def run_gesture(self, directory, required):
        banner = b'Android Console: Authentication required\nOK\n' if required else b'Android Console: ready\nOK\n'
        class Connection:
            def __init__(self):
                self.sent = []
            def __enter__(self): return self
            def __exit__(self, *args): pass
            def setsockopt(self, *args): pass
            def makefile(self, mode): return io.BytesIO(banner + b'OK\n' * 8)
            def sendall(self, data): self.sent.append(data)
        connection = Connection()
        native = SimpleNamespace(ENV={'HOME': directory, 'ANDROID_HOME': directory}, command=lambda *args, **kwargs: CAPS)
        output = io.StringIO()
        with patch('analytics_touch.socket.create_connection', return_value=connection) as connect, patch('analytics_touch.time.sleep'), redirect_stdout(output):
            result = gesture('double-tap', native)
        connect.assert_called_once_with(('127.0.0.1', 5554), timeout=5)
        return result, connection.sent, output.getvalue()

    def test_console_without_auth_does_not_read_or_create_a_token(self):
        with tempfile.TemporaryDirectory(prefix='analytics-console-') as directory:
            result, sent, _ = self.run_gesture(directory, False)
            self.assertEqual(result, {'passed': True})
            self.assertFalse(any(command.startswith(b'auth ') for command in sent))
            self.assertEqual(list(Path(directory).iterdir()), [])

    def test_required_auth_accepts_printable_tokens_without_logging_them(self):
        with tempfile.TemporaryDirectory(prefix='analytics-console-') as directory:
            token = 'fictional/private+console=token'
            (Path(directory) / '.emulator_console_auth_token').write_text(token + '\n')
            result, sent, output = self.run_gesture(directory, True)
            self.assertEqual(result, {'passed': True})
            self.assertIn(('auth ' + token + '\n').encode(), sent)
            self.assertNotIn(token, output)
            (Path(directory) / '.emulator_console_auth_token').write_text('invalid\nquit')
            with self.assertRaisesRegex(RuntimeError, 'authentication refused'):
                self.run_gesture(directory, True)

if __name__ == '__main__':
    unittest.main()
