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

    def prepare(self, module="B02", invoke_main=False, prepare_only=False):
        archive = io.BytesIO()
        with tarfile.open(fileobj=archive, mode='w') as tar:
            for name, content in self.baseline.items():
                info = tarfile.TarInfo(name)
                info.size = len(content)
                tar.addfile(info, io.BytesIO(content))
        with patch.object(check, 'ROOT', self.root), patch.object(
            check.subprocess, 'check_output', side_effect=['a' * 40 + '\n', archive.getvalue()]
        ), contextlib.redirect_stdout(io.StringIO()):
            if not invoke_main:
                return check.prepare(module, 'a' * 40)
            with patch('sys.argv', ['backend_check.py', module, '--base', 'a' * 40] + (['--prepare-only'] if prepare_only else [])):
                check.main()

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


class Stage2Isolation(SnapshotIsolation):
    def setUp(self):
        super().setUp()
        repo = Path(check.__file__).resolve().parents[1]
        for name in check.STAGE2_MIGRATIONS:
            self.baseline[f'{check.STORAGE}/migrations/{name}'] = (repo / check.STORAGE / 'migrations' / name).read_bytes()
        for rel in ['internal/control/bundle.go', 'internal/control/state.go']:
            self.baseline[rel] = (repo / rel).read_bytes()
        self.baseline['agent/internal/runtime/deleted.go'] = b'package runtime'
        self.baseline['controller/internal/releases/release.go'] = b'committed sibling'
        self.baseline[f'{check.STORAGE}/queries/b05_deleted.sql'] = b'old query'
        self.baseline['controller/internal/webui/dist/full.js'] = b'no build assets'
        self.baseline['controller/internal/webui/dist/.gitkeep'] = b''
        self.put(f'{check.STORAGE}/proposals/B04.sql', '-- fixture B04\nSELECT 6;')
        self.put(f'{check.STORAGE}/proposals/B05.sql', '-- no Controller state: fixture B05\nSELECT 7;')

    # Stage-1-specific inherited scenarios use the original fixture in that class.
    def prepare(self, module='B05', **kwargs):
        return super().prepare(module, **kwargs)

    def test_only_owned_overlay_and_proposal(self):
        self.put('agent/internal/runtime/runtime.go', 'package runtime\nfunc Private() {}')
        self.put('controller/internal/releases/release.go', 'active sibling MUST NOT copy')
        self.put('internal/control/bundle.go', 'active shared MUST NOT copy')
        self.put(f'{check.STORAGE}/queries/b05_fixture.sql', '-- own query')
        target, _ = self.prepare()
        self.assertEqual((target / 'controller/internal/releases/release.go').read_bytes(), b'committed sibling')
        self.assertEqual((target / 'internal/control/bundle.go').read_bytes(), self.baseline['internal/control/bundle.go'])
        self.assertFalse((target / 'agent/internal/runtime/deleted.go').exists())
        self.assertFalse((target / check.STORAGE / 'queries/b05_deleted.sql').exists())
        self.assertFalse((target / 'controller/web/package.json').exists())
        self.assertFalse((target / 'controller/internal/webui/dist/full.js').exists())
        for name, digest in check.STAGE2_MIGRATIONS.items():
            self.assertEqual(check.hashlib.sha256((target / check.STORAGE / 'migrations' / name).read_bytes()).hexdigest(), digest)
        self.assertIn('SELECT 1;', (target / check.STORAGE / 'migrations/006_scratch_placeholder.sql').read_text())
        self.assertNotIn('SELECT 6;', (target / check.STORAGE / 'migrations/006_scratch_placeholder.sql').read_text())
        manifest = json.loads((target / 'VALIDATION.json').read_text())
        self.assertEqual(manifest['scratch_migrations'], ['006'])
        self.assertEqual(manifest['status'], 'prepared_not_checked')
        self.assertEqual(manifest['overlay_sha256']['agent/internal/runtime/runtime.go'], check.hashlib.sha256((target / 'agent/internal/runtime/runtime.go').read_bytes()).hexdigest())
        target4, _ = self.prepare('B04')
        self.assertEqual((target4 / check.STORAGE / 'migrations/006_b04.sql').read_text(), '-- fixture B04\nSELECT 6;')
        self.assertFalse((target4 / check.STORAGE / 'migrations/007_b05.sql').exists())

    def test_refuses_wrong_baseline(self):
        self.baseline[f'{check.STORAGE}/migrations/003_b01.sql'] += b'-- changed history'
        with self.assertRaisesRegex(SystemExit, 'exact frozen formal'):
            self.prepare()

    def test_refuses_symlinks_and_non_source(self):
        self.put('agent/internal/runtime/private.db', 'must reject')
        with self.assertRaisesRegex(SystemExit, 'non-source'):
            self.prepare()
        (self.root / 'agent/internal/runtime/private.db').unlink()
        (self.root / 'agent/internal/runtime/escape.go').symlink_to('/tmp')
        with self.assertRaisesRegex(SystemExit, 'symlink'):
            self.prepare()

    def test_refuses_unpinned_dependency(self):
        self.put('documents/backend-delivery/B05.deps', 'example.invalid/dependency@latest')
        with self.assertRaisesRegex(SystemExit, 'exact module'):
            self.prepare()

    def test_missing_contract_slot_and_parent_symlink(self):
        saved = self.baseline.pop('internal/control/bundle.go')
        with self.assertRaisesRegex(SystemExit, 'I02-P frozen'):
            self.prepare()
        self.baseline['internal/control/bundle.go'] = saved
        self.baseline[f'{check.STORAGE}/migrations/006_unexpected.sql'] = b'SELECT 1;'
        with self.assertRaisesRegex(SystemExit, 'exact frozen formal'):
            self.prepare()
        del self.baseline[f'{check.STORAGE}/migrations/006_unexpected.sql']
        (self.root / check.STORAGE / 'proposals/B05.sql').unlink()
        with self.assertRaisesRegex(SystemExit, 'explicit module SQL'):
            self.prepare()
        self.put(f'{check.STORAGE}/proposals/B05.sql', '-- no Controller changes\nSELECT 1;')
        (self.root / 'agent').symlink_to('/tmp')
        with self.assertRaisesRegex(SystemExit, 'symlink'):
            self.prepare()
        with patch.object(check, 'ROOT', self.root), self.assertRaisesRegex(SystemExit, 'outside workspace'):
            check.safe_source(self.root / '../escaped.go')

    def test_real_stage2_sqlc_test_vet_in_synthetic_snapshot(self):
        # Real compiler/toolchain, synthetic modules ONLY in a temporary fixture.
        # This proves orchestration, never claims B04/B05 business acceptance.
        repo = Path(check.__file__).resolve().parents[1]
        archive = check.subprocess.check_output(['git', 'archive', '2d3c360'], cwd=repo)
        with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
            self.baseline = {entry.name: tar.extractfile(entry).read()
                             for entry in tar.getmembers() if entry.isfile()}
        for module in ['B04', 'B05']:
            for owned in check.OWNED[module]:
                if owned in self.baseline:
                    self.put(owned, self.baseline[owned].decode())
            for package in check.PACKAGES[module]:
                rel = package[2:-4]
                self.put(f'{rel}/fixture.go', 'package fixture\nfunc FixtureMarker() string { return "tool fixture only" }\n')
                self.put(f'{rel}/fixture_test.go', 'package fixture\nimport "testing"\nfunc TestToolFixture(t *testing.T) { if FixtureMarker() == "" { t.Fatal("missing overlay") } }\n')
            original_run = check.run
            def actual_run(command, cwd, env):
                self.assertTrue(str(cwd).startswith(str(self.root / '.local/backend-check' / module)))
                self.assertEqual(env['GOMAXPROCS'], '1')
                self.assertEqual(env['CGO_ENABLED'], '0')
                env = dict(env, GOCACHE=str(repo / '.local/cache/go'), GOTMPDIR=str(repo / '.local/cache/go-tmp'))
                original_run(command, cwd, env)
            with patch.object(check, 'run', side_effect=actual_run):
                self.prepare(module, invoke_main=True)
            latest = max((self.root / '.local/backend-check' / module).glob('run-*'), key=lambda p: p.stat().st_mtime_ns)
            manifest = json.loads((latest / 'VALIDATION.json').read_text())
            self.assertEqual(manifest['status'], 'passed_isolated_only')
            self.assertEqual(len(manifest['checks']), 3)

    def test_missing_empty_and_prepare_only_never_pass(self):
        for content in [None, 'package runtime\n']:
            if content is not None:
                for name in ['runtime', 'checkpoint', 'authorization']:
                    self.put(f'agent/internal/{name}/empty.go', content.replace('runtime', name))
            with self.assertRaisesRegex(SystemExit, 'source incomplete/empty'), patch.object(check, 'run') as run:
                self.prepare(invoke_main=True)
                run.assert_not_called()
            latest = max((self.root / '.local/backend-check/B05').glob('run-*'), key=lambda p: p.stat().st_mtime_ns)
            self.assertEqual(json.loads((latest / 'VALIDATION.json').read_text())['status'], 'failed_NOT_PASSED')
        with patch.object(check, 'run') as run:
            self.prepare(invoke_main=True, prepare_only=True)
            run.assert_not_called()
        latest = max((self.root / '.local/backend-check/B05').glob('run-*'), key=lambda p: p.stat().st_mtime_ns)
        self.assertEqual(json.loads((latest / 'VALIDATION.json').read_text())['status'], 'prepare_only_NOT_PASSED')


class Stage3Isolation(unittest.TestCase):
    put = SnapshotIsolation.put
    prepare = SnapshotIsolation.prepare

    @classmethod
    def setUpClass(cls):
        cls.repo = Path(check.__file__).resolve().parents[1]
        archive = check.subprocess.check_output(['git', 'archive', '94ec907874b5a5d0c7d80e05a4efbf6c0a631535'], cwd=cls.repo)
        with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
            cls.base = {entry.name: tar.extractfile(entry).read()
                        for entry in tar.getmembers() if entry.isfile()}
        # Curated pending shared contract, never sibling code or T02 work.
        for rel in ['internal/control/bundle.go', 'internal/control/bundle_v2.go',
                    'internal/control/session_v2.go', 'internal/control/observation_v2.go',
                    'internal/control/bundle_v2_test.go', 'internal/secretstore/secretstore.go',
                    'controller/api/bundle-v2.schema.json', 'controller/api/doc.go',
                    'controller/api/contract_test.go', 'controller/internal/stage3/contract.go',
                    'controller/internal/stage3/pins.go', 'controller/internal/stage3/pins_test.go',
                    'controller/api/control.schema.json', 'controller/api/control-v2.schema.json',
                    'controller/api/openapi.json']:
            cls.base[rel] = (cls.repo / rel).read_bytes()

    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.baseline = self.base.copy()
        for module, (slot, _) in check.STAGE3.items():
            self.put(f'{check.STORAGE}/proposals/{module}.sql',
                     f'CREATE TABLE fixture_{slot} (id INTEGER PRIMARY KEY);')
            self.put(f'{check.STORAGE}/queries/{module.lower()}_fixture.sql',
                     f'-- name: Fixture{slot} :many\nSELECT id FROM fixture_{slot};')

    def latest(self, module):
        directory = self.root / '.local/backend-check' / module
        run = max(directory.glob('run-*'), key=lambda path: path.stat().st_mtime_ns)
        return run, json.loads((run / 'VALIDATION.json').read_text())

    def test_three_real_toolchains_and_sibling_isolation(self):
        self.put('internal/control/bundle_v2.go', 'ACTIVE SHARED MUST NOT COPY')
        self.put('agent/integration/t02-artifact/draft.go', 'ACTIVE T02 MUST NOT COPY')
        self.put('scripts/integration/t02/draft.py', 'ACTIVE T02 MUST NOT COPY')
        self.put(f'{check.STORAGE}/migrations/007_b05.sql', 'ACTIVE FORMAL MUST NOT COPY')
        for shapes in check.STAGE3_CONSTRUCTORS.values():
            for name, ctor, impl, opts, iface in shapes:
                self.put(f'controller/internal/{name}/fixture.go',
                         f'package {name}\nimport "github.com/nicccce/meshstrata/controller/internal/stage3"\n'
                         f'type {impl} struct {{ stage3.{iface} }}\n'
                         f'func {ctor}(stage3.{opts}) (*{impl}, error) {{ return &{impl}{{}}, nil }}\n')
        original_run = check.run
        for module, (slot, names) in check.STAGE3.items():
            with self.subTest(module=module):
                def actual_run(command, cwd, env):
                    self.assertTrue(cwd.is_relative_to(self.root / '.local/backend-check' / module))
                    self.assertEqual(env['GOMAXPROCS'],'1')
                    self.assertEqual(env['CGO_ENABLED'],'0')
                    self.assertIn('-mod=readonly',env['GOFLAGS'])
                    cached = dict(env,GOCACHE=str(self.repo / '.local/cache/go'),GOTMPDIR=str(self.repo / '.local/cache/go-tmp'))
                    Path(cached['GOTMPDIR']).mkdir(parents=True,exist_ok=True)
                    original_run(command,cwd,cached)
                with patch.object(check,'run',side_effect=actual_run):
                    self.prepare(module,invoke_main=True)
                target, manifest = self.latest(module)
                self.assertEqual(manifest['status'],'passed_isolated_only')
                self.assertTrue((target / manifest['contract_check']['path']).exists())
                self.assertEqual([c['exit_code'] for c in manifest['checks']], [0,0,0])
                self.assertEqual(manifest['baseline_migrations'],check.STAGE3_MIGRATIONS)
                self.assertEqual(manifest['scratch_migrations'],[f'{n:03}' for n in range(8,slot)])
                for sibling, (_, dirs) in check.STAGE3.items():
                    self.assertFalse((target / check.STORAGE / 'proposals' / f'{sibling}.sql').exists())
                    for name in dirs:
                        self.assertEqual((target / 'controller/internal' / name).exists(),name in names)
                self.assertEqual((target / 'internal/control/bundle_v2.go').read_bytes(),
                                 self.base['internal/control/bundle_v2.go'])
                self.assertFalse((target / 'agent/integration/t02-artifact').exists())
                self.assertFalse((target / 'scripts/integration/t02').exists())
                self.assertFalse((target / 'controller/web/node_modules').exists())

    def test_rejects_missing_implementation_proposal_history_and_non_source(self):
        with patch.object(check,'run') as run:
            self.prepare('B06',invoke_main=True,prepare_only=True)
            self.assertEqual(self.latest('B06')[1]['status'],'prepare_only_NOT_PASSED')
            self.put('controller/internal/subscriptions/empty.go','package subscriptions')
            with self.assertRaisesRegex(SystemExit,'source incomplete'):
                self.prepare('B08',invoke_main=True)
            self.assertEqual(self.latest('B08')[1]['status'],'failed_NOT_PASSED')
            run.assert_not_called()
        (self.root / check.STORAGE / 'proposals/B06.sql').unlink()
        with self.assertRaisesRegex(SystemExit,'explicit module SQL proposal'):
            self.prepare('B06')
        self.put('controller/internal/clientconfig/real.key','not source')
        with self.assertRaisesRegex(SystemExit,'non-source'):
            self.prepare('B07')
        (self.root / 'controller/internal/clientconfig/real.key').unlink()
        self.put('documents/backend-delivery/B07.deps','example.invalid/dependency@latest')
        with self.assertRaisesRegex(SystemExit,'exact module'):
            self.prepare('B07')
        (self.root / 'controller/internal/clientconfig/escape.go').symlink_to('/tmp')
        with self.assertRaisesRegex(SystemExit,'symlink'):
            self.prepare('B07')
        self.baseline[f'{check.STORAGE}/migrations/007_b05.sql'] += b'-- rewrite forbidden'
        with self.assertRaisesRegex(SystemExit,'exact frozen formal'):
            self.prepare('B07')


if __name__ == '__main__':
    unittest.main()
