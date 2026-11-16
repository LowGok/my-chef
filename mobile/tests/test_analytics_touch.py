"""Closed emulator input contracts; these fakes never claim native evidence."""
import unittest
import io
from contextlib import redirect_stdout
from contextlib import contextmanager
from types import SimpleNamespace
from unittest.mock import patch
import analytics_touch as touch

PROFILE = ("add device 1: /dev/input/event7\n  name: \"fictional touchscreen\"\n"
           "  EV_KEY: BTN_TOUCH\n"
           "  ABS_MT_SLOT: value 0, min 0, max 9\n"
           "  ABS_MT_POSITION_X: value 0, min 0, max 32767\n"
           "  ABS_MT_POSITION_Y: value 0, min 0, max 32767\n"
           "  ABS_MT_TRACKING_ID: value -1, min 0, max 65535\n"
           "  ABS_MT_PRESSURE: value 0, min 0, max 255\n"
           "  input props: INPUT_PROP_DIRECT\n")

# Android 13 dump format; fictional IDs intentionally differ between layers.
READER = ('Event Hub State:\n  Devices:\n    12: fictional touchscreen\n'
          '      Path: /dev/input/event7\n      Enabled: true\n'
          '\nInput Reader State (Nums of device: 1):\n  Device 27: fictional touchscreen\n'
          '    EventHub Devices: [ 12 ] \n    Sources: 0x00001002\n'
          '    Motion Ranges:\n      X: source=0x00001002\n'
          '\nInput Dispatcher State:\n')


def native_fixture(root='0', profile=PROFILE, foreign=False, fail_write=False, reader=READER):
    calls = []
    def command(*args, **options):
        calls.append((args, options))
        if args[0] == 'ps': return '1'
        if args[0] == 'xdotool':
            if args[1] == 'search': return '42'
            if args[1] == 'getwindowpid': return '999' if foreign else '101'
            if args[1] == 'getwindowgeometry': return 'WIDTH=288\nHEIGHT=640\n'
            return ''
        if args[-2:] == ('id', '-u'): return root
        if args[-2:] == ('getevent', '-lp'): return profile
        if args[-4:] == ('dumpsys', '-t', '5', 'input'): return reader
        if options.get('data') and fail_write and sum(bool(v.get('data')) for _, v in calls) == 1:
            raise RuntimeError('Fictional input interruption')
        return ''
    return SimpleNamespace(ENV={'DISPLAY': ':91', 'MYCHEF_ANALYTICS_EMULATOR_PID': '101',
                                'ANDROID_HOME': '/fictional-sdk'}, command=command), calls


class DisplayFocusTest(unittest.TestCase):
    def test_startup_focus_uses_only_the_owned_window_without_input(self):
        native, calls = native_fixture()
        with patch.object(touch.time, 'sleep'):
            self.assertEqual(touch.focus_display(native), ('42', 288, 640))
        self.assertFalse(any('mousedown' in args or options.get('data') for args, options in calls))
        native, _ = native_fixture(foreign=True)
        with self.assertRaises(RuntimeError): touch.focus_display(native)
        native, _ = native_fixture(); native.ENV['DISPLAY'] = ':0'
        with self.assertRaises(RuntimeError): touch.focus_display(native)


    def test_missing_ambiguous_or_unbounded_owned_display_stays_refused(self):
        for search, geometry in (('', 'WIDTH=288\nHEIGHT=640\n'),
                                 ('42 43', 'WIDTH=288\nHEIGHT=640\n'),
                                 ('42', 'WIDTH=1\nHEIGHT=2\n'), ('42;unsafe', 'WIDTH=288\nHEIGHT=640\n')):
            native, _ = native_fixture()
            original = native.command
            def command(*args, **options):
                if args[0] == 'xdotool' and args[1] == 'search': return search
                if args[0] == 'xdotool' and args[1] == 'getwindowgeometry': return geometry
                return original(*args, **options)
            native.command = command
            with self.subTest(search=search), self.assertRaises(RuntimeError): touch.focus_display(native)


class KernelGestureTest(unittest.TestCase):
    def test_reader_failure_diagnostics_never_expose_raw_input_history(self):
        raw = 'private fixture history must never be printed'
        output = io.StringIO()
        with redirect_stdout(output), self.assertRaises(ValueError):
            touch.reader_touch_devices(raw, diagnostics=True)
        self.assertNotIn(raw, output.getvalue())
        self.assertRegex(output.getvalue(), r'^Analytics input reader: bytes=[0-9]+ hubHeaders=0 readerHeaders=0 stage=[0-9]+ hubs=0 readers=0 enabled=0 touch=0 mapped=0\n$')

    def test_configured_touchscreen_requires_explicit_reader_to_hub_mapping(self):
        self.assertEqual(touch.reader_touch_devices(READER), {'/dev/input/event7'})
        no_property = PROFILE.replace('INPUT_PROP_DIRECT', '')
        self.assertEqual(touch.select_touch_device(no_property, reader_paths=touch.reader_touch_devices(READER))['path'],
                         '/dev/input/event7')
        # A raw direct property cannot override the configured reader identity.
        with self.assertRaises(ValueError):
            touch.select_touch_device(PROFILE, reader_paths={'/dev/input/event8'})

    def test_patched_android_source_names_keep_the_same_touchscreen_identity_gate(self):
        keyboard = '  Device 1: fictional keyboard\n    Sources: KEYBOARD | DPAD\n'
        for source in ('TOUCHSCREEN', 'TOUCHSCREEN | STYLUS', 'KEYBOARD | TOUCHSCREEN'):
            reader = READER.replace('  Device 27:', keyboard + '  Device 27:').replace(
                'Sources: 0x00001002', 'Sources: ' + source)
            self.assertEqual(touch.reader_touch_devices(reader), {'/dev/input/event7'})
        for source in ('MOUSE', 'TOUCHPAD', 'UNKNOWN', 'ANY', '0xffffffff',
                       'UNKNOWN | TOUCHSCREEN', 'TOUCHSCREEN | invented', 'TOUCHSCREEN | TOUCHSCREEN'):
            reader = READER.replace('Sources: 0x00001002', 'Sources: ' + source)
            with self.subTest(source=source), self.assertRaises(ValueError):
                touch.reader_touch_devices(reader)

    def test_builtin_alias_requires_both_the_hub_record_and_its_explicit_alias_label(self):
        alias = READER.replace('  Devices:\n', '  BuiltInKeyboardId: 12\n  Devices:\n').replace(
            '12: fictional touchscreen\n', '12: fictional touchscreen (aka device 0 - built-in keyboard)\n').replace(
            'EventHub Devices: [ 12 ]', 'EventHub Devices: [ 0 ]')
        self.assertEqual(touch.reader_touch_devices(alias), {'/dev/input/event7'})
        for value in (alias.replace(' (aka device 0 - built-in keyboard)', ''),
                      alias.replace('BuiltInKeyboardId: 12', 'BuiltInKeyboardId: 13')):
            with self.assertRaises(ValueError): touch.reader_touch_devices(value)

    def test_disabled_reader_is_excluded_without_hiding_another_enabled_reader(self):
        disabled = READER.replace('    12: fictional touchscreen', '    13: disabled touchscreen\n'
            '      Path: /dev/input/event8\n      Enabled: false\n    12: fictional touchscreen')
        disabled = disabled.replace('  Device 27:', '  Device 26: disabled touchscreen\n'
            '    EventHub Devices: [ 13 ]\n    Sources: TOUCHSCREEN\n  Device 27:')
        self.assertEqual(touch.reader_touch_devices(disabled), {'/dev/input/event7'})

    def test_missing_ambiguous_or_non_touch_reader_mapping_is_refused(self):
        for reader in (None, '', 'x' * 524289,
                       READER.replace('Sources: 0x00001002', 'Sources: 0x00002002'),
                       READER.replace('    Sources: 0x00001002\n', ''),
                       READER.replace('EventHub Devices: [ 12 ]', 'EventHub Devices: [ 27 ]'),
                       READER.replace('/dev/input/event7', '/private/unsafe'),
                       READER.replace('      Enabled: true', '      Enabled: false'),
                       READER.replace('    EventHub Devices: [ 12 ] \n', ''),
                       READER.replace('  Devices:\n', '  Devices:\n    12: duplicate\n      Path: /dev/input/event8\n'),
                       READER.replace('    Sources: 0x00001002', '    Sources: malformed')):
            with self.subTest(reader=type(reader).__name__), self.assertRaises(ValueError):
                touch.reader_touch_devices(reader)

    def test_only_one_measured_direct_slot_touchscreen_can_be_selected(self):
        device = touch.select_touch_device(PROFILE)
        self.assertEqual(device['path'], '/dev/input/event7')
        self.assertEqual(device['x'], (0, 32767))
        self.assertTrue(device['button'])
        for profile in (None, '', 'x' * 65537, PROFILE.replace('INPUT_PROP_DIRECT', ''),
                        PROFILE.replace('ABS_MT_SLOT', 'ABS_OTHER'),
                        PROFILE.replace('/dev/input/event7', '/private/not-input'),
                        PROFILE + PROFILE.replace('/dev/input/event7', '/dev/input/event8'),
                        PROFILE.replace('max 32767', 'max 0')):
            with self.subTest(profile=type(profile).__name__), self.assertRaises(ValueError):
                touch.select_touch_device(profile)

    def test_closed_plans_stay_in_the_measured_axes_and_release_every_contact(self):
        device = touch.select_touch_device(PROFILE)
        for kind, contacts in (('double-tap', 2), ('next', 1)):
            frames = touch.kernel_frames(kind, device)
            events = [event for _, frame in frames for event in frame]
            self.assertEqual(sum(t == 3 and c == 57 and v >= 0 for t, c, v in events), contacts)
            self.assertEqual(events.count((3, 57, -1)), contacts)
            self.assertTrue(all(frame[-1] == (0, 0, 0) for _, frame in frames))
            self.assertTrue(all(0 <= v <= 32767 for t, c, v in events if t == 3 and c in (53, 54)))
            self.assertTrue(all(0 <= delay <= .2 for delay, _ in frames))
        release = [event for _, frame in touch.kernel_frames('release', device) for event in frame]
        self.assertFalse(any(t == 3 and c == 57 and v >= 0 for t, c, v in release))
        for kind in ('click:1:2', '', None):
            with self.assertRaises(ValueError): touch.kernel_frames(kind, device)
        with self.assertRaises(ValueError):
            touch.kernel_frames('next', {**device, 'path': '/dev/input/event7;unsafe'})

    def test_writer_is_scoped_observed_and_never_retries_activation(self):
        for failed in (False, True):
            native, calls = native_fixture(fail_write=failed)
            observing = [False]
            original = native.command
            def command(*args, **options):
                if options.get('data'):
                    self.assertTrue(observing[0])
                    self.assertEqual(args[:3], ('/fictional-sdk/platform-tools/adb', '-s', 'emulator-5554'))
                return original(*args, **options)
            native.command = command
            @contextmanager
            def observer(_, contacts, device=None):
                self.assertEqual(contacts, 2)
                self.assertEqual(device, '/dev/input/event7')
                observing[0] = True
                try: yield
                finally: observing[0] = False
            with self.subTest(failed=failed), patch.object(touch, 'touch_trace', side_effect=observer), \
                 patch.object(touch.time, 'sleep'):
                if failed:
                    with self.assertRaisesRegex(RuntimeError, 'touchscreen injection'):
                        touch.gesture('double-tap', native)
                else:
                    self.assertEqual(touch.gesture('double-tap', native), {'passed': True})
            writes = [options['data'] for _, options in calls if options.get('data')]
            self.assertEqual(len(writes), 2, 'One gesture plus release cleanup only')
            self.assertNotIn(' 3 57 0', writes[-1]); self.assertNotIn(' 3 57 1', writes[-1])
            self.assertFalse(any('mousedown' in args for args, _ in calls))

    def test_unrooted_foreign_or_unmeasured_device_refuses_before_writing(self):
        for options in ({'root': '2000'}, {'foreign': True}, {'profile': ''}, {'reader': ''}):
            native, calls = native_fixture(**options)
            with patch.object(touch.time, 'sleep'), self.assertRaises((ValueError, RuntimeError)):
                touch.gesture('next', native)
            self.assertFalse(any(fields.get('data') for _, fields in calls))


class TouchTraceTest(unittest.TestCase):
    def test_double_tap_requires_two_complete_contacts(self):
        from analytics_touch import touch_facts
        contact = "EV_ABS ABS_MT_POSITION_X 00004000\nEV_ABS ABS_MT_POSITION_Y 00004000\nEV_ABS ABS_MT_TRACKING_ID 00000003\nEV_ABS ABS_MT_TRACKING_ID ffffffff\n"
        self.assertFalse(touch_facts(contact, contacts=2)['touchscreen'])
        self.assertTrue(touch_facts(contact + contact, contacts=2)['touchscreen'])
        # Linux omits unchanged ABS coordinates on a later contact.
        same_y = contact.replace('EV_ABS ABS_MT_POSITION_Y 00004000\n', '')
        self.assertTrue(touch_facts(contact + same_y, contacts=2)['touchscreen'])

    def test_foreign_touch_device_cannot_supply_the_selected_writers_contacts(self):
        contact = "EV_ABS ABS_MT_POSITION_X 00004000\nEV_ABS ABS_MT_POSITION_Y 00004000\nEV_ABS ABS_MT_TRACKING_ID 00000003\nEV_ABS ABS_MT_TRACKING_ID ffffffff\n"
        own = ''.join('[ 1.000000] /dev/input/event7: ' + line + '\n' for line in contact.splitlines())
        foreign = own.replace('/dev/input/event7:', '/dev/input/event8:')
        self.assertTrue(touch.touch_facts(own, device='/dev/input/event7')['touchscreen'])
        self.assertFalse(touch.touch_facts(foreign, device='/dev/input/event7')['touchscreen'])
        with self.assertRaises(ValueError): touch.touch_facts(own, device='/private/not-input')

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

    def test_writer_path_must_be_in_the_second_measured_snapshot(self):
        native = SimpleNamespace(ENV={'ANDROID_HOME': '/fictional-sdk'}, command=lambda *a, **k: self.profile)
        with patch.object(touch.subprocess, 'Popen') as process:
            with self.assertRaisesRegex(RuntimeError, 'observer unavailable'):
                with touch.touch_trace(native, 1, device='/dev/input/event8'): pass
            process.assert_not_called()

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
