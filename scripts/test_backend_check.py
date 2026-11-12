"""Isolation checks for the stage-1 snapshot helper; no real checkout/index edits."""
import contextlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from scripts import backend_check as check


class SnapshotIsolation(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.baseline = {
            'internal/secretstore/secretstore.go': b'frozen cipher',
            'internal/adminauth/adminauth.go': b'frozen guard',
            f'{check.STORAGE}/database.go': b'frozen database',
            f'{check.STORAGE}/migrations/001_initial.sql': b'SELECT 1;',
            f'{check.STORAGE}/migrations/002_foundation.sql': b'SELECT 1;',
            'controller/internal/auth/auth.go': b'baseline sibling',
            'controller/internal/enrollment/deleted.go': b'baseline deleted',
            'controller/web/package.json': b'baseline frontend',
        }

    def put(self, rel, content):
        path = self.root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content)

    def prepare(self):
        archive = io.BytesIO()
        with tarfile.open(fileobj=archive, mode='w') as tar:
            for name, content in self.baseline.items():
                info = tarfile.TarInfo(name)
                info.size = len(content)
                tar.addfile(info, io.BytesIO(content))
        with patch.object(check, 'ROOT', self.root), patch.object(
            check.subprocess, 'check_output', side_effect=['a' * 40 + '\n', archive.getvalue()]
        ), contextlib.redirect_stdout(io.StringIO()):
            return check.prepare('B02', 'explicit-I00-commit')

    def test_only_owned_overlay_and_proposal(self):
        self.put('controller/internal/enrollment/new.go', 'own module')
        self.put('controller/internal/auth/auth.go', 'uncommitted sibling')
        self.put('internal/secretstore/secretstore.go', 'uncommitted shared change')
        self.put('.local/controller/controller.db', 'must never copy database')
        self.put('controller/web/node_modules/private.txt', 'must never copy dependencies')
        self.put('controller/internal/enrollment/node_modules/private.json', 'must never copy nested dependencies')
        self.put(f'{check.STORAGE}/proposals/B01.sql', 'must never copy sibling SQL')
        self.put(f'{check.STORAGE}/proposals/B02.sql', 'SELECT 2;')
        self.put(f'{check.STORAGE}/queries/b02_test.sql', '-- own query')
        self.put(f'{check.STORAGE}/b02_repository_test.go', '// own test')
        self.put('documents/backend-delivery/B02.deps', 'example.invalid/dependency@v1.2.3\n')
        target, deps = self.prepare()
        self.assertEqual((target / 'controller/internal/auth/auth.go').read_bytes(), b'baseline sibling')
        self.assertEqual((target / 'internal/secretstore/secretstore.go').read_bytes(), b'frozen cipher')
        self.assertEqual((target / 'controller/internal/enrollment/new.go').read_text(), 'own module')
        self.assertFalse((target / 'controller/internal/enrollment/deleted.go').exists())
        self.assertFalse((target / '.local').exists())
        self.assertFalse((target / 'controller/web/node_modules').exists())
        self.assertFalse((target / 'controller/internal/enrollment/node_modules').exists())
        self.assertFalse((target / check.STORAGE / 'proposals/B01.sql').exists())
        self.assertEqual((target / check.STORAGE / 'migrations/004_b02.sql').read_text(), 'SELECT 2;')
        self.assertIn('SELECT 1', (target / check.STORAGE / 'migrations/003_scratch_placeholder.sql').read_text())
        self.assertTrue((target / check.STORAGE / 'b02_repository_test.go').exists())
        self.assertEqual(deps, ['example.invalid/dependency@v1.2.3'])
        manifest = json.loads((target / 'VALIDATION.json').read_text())
        self.assertEqual(manifest['base'], 'a' * 40)
        self.assertNotIn('controller/internal/auth/auth.go', manifest['overlay'])

    def test_refuses_wrong_baseline(self):
        del self.baseline['internal/adminauth/adminauth.go']
        with self.assertRaisesRegex(SystemExit, 'frozen I00'):
            self.prepare()

    def test_refuses_symlinks_and_non_source(self):
        self.put('controller/internal/enrollment/real.key', 'private file')
        with self.assertRaisesRegex(SystemExit, 'non-source'):
            self.prepare()
        (self.root / 'controller/internal/enrollment/real.key').unlink()
        (self.root / 'controller/internal/enrollment/link.go').symlink_to('/tmp')
        with self.assertRaisesRegex(SystemExit, 'symlink'):
            self.prepare()

    def test_refuses_unpinned_dependency(self):
        self.put('documents/backend-delivery/B02.deps', 'example.invalid/dependency@latest')
        with self.assertRaisesRegex(SystemExit, 'exact module'):
            self.prepare()


if __name__ == '__main__':
    unittest.main()
