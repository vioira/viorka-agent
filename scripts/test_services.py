#!/usr/bin/env python3
"""Installer service filesystem/command regression; no privileged host writes.

The separate Linux smoke test exercises real service managers.
"""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class Services(unittest.TestCase):
    def exercise(self, manager, failure=''):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            home = root / 'root'
            install = home / '.local/share/viorka-agent'
            install.mkdir(parents=True)
            (install / 'state').mkdir()
            (install / 'state/identity.json').write_text('preserve identity')
            (install / 'agent.json').write_text('{}')
            agent = install / 'viorka-agent'
            agent.write_text('#!/bin/sh\nexit 0\n')
            agent.chmod(0o700)
            bin_dir = root / 'bin'
            bin_dir.mkdir()
            def executable(name, text):
                p = bin_dir / name
                p.write_text('#!/bin/sh\n' + text)
                p.chmod(0o700)
            executable('uname', 'echo Linux\n')
            executable('id', 'echo ' + ('1000' if failure == 'nonroot' else '0') + '\n')
            # macOS stat differs; the Linux integration uses the actual GNU/BusyBox stat.
            executable('stat', 'case "$2" in %u) echo 0;; %a) echo 700;; esac\n')
            executable('timeout', 'shift\nexec "$@"\n')
            executable('systemctl', '''printf '%s\n' "$*" >> "$CALLS"
if [ "$1" = show ]; then echo not-found; fi
if [ "$FAILURE" = start ] && [ "$1" = enable ]; then exit 1; fi
if [ "$FAILURE" = status ] && [ "$1" = is-active ]; then exit 1; fi
exit 0
''')
            executable('rc-service', '''printf '%s\n' "$*" >> "$CALLS"
if [ "$FAILURE" = start ] && [ "$2" = start ]; then exit 1; fi
if [ "$FAILURE" = status ] && [ "$2" = status ]; then exit 1; fi
exit 0
''')
            for name in ('rc-update', 'supervise-daemon', 'logger'):
                executable(name, 'exit 0\n')
            service = root / ('etc/systemd/system/viorka-agent.service' if manager == 'systemd' else 'etc/init.d/viorka-agent')
            service.parent.mkdir(parents=True)
            runtime = root / ('run/systemd/system' if manager == 'systemd' else 'run/openrc')
            runtime.mkdir(parents=True)
            openrc = root / 'sbin/openrc-run'
            openrc.parent.mkdir()
            openrc.write_text('#!/bin/sh\n')
            openrc.chmod(0o700)
            if failure == 'existing':
                service.write_text('do not touch')
            if failure == 'symlink':
                service.symlink_to(root / 'missing')
            script = (ROOT / 'install.sh').read_text()
            # Relocate only fixed filesystem constants, never add installer override knobs.
            for prefix in ('/root', '/etc', '/run', '/sbin'):
                script = script.replace(prefix, str(root) + prefix)
            local = root / 'install.sh'
            local.write_text(script)
            calls = root / 'calls'
            env = dict(os.environ, HOME=str(home), PATH=f'{bin_dir}:' + os.environ['PATH'], CALLS=str(calls), FAILURE=failure)
            result = subprocess.run(['sh', str(local), 'service'], env=env, capture_output=True, timeout=10)
            self.assertEqual((install / 'state/identity.json').read_text(), 'preserve identity')
            if failure:
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertNotIn('后台服务已启动'.encode(), result.stdout)
                if failure == 'existing':
                    self.assertEqual(service.read_text(), 'do not touch')
                if failure == 'symlink':
                    self.assertTrue(service.is_symlink())
                    self.assertFalse((root / 'missing').exists())
                if failure == 'nonroot':
                    self.assertFalse(service.exists())
            else:
                self.assertEqual(result.returncode, 0, result.stderr.decode())
                self.assertIn('后台服务已启动'.encode(), result.stdout)
                self.assertTrue(service.exists())
                self.assertIn(str(install), service.read_text())
                self.assertEqual(service.stat().st_mode & 0o777, 0o644 if manager == 'systemd' else 0o755)
                if manager == 'openrc':
                    subprocess.run(['sh', '-n', str(service)], check=True)
                else:
                    self.assertIn('enable --now viorka-agent.service', calls.read_text())

    def test_service_modes(self):
        for manager in ('systemd', 'openrc'):
            for failure in ('', 'nonroot', 'existing', 'symlink', 'start', 'status'):
                with self.subTest(manager=manager, failure=failure):
                    self.exercise(manager, failure)


if __name__ == '__main__':
    unittest.main()
