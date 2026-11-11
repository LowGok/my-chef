"""Credential selection for fixture and explicitly requested live rehearsals."""
import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
spec = importlib.util.spec_from_file_location("menu_rehearsal", ROOT / "scripts/test_rewrite_menu.py")
menu = importlib.util.module_from_spec(spec)
spec.loader.exec_module(menu)


class CredentialsTest(unittest.TestCase):
    def test_fixture_never_reads_or_passes_provider_credentials(self):
        env = {"MYCHEF_CREDENTIALS": "/private/do-not-read.json", "GEMINI_API_KEY": "synthetic"}
        with patch.object(Path, "read_text", side_effect=AssertionError("fixture read credentials")):
            menu.configure_provider(env, live=False)
        self.assertEqual(env["MYCHEF_PROVIDER"], "fixture")
        self.assertEqual(env["MYCHEF_E2E"], "1")
        self.assertNotIn("MYCHEF_CREDENTIALS", env)
        self.assertNotIn("GEMINI_API_KEY", env)

    def test_live_uses_original_json_without_copying_or_mutating_it(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "provider.json"
            content = json.dumps({"gemini_api_key": "synthetic-key", "gemini_recipe_model": "synthetic-model"})
            path.write_text(content)
            env = {"MYCHEF_CREDENTIALS": str(path), "MYCHEF_E2E": "1"}
            menu.configure_provider(env, live=True)
            self.assertEqual(env["MYCHEF_CREDENTIALS"], str(path.resolve()))
            self.assertEqual(env["MYCHEF_PROVIDER"], "gemini")
            self.assertNotIn("MYCHEF_E2E", env)
            self.assertEqual(path.read_text(), content)
            self.assertEqual(list(Path(directory).iterdir()), [path])

    def test_live_rejects_missing_or_invalid_json_without_exposing_contents(self):
        with self.assertRaisesRegex(ValueError, "MYCHEF_CREDENTIALS"):
            menu.configure_provider({}, live=True)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "provider.json"
            for value in ["synthetic-secret-not-json", "[]", "{}", '{"gemini_api_key": 42}']:
                path.write_text(value)
                with self.assertRaises(ValueError) as result:
                    menu.configure_provider({"MYCHEF_CREDENTIALS": str(path)}, live=True)
                self.assertNotIn(value, str(result.exception))


if __name__ == "__main__":
    unittest.main()
