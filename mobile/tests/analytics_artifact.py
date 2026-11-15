"""Enforce the owner's simulator-only artifact boundary before CI transfer."""
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import plistlib
import re
import stat
import zipfile

ROOT = Path(__file__).resolve().parents[2]
IDENTITY = 'MYCHEF0000.com.gkldevelopment.mychef.preview'
ENTITLEMENTS = {'application-identifier': IDENTITY, 'keychain-access-groups': [IDENTITY]}


def verify_archive(raw):
    if not 0 < len(raw) <= 64 * 1024 * 1024:
        return False
    try:
        with zipfile.ZipFile(io.BytesIO(raw)) as archive:
            files = archive.infolist()
            if len(files) > 20000 or sum(item.file_size for item in files) > 256 * 1024 * 1024:
                return False
            seen = set()
            for item in files:
                path = PurePosixPath(item.filename)
                if (item.filename in seen or path.is_absolute() or '..' in path.parts or '\\' in item.filename
                        or not path.parts or path.parts[0] not in ('App.app', '__MACOSX')
                        or item.flag_bits & 1):
                    return False
                if path.parts[0] == '__MACOSX' and len(path.parts) > 1 and path.parts[1] not in ('App.app', '._App.app'):
                    return False
                seen.add(item.filename)
                if item.is_dir():
                    continue
                mode = item.external_attr >> 16
                if stat.S_IFMT(mode) not in (0, stat.S_IFREG):
                    return False
                if (path.suffix.lower() in ('.p12', '.pfx', '.key', '.pem', '.mobileprovision', '.db', '.sqlite', '.sqlite3', '.keychain', '.keychain-db')
                        or '.env' == path.name or path.name.startswith('.env.')):
                    return False
                data = archive.read(item)
                if data.startswith(b'SQLite format 3\0') or re.search(rb'-----BEGIN [A-Z ]*PRIVATE KEY-----', data):
                    return False
            info = plistlib.loads(archive.read('App.app/Info.plist'))
            return (info.get('CFBundleIdentifier') == 'com.gkldevelopment.mychef.preview'
                    and info.get('CFBundleSupportedPlatforms') == ['iPhoneSimulator']
                    and 'App.app/App' in seen)
    except (ValueError, KeyError, OSError, zipfile.BadZipFile, RuntimeError):
        return False


def main():
    generated = ROOT / 'mobile/.generated'
    plist = generated / 'simulator-keychain.plist'
    receipt_path = generated / 'artifacts/analytics-compilation.json'
    archive = generated / 'artifacts/analytics-simulator.app.zip'
    if (plist.stat().st_size > 4096 or receipt_path.stat().st_size > 65536
            or archive.stat().st_size > 64 * 1024 * 1024):
        raise SystemExit('Simulator transfer input exceeds bound')
    receipt = json.loads(receipt_path.read_text())
    raw = archive.read_bytes()
    if (plistlib.loads(plist.read_bytes()) != ENTITLEMENTS or not verify_archive(raw)
            or receipt.get('platform') != 'ios-simulator'
            or receipt.get('artifact') != archive.name
            or receipt['candidate']['sourceCommit'] != os.environ.get('GITHUB_SHA')
            or hashlib.sha256(raw).hexdigest() != receipt.get('artifactSha256')):
        raise SystemExit('Simulator artifact transfer boundary refused')
    print('Simulator-only archive and fictional entitlements passed transfer checks')


if __name__ == '__main__':
    main()
