"""Project synthetic API-created data into the retained version-three schema."""
import hashlib
from pathlib import Path
import sqlite3


def legacy_snapshot(database, destination):
    migrations = Path(__file__).resolve().parents[1] / "migrations"
    schema = "".join((migrations / name).read_text() for name in
                     ("001_initial.sql", "002_shopping.sql", "003_favorites.sql"))
    with sqlite3.connect(database) as source, sqlite3.connect(destination) as target:
        target.executescript(schema)
        tables = [row[0] for row in target.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")]
        for table in tables:
            columns = [row[1] for row in target.execute(f'PRAGMA table_info("{table}")')]
            selected = ",".join('"' + column + '"' for column in columns)
            rows = source.execute(f'SELECT {selected} FROM "{table}"').fetchall()
            placeholders = ",".join("?" for _ in columns)
            target.executemany(f'INSERT INTO "{table}" ({selected}) VALUES ({placeholders})', rows)
        target.execute("CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,checksum TEXT NOT NULL,dirty BOOLEAN NOT NULL)")
        target.execute("INSERT INTO schema_migrations VALUES(3,?,0)", (hashlib.sha256(schema.encode()).hexdigest(),))
        assert target.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
        assert target.execute("PRAGMA foreign_key_check").fetchall() == []
