#!/usr/bin/env python3
"""Exercise installer image selection and failure ordering without a Docker daemon."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / 'scripts').mkdir()
        (self.root / 'deploy').mkdir()
        (self.root / 'bin').mkdir()
        shutil.copy(ROOT / 'scripts/install.sh', self.root / 'scripts/install.sh')
        shutil.copy(ROOT / 'deploy/compose.yaml', self.root / 'deploy/compose.yaml')
        self.log = self.root / 'docker.jsonl'
        stub = self.root / 'bin/docker'
        stub.write_text('''#!/usr/bin/env python3
import json, os, sys
args = sys.argv[1:]
with open(os.environ['DOCKER_TEST_LOG'], 'a') as out:
    out.write(json.dumps(args) + '\\n')
if args[:2] == ['image', 'inspect']:
    print('sha256:' + 'a' * 64)
if 'pull' in args and os.environ.get('DOCKER_TEST_PULL_FAIL'):
    sys.exit(1)
''')
        stub.chmod(0o755)
        self.env = {k: v for k, v in os.environ.items() if not k.startswith('Q4D_') and k != 'COMPOSE_PROFILES'}
        self.env.update(PATH=str(self.root / 'bin') + os.pathsep + os.environ['PATH'], DOCKER_TEST_LOG=str(self.log))

    def run_install(self, *args, ok=True):
        result = subprocess.run(['bash', str(self.root / 'scripts/install.sh'), *args], cwd=self.root,
                                env=self.env, capture_output=True, text=True)
        if ok:
            self.assertEqual(result.returncode, 0, result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0)
        return result

    def calls(self):
        return [json.loads(line) for line in self.log.read_text().splitlines()] if self.log.exists() else []

    def test_source_build_remains_default(self):
        self.run_install()
        builds = [a[-1] for a in self.calls() if 'build' in a]
        self.assertEqual(builds, ['api', 'web'])
        self.assertFalse(any('pull' in a for a in self.calls()))

    def test_pull_all_extensions_before_setup_without_build(self):
        self.run_install('--pull', '--image-prefix', 'docker.io/example/quant4dad-opensource',
                         '--image-tag', 'v1.2.3', '--with-mcp', '--with-agent')
        calls = self.calls()
        pull_index = next(i for i, a in enumerate(calls) if 'pull' in a)
        self.assertEqual(calls[pull_index][-4:], ['api', 'web', 'mcp', 'agent'])
        self.assertLess(pull_index, next(i for i, a in enumerate(calls) if a[0] == 'run'))
        self.assertFalse(any('build' in a for a in calls))
        saved = (self.root / 'data/standalone/deployment.env').read_text()
        for service in ('api', 'web', 'mcp', 'agent'):
            self.assertIn('docker.io/example/quant4dad-opensource-' + service + ':v1.2.3', saved)

    def test_bundle_replaces_local_images_and_preserves_extensions(self):
        self.run_install()
        (self.root / 'data/standalone/profiles').write_text('mcp\nagent\n')
        (self.root / 'deploy/images.env').write_text(''.join(
            f'Q4D_{s.upper()}_IMAGE=docker.io/example/quant4dad-opensource-{s}:v2\n'
            for s in ('api', 'web', 'mcp', 'agent')))
        self.log.unlink()
        self.run_install('--pull')
        pull = next(a for a in self.calls() if 'pull' in a)
        self.assertEqual(pull[-4:], ['api', 'web', 'mcp', 'agent'])
        self.assertIn('quant4dad-opensource-api:v2', (self.root / 'data/standalone/deployment.env').read_text())

    def test_failed_pull_never_runs_setup_or_starts_containers(self):
        self.env['DOCKER_TEST_PULL_FAIL'] = '1'
        self.run_install('--pull', '--image-prefix', 'docker.io/example/quant4dad-opensource', ok=False)
        self.assertFalse(any(a[0] == 'run' or 'up' in a or 'build' in a for a in self.calls()))

    def test_offline_cached_images_never_pull_or_build(self):
        self.run_install('--no-build')
        self.assertFalse(any('pull' in a or 'build' in a for a in self.calls()))

    def test_missing_public_images_or_conflicting_options_are_rejected(self):
        for args in [('--pull',), ('--pull', '--no-build'),
                     ('--image-prefix', 'docker.io/example/test'),
                     ('--pull', '--image-tag', 'v1'),
                     ('--pull', '--image-prefix', 'BAD PREFIX')]:
            with self.subTest(args=args):
                self.run_install(*args, ok=False)
                self.assertFalse(any(a[0] == 'run' or 'up' in a or 'pull' in a for a in self.calls()))


class DownloadTests(unittest.TestCase):
    def test_download_preserves_data_and_forwards_extensions(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            bindir = root / 'bin'
            bindir.mkdir()
            target = root / 'installation'
            (target / 'data').mkdir(parents=True)
            (target / 'data/keep').write_text('existing data')
            curl = bindir / 'curl'
            curl.write_text('''#!/usr/bin/env python3
import os, pathlib, sys
args = sys.argv[1:]
url = args[args.index('--output') - 1]
if os.environ.get('FAIL_DOWNLOAD') and url.endswith('compose.yaml'):
    sys.exit(22)
content = 'example'
if url.endswith('install.sh'):
    content = '#!/usr/bin/env bash\\nprintf "%s\\\\n" "$@" > "$Q4D_INSTALL_DIR/arguments"\\n'
pathlib.Path(args[args.index('--output') + 1]).write_text(content)
''')
            curl.chmod(0o755)
            env = {k: v for k, v in os.environ.items() if not k.startswith('Q4D_')}
            env.update(PATH=str(bindir) + os.pathsep + os.environ['PATH'], Q4D_INSTALL_DIR=str(target))
            command = ['bash', str(ROOT / 'scripts/download.sh'), '--with-agent']
            result = subprocess.run(command, env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((target / 'arguments').read_text().splitlines(), ['--pull', '--with-agent'])
            self.assertEqual((target / 'data/keep').read_text(), 'existing data')
            (target / 'scripts/install.sh').write_text('prior installer')
            env['FAIL_DOWNLOAD'] = '1'
            self.assertNotEqual(subprocess.run(command, env=env, capture_output=True).returncode, 0)
            self.assertEqual((target / 'scripts/install.sh').read_text(), 'prior installer')


if __name__ == '__main__':
    unittest.main()
