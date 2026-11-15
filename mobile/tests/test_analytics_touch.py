"""Closed emulator hardware gestures; no emulator or socket needed."""
import unittest
from analytics_touch import axes, frames

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

if __name__ == '__main__':
    unittest.main()
