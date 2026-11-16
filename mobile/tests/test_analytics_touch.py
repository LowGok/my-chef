"""Closed gestures through this run's X11 emulator, without a real display."""
import unittest
from contextlib import nullcontext
from types import SimpleNamespace
from unittest.mock import patch
from analytics_touch import gesture


class DisplayGestureTest(unittest.TestCase):
    def setUp(self):
        trace = patch('analytics_touch.touch_trace', return_value=nullcontext())
        trace.start()
        self.addCleanup(trace.stop)
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

    def test_startup_focus_uses_the_owned_window_without_input(self):
        from analytics_touch import focus_display
        native, calls = self.native()
        self.assertEqual(focus_display(native), ('42', 420, 780))
        self.assertEqual(calls[-1], ('xdotool', 'windowfocus', '--sync', '42'))
        self.assertFalse(any('click' in call or 'mousedown' in call for call in calls))
        native, _ = self.native(owner='999')
        with self.assertRaises(RuntimeError): focus_display(native)

    def test_double_tap_targets_largest_visible_window_owned_by_this_emulator(self):
        native, calls = self.native()
        self.assertEqual(gesture('double-tap', native), {'passed': True})
        self.assertIn(('xdotool', 'search', '--onlyvisible', '--name', '^Android Emulator - mychef-ci(:5554)?$'), calls)
        self.assertIn(('xdotool', 'windowfocus', '--sync', '42'), calls)
        motion = calls[-1]
        self.assertEqual(motion.count('click'), 2)
        self.assertEqual(motion.count('mousemove'), 2)
        self.assertIn('198', motion); self.assertIn('222', motion)
        self.assertIn('sleep', motion)
        self.assertFalse(any('adb' in str(call) or 'auth' in str(call) for call in calls))

    def test_swipe_releases_button_and_uses_only_the_owned_display(self):
        native, calls = self.native()
        with patch('analytics_touch.time.sleep'):
            self.assertEqual(gesture('next', native), {'passed': True})
        motion = next(call for call in calls if 'mousedown' in call)
        self.assertGreater(motion.count('mousemove'), 5)
        self.assertIn('mouseup', motion)
        self.assertEqual(calls[-1], ('xdotool', 'mouseup', '1'))

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
            if args[1] == 'mousemove' and 'mousedown' in args:
                raise RuntimeError('Fictional display interruption')
            return result
        native.command = command
        with patch('analytics_touch.time.sleep'), self.assertRaises(RuntimeError):
            gesture('next', native)
        self.assertEqual(calls[-1], ('xdotool', 'mouseup', '1'))


class TouchTraceTest(unittest.TestCase):
    def test_double_tap_requires_two_complete_contacts(self):
        from analytics_touch import touch_facts
        contact = "EV_ABS ABS_MT_POSITION_X 00004000\nEV_ABS ABS_MT_POSITION_Y 00004000\nEV_ABS ABS_MT_TRACKING_ID 00000003\nEV_ABS ABS_MT_TRACKING_ID ffffffff\n"
        self.assertFalse(touch_facts(contact, contacts=2)['touchscreen'])
        self.assertTrue(touch_facts(contact + contact, contacts=2)['touchscreen'])
        # Linux omits unchanged ABS coordinates on a later contact.
        same_y = contact.replace('EV_ABS ABS_MT_POSITION_Y 00004000\n', '')
        self.assertTrue(touch_facts(contact + same_y, contacts=2)['touchscreen'])

    def test_mouse_or_incomplete_contact_is_not_touchscreen_evidence(self):
        from analytics_touch import touch_facts
        contact = "EV_ABS ABS_MT_POSITION_X 00004000\nEV_ABS ABS_MT_POSITION_Y 00004000\nEV_ABS ABS_MT_TRACKING_ID 00000003\n"
        release = "EV_ABS ABS_MT_TRACKING_ID ffffffff\n"
        self.assertTrue(touch_facts(contact + release)['touchscreen'])
        self.assertFalse(touch_facts(contact)['touchscreen'])
        self.assertFalse(touch_facts(release)['touchscreen'])
        self.assertFalse(touch_facts('EV_KEY BTN_LEFT DOWN\nEV_KEY BTN_LEFT UP\n')['touchscreen'])
        single = contact.replace('EV_ABS ABS_MT_TRACKING_ID 00000003', 'EV_KEY BTN_TOUCH DOWN')
        self.assertTrue(touch_facts(single + 'EV_KEY BTN_TOUCH UP\n')['touchscreen'])
        self.assertFalse(touch_facts('arbitrary text with ABS_MT_POSITION_X and DOWN')['touchscreen'])


if __name__ == '__main__': unittest.main()
