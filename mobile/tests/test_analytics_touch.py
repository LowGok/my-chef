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
        motion = next(call for call in calls if call.count('mousedown') == 2)
        self.assertEqual(motion.count('mouseup'), 2)
        self.assertEqual(motion.count('.05'), 2)
        self.assertEqual(motion.count('mousemove'), 2)
        self.assertIn('198', motion); self.assertIn('222', motion)
        self.assertIn('384', motion); self.assertIn('396', motion)
        self.assertIn('sleep', motion)
        self.assertEqual(calls[-1], ('xdotool', 'mouseup', '1'))
        self.assertFalse(any('adb' in str(call) or 'auth' in str(call) for call in calls))

    def test_swipe_releases_button_and_uses_only_the_owned_display(self):
        native, calls = self.native()
        with patch('analytics_touch.time.sleep'):
            self.assertEqual(gesture('next', native), {'passed': True})
        motion = next(call for call in calls if 'mousedown' in call)
        self.assertGreater(motion.count('mousemove'), 5)
        points = [(int(motion[i + 3]), int(motion[i + 4]))
                  for i, value in enumerate(motion) if value == 'mousemove']
        dx, dy = abs(points[-1][0] - points[0][0]), abs(points[-1][1] - points[0][1])
        self.assertGreater(dy, 0, 'A fresh trace must observe both absolute axes')
        self.assertGreater(dx, dy * 2, 'TalkBack navigation remains a horizontal swipe')
        self.assertIn('mouseup', motion)
        self.assertEqual(calls[-1], ('xdotool', 'mouseup', '1'))

    def test_swipe_starts_inside_the_phone_on_a_wide_emulator_canvas(self):
        # A centred portrait phone can occupy only the middle of a wide Qt
        # window. Pressing its host margins never creates a guest contact.
        native, calls = self.native(geometries={
            '42': 'WIDTH=1280\nHEIGHT=800\n', '43': 'WIDTH=50\nHEIGHT=500\n'})
        with patch('analytics_touch.time.sleep'):
            gesture('next', native)
        motion = next(call for call in calls if 'mousedown' in call)
        points = [(int(motion[i + 3]), int(motion[i + 4]))
                  for i, value in enumerate(motion) if value == 'mousemove']
        self.assertTrue(all(450 < x < 830 and 300 < y < 500 for x, y in points),
                        'The complete pressed swipe must stay in the centred phone surface')
        self.assertGreater(points[-1][0] - points[0][0], 120,
                           'A bounded swipe still needs meaningful physical travel')

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

    def test_failed_gesture_still_releases_the_host_button(self):
        for kind in ('double-tap', 'next'):
            with self.subTest(kind=kind):
                native, calls = self.native()
                original = native.command
                def command(*args, **kwargs):
                    result = original(*args, **kwargs)
                    if args[1] == 'mousemove' and 'mousedown' in args:
                        raise RuntimeError('Fictional display interruption')
                    return result
                native.command = command
                with patch('analytics_touch.time.sleep'), self.assertRaises(RuntimeError):
                    gesture(kind, native)
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


class TouchObserverReadyTest(unittest.TestCase):
    profile = ('add device 1: /dev/input/event0\n  name: "keyboard"\n'
               'add device 2: /dev/input/event7\n  name: "touchscreen"\n'
               '  ABS_MT_POSITION_X ABS_MT_POSITION_Y ABS_MT_TRACKING_ID\n')
    contact = ('EV_ABS ABS_MT_POSITION_X 00004000\nEV_ABS ABS_MT_POSITION_Y 00004000\n'
               'EV_ABS ABS_MT_TRACKING_ID 00000003\nEV_ABS ABS_MT_TRACKING_ID ffffffff\n')

    def exercise(self, headers, ended=False):
        import analytics_touch
        from unittest.mock import Mock
        process = SimpleNamespace(poll=lambda: 0 if ended else None)
        native = SimpleNamespace(ENV={'ANDROID_HOME': '/fictional-sdk'},
                                 command=Mock(return_value=self.profile), terminate=Mock())
        def start(*_, **fields):
            fields['stdout'].write((self.contact * 2).encode())
            fields['stdout'].flush()
            return process
        with patch.object(analytics_touch.subprocess, 'Popen', side_effect=start), \
             patch.object(analytics_touch.os, 'pread', side_effect=headers) as read, \
             patch.object(analytics_touch.time, 'sleep'):
            with analytics_touch.touch_trace(native, 2):
                self.assertGreaterEqual(read.call_count, 2, 'A keyboard header cannot release the gesture')
        native.terminate.assert_called_once_with(process)

    def test_gesture_waits_until_the_actual_touch_device_is_open(self):
        self.exercise([b'add device 1: /dev/input/event0\n',
                       b'add device 1: /dev/input/event0\nadd device 2: /dev/input/event7\n',
                       (self.contact * 2).encode()])

    def test_ended_observer_without_touch_device_prevents_the_gesture(self):
        with self.assertRaisesRegex(RuntimeError, 'observer unavailable'):
            self.exercise([b'add device 1: /dev/input/event0\n'], ended=True)

    def test_touch_device_selection_requires_both_mt_axes_and_bounded_real_paths(self):
        from analytics_touch import touch_devices
        self.assertEqual(touch_devices(self.profile), {'/dev/input/event7'})
        for profile in (None, '', 'x' * 65537, self.profile.replace('ABS_MT_POSITION_Y', ''),
                        self.profile.replace('/dev/input/event7', '/fictional/private-device')):
            with self.assertRaisesRegex(RuntimeError, 'observer unavailable'):
                touch_devices(profile)


if __name__ == '__main__': unittest.main()
