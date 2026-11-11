"""The full browser suite must not share account rate limits between specs."""
import importlib.util
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("browser_runner", ROOT / "scripts/rewrite.py")
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


class BrowserRunnerTest(unittest.TestCase):
    def invoke(self, root, arguments=(), effect=None):
        with patch.object(runner, "ROOT", root), \
             patch.object(runner, "environment", return_value={}), \
             patch.object(sys, "argv", ["rewrite.py", "e2e", *arguments]), \
             patch.object(runner.os, "umask"), \
             patch.object(runner.subprocess, "run", side_effect=effect) as run:
            runner.main()
            return [list(call.args[0]) for call in run.call_args_list]

    def test_full_suite_starts_one_fixture_lifecycle_per_spec(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tests = root / "web/tests"
            tests.mkdir(parents=True)
            for name in ("two.spec.ts", "one.spec.ts", "notes.ts"):
                (tests / name).touch()
            self.assertEqual(self.invoke(root), [
                ["bun", "run", "e2e", "one.spec.ts"],
                ["bun", "run", "e2e", "two.spec.ts"],
            ])

    def test_explicit_browser_arguments_are_preserved(self):
        self.assertEqual(self.invoke(ROOT, ["feedback.spec.ts", "--grep", "receipt"]), [
            ["bun", "run", "e2e", "feedback.spec.ts", "--grep", "receipt"],
        ])

    def test_empty_suite_cannot_report_success(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(SystemExit, "No browser specs"):
                self.invoke(Path(directory))

    def test_a_failed_spec_stops_the_suite(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            tests = root / "web/tests"
            tests.mkdir(parents=True)
            (tests / "one.spec.ts").touch()
            (tests / "two.spec.ts").touch()
            with self.assertRaises(subprocess.CalledProcessError):
                self.invoke(root, effect=subprocess.CalledProcessError(1, "fixture"))


if __name__ == "__main__":
    unittest.main()
