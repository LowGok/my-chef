"""Local development must not select production storage or a paid provider implicitly."""
import importlib.util
from pathlib import Path
import tempfile
import unittest
import sys

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
spec = importlib.util.spec_from_file_location("development", ROOT / "scripts/dev_server.py")
dev = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dev)


class DevelopmentTest(unittest.TestCase):
    def test_fixture_selects_only_its_disposable_storage(self):
        with tempfile.TemporaryDirectory() as directory:
            env = dev.configure({"MYCHEF_DB": "/private/production.db", "MYCHEF_CREDENTIALS": "/private/keys.json"}, fixture=directory)
            self.assertEqual(env["MYCHEF_DB"], str(Path(directory) / "mychef.db"))
            self.assertEqual(env["MYCHEF_PROVIDER"], "fixture")
            self.assertEqual(env["MYCHEF_E2E"], "1")
            self.assertNotIn("MYCHEF_CREDENTIALS", env)

    def test_real_preview_requires_explicit_credentials(self):
        with self.assertRaisesRegex(ValueError, "MYCHEF_CREDENTIALS"):
            dev.configure({})

    def test_real_preview_preserves_explicit_origin_and_database(self):
        env = dev.configure({"MYCHEF_CREDENTIALS": "/private/provider.json", "MYCHEF_DB": "data/dev.db", "MYCHEF_ORIGIN": "https://dev.example.test", "MYCHEF_E2E": "1"})
        self.assertEqual(env["MYCHEF_ORIGIN"], "https://dev.example.test")
        self.assertEqual(env["MYCHEF_DB"], "data/dev.db")
        self.assertEqual(env["MYCHEF_PROVIDER"], "gemini")
        self.assertNotIn("MYCHEF_E2E", env)


if __name__ == "__main__":
    unittest.main()
