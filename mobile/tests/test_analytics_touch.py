"""Closed gestures through this run's X11 emulator, without a real display."""
import unittest
from types import SimpleNamespace
from unittest.mock import patch
from analytics_touch import gesture


class DisplayGestureTest(unittest.TestCase):
    def native(self, windows='42\n43\n', geometries=None, owner='101'):
        calls = []
        geometries = geometries or {'42': 'X=0\nY=0\nWIDTH=420\nHEIGHT=780\n',
                                   '43': 'X=420\nY=0\nWIDTH=50\nHEIGHT=500\n'}
        def command(*args, **kwargs):
            calls.append(args)
            if args[0] == 'ps': return '1'
            if args[1] == 'search': return windows
            if args[1] == 'getwindowpid': return owner
            if args[1] == 'getwindowgeometry': return geometries[args[-1]]
            return ''
        return SimpleNamespace(ENV={'DISPLAY': ':91', 'MYCHEF_ANALYTICS_EMULATOR_PID': '101'}, command=command), calls

    def test_double_tap_targets_largest_visible_window_owned_by_this_emulator(self):
        native, calls = self.native()
        self.assertEqual(gesture('double-tap', native), {'passed': True})
        self.assertIn(('xdotool', 'search', '--onlyvisible', '--name', '^Android Emulator - mychef-ci(:5554)?$'), calls)
        self.assertIn(('xdotool', 'mousemove', '--sync', '--window', '42', '210', '390'), calls)
        self.assertIn(('xdotool', 'click', '--repeat', '2', '--delay', '120', '1'), calls)
        self.assertFalse(any('adb' in str(call) or 'auth' in str(call) for call in calls))

    def test_swipe_releases_button_and_uses_only_the_owned_display(self):
        native, calls = self.native()
        with patch('analytics_touch.time.sleep'):
            self.assertEqual(gesture('next', native), {'passed': True})
        self.assertIn(('xdotool', 'mousedown', '1'), calls)
        self.assertEqual(calls[-1], ('xdotool', 'mouseup', '1'))
        self.assertGreater(len([call for call in calls if call[1] == 'mousemove']), 5)

    def test_missing_foreign_ambiguous_or_unbounded_windows_are_refused(self):
        for options in ({'windows': ''}, {'owner': '999'},
                        {'geometries': {'42': 'WIDTH=1\nHEIGHT=2\n', '43': 'WIDTH=1\nHEIGHT=2\n'}},
                        {'geometries': {'42': 'WIDTH=420\nHEIGHT=780\n', '43': 'WIDTH=420\nHEIGHT=780\n'}}):
            with self.subTest(options=options), self.assertRaises(RuntimeError):
                gesture('double-tap', self.native(**options)[0])
        native, _ = self.native(); native.ENV['DISPLAY'] = ':0'
        with self.assertRaises(RuntimeError): gesture('next', native)
        for target in ('click:1:2', '', None):
            with self.assertRaises(ValueError): gesture(target, self.native()[0])

    def test_failed_swipe_still_releases_the_host_button(self):
        native, calls = self.native()
        original = native.command
        def command(*args, **kwargs):
            result = original(*args, **kwargs)
            if args[1] == 'mousemove' and any(call[1] == 'mousedown' for call in calls):
                raise RuntimeError('Fictional display interruption')
            return result
        native.command = command
        with patch('analytics_touch.time.sleep'), self.assertRaises(RuntimeError):
            gesture('next', native)
        self.assertEqual(calls[-1], ('xdotool', 'mouseup', '1'))


if __name__ == '__main__': unittest.main()
