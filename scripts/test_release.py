#!/usr/bin/env python3
"""Release flow tests using temporary real Git repositories; no network."""
import contextlib
import io
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.dont_write_bytecode = True
import release


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        root = Path(self.temp.name)
        self.repo = root / 'repo'
        self.repo.mkdir()
        self.remote = root / 'remote.git'
        subprocess.run(['git', 'init', '--bare', str(self.remote)], check=True, capture_output=True)
        self.root_patch = patch.object(release, 'ROOT', self.repo)
        self.root_patch.start()
        self.addCleanup(self.root_patch.stop)
        release.git('init', '-b', 'main')
        release.git('config', 'user.name', 'Release Test')
        release.git('config', 'user.email', 'test@example.invalid')
        release.git('config', 'commit.gpgsign', 'false')
        release.git('config', 'tag.gpgsign', 'false')
        self.version = self.repo / release.VERSION_FILE
        self.version.parent.mkdir(parents=True)
        self.version.write_text('package version\n\nconst Version = "0.1.0-bate.1"\n')
        release.git('add', '.')
        release.git('commit', '-m', 'initial')
        release.git('remote', 'add', 'origin', str(self.remote))
        self.head = release.git('rev-parse', 'HEAD')
        real_run = release.run

        def run(*args, **kwargs):
            # Validation/build behavior is tested separately by validate.sh itself.
            # All Git mutations, refs and transport here are real.
            if args[0] in ('sh', 'go'):
                if self.fail_validation:
                    raise subprocess.CalledProcessError(1, args)
                return None
            return real_run(*args, **kwargs)

        self.fail_validation = False
        self.run_patch = patch.object(release, 'run', side_effect=run)
        self.run_patch.start()
        self.addCleanup(self.run_patch.stop)

    def invoke(self, answers):
        with patch('builtins.input', side_effect=answers), contextlib.redirect_stdout(io.StringIO()):
            release.main()

    def test_version_validation(self):
        for value in ('v0.1.0-bate.1', '1.0.0', '1.2.3-rc.2+build.1'):
            self.assertEqual(release.normalize_version(value), value.removeprefix('v'))
        for value in ('01.0.0', '1.0.0-01', '../x', '1.0', '1.0.0;id', ''):
            with self.assertRaises(ValueError):
                release.normalize_version(value)

    def test_publish(self):
        self.invoke(['4', '0.2.0-bate.1', 'y'])
        self.assertIn('"0.2.0-bate.1"', self.version.read_text())
        self.assertEqual(release.git('status', '--porcelain'), '')
        self.assertNotEqual(release.git('rev-parse', 'HEAD'), self.head)
        self.assertIn('refs/tags/v0.2.0-bate.1', release.git('ls-remote', '--tags', 'origin'))
        self.assertIn(release.git('rev-parse', 'HEAD'), release.git('ls-remote', '--heads', 'origin'))

    def test_current_version_does_not_commit(self):
        self.invoke(['5', 'Y'])
        self.assertEqual(release.git('rev-parse', 'HEAD'), self.head)

    def test_cancel_keeps_edit_without_tag(self):
        self.invoke(['4', '0.2.0', ''])
        self.assertIn('"0.2.0"', self.version.read_text())
        self.assertEqual(release.git('rev-parse', 'HEAD'), self.head)
        self.assertEqual(release.git('tag'), '')

    def test_dirty_worktree(self):
        (self.repo / 'untracked').write_text('keep me')
        with self.assertRaises(ValueError):
            self.invoke([])
        self.assertEqual((self.repo / 'untracked').read_text(), 'keep me')

    def test_duplicate_local_tag(self):
        release.git('tag', 'v0.1.0-bate.1')
        with self.assertRaises(ValueError):
            self.invoke(['5'])

    def test_duplicate_remote_tag(self):
        release.git('tag', 'v0.1.0-bate.1')
        release.git('push', 'origin', 'refs/tags/v0.1.0-bate.1')
        release.git('tag', '-d', 'v0.1.0-bate.1')
        with self.assertRaises(ValueError):
            self.invoke(['5'])

    def test_failed_validation(self):
        self.fail_validation = True
        with self.assertRaises(subprocess.CalledProcessError):
            self.invoke(['4', '0.2.0'])
        self.assertEqual(release.git('tag'), '')
        self.assertEqual(release.git('rev-parse', 'HEAD'), self.head)

    def test_failed_push_keeps_local_tag(self):
        hook = self.remote / 'hooks' / 'pre-receive'
        hook.write_text('#!/bin/sh\nexit 1\n')
        hook.chmod(0o755)
        with self.assertRaises(subprocess.CalledProcessError):
            self.invoke(['4', '0.2.0', 'y'])
        self.assertEqual(release.git('tag'), 'v0.2.0')
        self.assertEqual(release.git('ls-remote', 'origin'), '')

    def test_detached_head(self):
        release.git('checkout', '--detach')
        with self.assertRaises(subprocess.CalledProcessError):
            self.invoke([])

    def test_menu_versions(self):
        for answers, expected in [([''], '0.1.1'), (['1'], '0.1.1'), (['2'], '0.2.0'),
                                  (['3'], '1.0.0'), (['6', ''], '0.1.0-bate.2'),
                                  (['7'], '0.1.0'), (['0'], None)]:
            with patch('builtins.input', side_effect=answers), contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(release.choose_version('0.1.0-bate.1'), expected)

    def test_semver_order(self):
        ordered = ['1.0.0-alpha', '1.0.0-alpha.1', '1.0.0-alpha.beta',
                   '1.0.0-bate.1', '1.0.0-bate.2', '1.0.0-bate.10',
                   '1.0.0-beta.1', '1.0.0-rc.1', '1.0.0', '1.0.1']
        self.assertEqual(sorted(reversed(ordered), key=release.version_key), ordered)
        self.assertEqual(release.version_key('1.0.0+abc'), release.version_key('1.0.0+xyz'))

    def test_menu_rejects_downgrade(self):
        with self.assertRaises(ValueError):
            self.invoke(['4', '0.0.9'])
        self.assertEqual(release.git('status', '--porcelain'), '')

    def test_newer_remote_release_rejected(self):
        release.git('tag', 'v1.0.0')
        release.git('push', 'origin', 'refs/tags/v1.0.0')
        release.git('tag', '-d', 'v1.0.0')
        with self.assertRaises(ValueError):
            self.invoke(['5'])
        self.assertEqual(release.git('status', '--porcelain'), '')

    def test_remote_ahead_rejected(self):
        release.git('commit', '--allow-empty', '-m', 'remote change')
        release.git('push', 'origin', 'main')
        release.git('reset', '--hard', self.head)
        with self.assertRaisesRegex(ValueError, '分叉'):
            self.invoke([])
        self.assertEqual(release.git('tag'), '')

    def test_menu_cancel_is_clean(self):
        self.invoke(['0'])
        self.assertEqual(release.git('status', '--porcelain'), '')
        self.assertEqual(release.git('tag'), '')

    def test_single_non_origin_remote(self):
        release.git('remote', 'rename', 'origin', 'upstream')
        self.invoke(['0'])

    def test_multiple_remotes_prompt(self):
        release.git('remote', 'add', 'upstream', str(self.remote))
        self.invoke(['upstream', '0'])

    def test_no_remote(self):
        release.git('remote', 'remove', 'origin')
        with self.assertRaisesRegex(ValueError, '未配置'):
            self.invoke([])

    def test_non_main_rejected(self):
        release.git('checkout', '-b', 'feature')
        with self.assertRaisesRegex(ValueError, 'main'):
            self.invoke([])

    def test_different_push_destination_rejected(self):
        release.git('remote', 'set-url', '--push', 'origin', str(self.repo.parent / 'other.git'))
        with self.assertRaisesRegex(ValueError, 'push URL'):
            self.invoke([])


class StageOutputTests(unittest.TestCase):
    def test_success_hides_child_output_and_removes_log(self):
        with tempfile.TemporaryDirectory() as directory:
            output, errors = io.StringIO(), io.StringIO()
            with patch.object(tempfile, 'tempdir', directory), \
                    contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors):
                release.run_stage('[1/3] 完整验证', sys.executable, '-c',
                                  'import sys; print("test stdout"); print("test stderr", file=sys.stderr)')
            self.assertIn('通过', output.getvalue())
            self.assertNotIn('test stdout', output.getvalue())
            self.assertEqual(errors.getvalue(), '')
            self.assertEqual(list(Path(directory).iterdir()), [])

    def test_failure_shows_tail_and_preserves_full_log(self):
        with tempfile.TemporaryDirectory() as directory:
            output, errors = io.StringIO(), io.StringIO()
            with patch.object(tempfile, 'tempdir', directory), \
                    contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors):
                with self.assertRaises(subprocess.CalledProcessError):
                    release.run_stage('[2/3] 构建', sys.executable, '-c',
                                      'import sys; print("first line"); '
                                      '[print(f"line {i}") for i in range(40)]; '
                                      'print("build error", file=sys.stderr); sys.exit(2)')
            logs = list(Path(directory).glob('*.log'))
            self.assertEqual(len(logs), 1)
            self.assertIn('first line', logs[0].read_text())
            self.assertIn('build error', logs[0].read_text())
            self.assertNotIn('first line', errors.getvalue())
            self.assertIn('line 39', errors.getvalue())
            self.assertIn(str(logs[0]), errors.getvalue())
            self.assertNotIn('通过', output.getvalue())


if __name__ == '__main__':
    unittest.main()
