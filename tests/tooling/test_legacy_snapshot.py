"""A synthetic upgrade baseline must be versioned and omit later identity fields."""
import hashlib
from pathlib import Path
import sqlite3
import sys

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
from legacy_snapshot import legacy_snapshot


def test_legacy_projection_preserves_values_and_version_checksum(tmp_path):
    original, legacy = tmp_path / "original.db", tmp_path / "legacy.db"
    schema = "".join((ROOT / "migrations" / name).read_text() for name in
                     ("001_initial.sql", "002_shopping.sql", "003_favorites.sql"))
    with sqlite3.connect(original) as source:
        source.executescript(schema)
        source.execute("ALTER TABLE users ADD COLUMN email_verified_at INTEGER")
        source.execute("INSERT INTO users(firstname,lastname,email,password,username,email_verified_at) VALUES(?,?,?,?,?,?)",
                       ("Fixture", "User", "fixture@example.test", "synthetic-hash", "fixture", 123))
    legacy_snapshot(original, legacy)
    with sqlite3.connect(legacy) as result:
        assert result.execute("SELECT version,checksum,dirty FROM schema_migrations").fetchone() == (3, hashlib.sha256(schema.encode()).hexdigest(), 0)
        assert result.execute("SELECT email,password FROM users").fetchone() == ("fixture@example.test", "synthetic-hash")
        assert "email_verified_at" not in {row[1] for row in result.execute("PRAGMA table_info(users)")}
    with sqlite3.connect(original) as source:
        assert source.execute("SELECT email_verified_at FROM users").fetchone()[0] == 123
