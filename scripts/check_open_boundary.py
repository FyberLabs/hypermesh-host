"""Reject provider-specific symbols on the declared open runtime surface.

Pass one or more runtime/package paths explicitly; missing inputs fail closed.
Scan code, schemas, templates and dependency metadata, not prose or test evidence.
The caller must enumerate its complete shipped surface (including generated SDKs).
"""
from __future__ import annotations

import argparse
from pathlib import Path
import re

PROVIDER = re.compile(r'stripe', re.IGNORECASE)
EXTENSIONS = {'.py','.pyi','.go','.rs','.js','.jsx','.ts','.tsx','.proto','.sql',
              '.json','.yaml','.yml','.toml','.jinja','.j2','.h','.hpp','.c','.cpp',
              '.kt','.kts','.java','.swift','.cs','.mod','.sum',
              '.html','.css','.sh','.service','.plist'}


def violations(paths):
    found = []
    for root in paths:
        if not root.exists():
            raise ValueError(f'missing open surface: {root}')
        files = [root] if root.is_file() else sorted(root.rglob('*'))
        for path in files:
            if not path.is_file():
                continue
            if path.suffix not in EXTENSIONS:
                # Installed executables such as deploy/lib/update-rollback have no suffix.
                if path.suffix or not path.read_bytes().startswith(b'#!'):
                    continue
            for line, value in enumerate(path.read_text(encoding='utf-8').splitlines(), 1):
                if PROVIDER.search(value):
                    found.append(f'{path}:{line}: provider-specific symbol in open surface')
    return found


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('paths', nargs='+', type=Path)
    args = parser.parse_args()
    try:
        failures = violations(args.paths)
    except (ValueError, OSError, UnicodeError) as exc:
        parser.exit(2, str(exc) + '\n')
    if failures:
        parser.exit(1, '\n'.join(failures) + '\n')


if __name__ == '__main__':
    main()
