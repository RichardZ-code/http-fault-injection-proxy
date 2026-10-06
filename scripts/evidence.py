#!/usr/bin/env python3
"""Verify the published textual P09 subset, never promote it to formal data."""
import hashlib
import json
from pathlib import Path
import re
import sys


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def verify(root):
    root = Path(root)
    entries = {}
    for line in (root / 'SHA256SUMS').read_text().splitlines():
        match = re.fullmatch(r'([a-f0-9]{64})  (.+)', line)
        if not match:
            raise ValueError('invalid published checksum line')
        hash_value, name = match.groups()
        path = Path(name)
        if path.is_absolute() or '..' in path.parts or name in entries or name == 'SHA256SUMS':
            raise ValueError('unsafe or duplicate checksum path')
        if (root / path).is_symlink() or digest(root / path) != hash_value:
            raise ValueError('published checksum mismatch: ' + name)
        entries[name] = hash_value
    actual = {str(p.relative_to(root)) for p in root.rglob('*') if p.is_file()}
    if actual != set(entries) | {'SHA256SUMS'}:
        raise ValueError('published file inventory mismatch')
    receipt = json.loads((root / 'IMPORT.json').read_text())
    if len(receipt['files']) != 167 or len(actual) != 170 or len(receipt['excluded']) != 15:
        raise ValueError('approved inventory count mismatch')
    imported = set()
    for item in receipt['files']:
        name = item['published']
        if name in imported or entries.get(name) != item['published_sha256']:
            raise ValueError('import receipt mismatch')
        imported.add(name)
        if item['treatment'] == 'copy unchanged' and item['original_sha256'] != item['published_sha256']:
            raise ValueError('raw evidence was transformed')
    if actual != imported | {'README.md', 'IMPORT.json', 'SHA256SUMS'}:
        raise ValueError('unexpected generated file')
    manifest = json.loads((root / 'dataset/manifest.json').read_text())
    if manifest['source']['sha'] != receipt['measured_sha'] or not manifest['valid']:
        raise ValueError('measured source or historical validity mismatch')
    for name in ('proxy', 'upstream', 'client'):
        if (root / 'dataset' / name).exists():
            raise ValueError('binary imported into textual subset')
        if manifest['files'][name] != manifest['builds'][name]['sha256']:
            raise ValueError('original binary hashes lost')
    for name, expected in manifest['files'].items():
        if name not in ('proxy', 'upstream', 'client'):
            path = Path(name)
            if path.is_absolute() or '..' in path.parts or digest(root / 'dataset' / path) != expected:
                raise ValueError('original dataset checksum mismatch: ' + name)
    for name in actual:
        content = (root / name).read_text()
        if re.search(r'/Users/|/private/tmp/|/var/folders/|-----BEGIN .*PRIVATE KEY|gh[pousr]_[A-Za-z0-9]{20}', content):
            raise ValueError('private path or secret-like metadata: ' + name)
    return len(actual)


if __name__ == '__main__':
    print('PASS: published textual inventory/checksums/privacy:', verify(sys.argv[1]), 'files; formal restoration still required')
