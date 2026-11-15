"""Offline fixture/controller boundaries; no device or CI authority is asserted."""
import io
from pathlib import Path
import plistlib
import tempfile
import unittest
import zipfile

from analytics_fixture import own_export
import analytics_ios


class FixtureBoundaryTest(unittest.TestCase):
    def archive(self, **files):
        buffer = io.BytesIO()
        with zipfile.ZipFile(buffer, 'w') as archive:
            for name, value in files.items():
                archive.writestr(name, value)
        return buffer.getvalue()

    def test_native_saved_export_requires_owned_data_and_rejects_foreign_accounts(self):
        own = 'analyticsinstalled@example.test'
        self.assertTrue(own_export(self.archive(**{'account.json': own, 'analytics.json': '{}'}), own))
        self.assertFalse(own_export(b'not a ZIP', own))
        self.assertFalse(own_export(self.archive(**{'account.json': own}), own))
        self.assertFalse(own_export(self.archive(**{'account.json': 'foreign@example.test', 'analytics.json': '{}'}), own))
        for other in ('analyticsdisabled@example.test', 'analyticsassessed@example.test'):
            self.assertFalse(own_export(self.archive(**{'account.json': own, 'analytics.json': other}), own))

    def test_generated_probe_has_only_a_separate_unsigned_ui_target(self):
        with tempfile.TemporaryDirectory(prefix='analytics-probe-') as folder:
            project = analytics_ios.project(Path(folder))
            value = plistlib.loads((project / 'project.pbxproj').read_bytes())
            targets = [item for item in value['objects'].values() if item.get('isa') == 'PBXNativeTarget']
            self.assertEqual(len(targets), 1)
            self.assertEqual(targets[0]['productType'], 'com.apple.product-type.bundle.ui-testing')
            self.assertEqual(targets[0]['dependencies'], [])
            settings = [item['buildSettings'] for item in value['objects'].values() if item.get('isa') == 'XCBuildConfiguration']
            self.assertTrue(all(item['CODE_SIGNING_ALLOWED'] == 'NO' for item in settings))
            self.assertFalse(list(Path(folder).rglob('App.app')))


if __name__ == '__main__':
    unittest.main()
