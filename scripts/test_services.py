#!/usr/bin/env python3
"""Installer service filesystem/command regression; no privileged host writes.

The separate Linux smoke test exercises real service managers.
"""
import os
import re
import sys
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
            install = home / '.viorka-agent'
            install.mkdir(parents=True)
            (install / 'state').mkdir()
            identity = '{"node_id":""}' if failure == 'pending' else '{"node_id":"node-1"}'
            (install / 'state/identity.json').write_text(identity)
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
            executable('uname', 'case "$1" in -s) echo Linux;; *) echo x86_64;; esac\n')
            executable('id', 'echo ' + ('1000' if failure == 'nonroot' else '0') + '\n')
            # macOS stat differs; the Linux integration uses the actual GNU/BusyBox stat.
            executable('stat', 'case "$2" in %u) echo 0;; %a) echo 700;; esac\n')
            executable('timeout', 'shift\nexec "$@"\n')
            executable('systemctl', '''printf '%s\n' "$*" >> "$CALLS"
if [ "$1" = show ]; then
 case "$4" in
 LoadState) echo not-found;;
 FragmentPath) echo "$SERVICE";;
 DropInPaths) :;;
 esac
fi
if [ "$FAILURE" = upgrade ] && [ "$1" = restart ] && [ ! -e "$MARKER" ]; then touch "$MARKER"; exit 1; fi
if [ "$FAILURE" = start ] && [ "$1" = enable ]; then exit 1; fi
if [ "$FAILURE" = status ] && [ "$1" = is-active ]; then exit 1; fi
exit 0
''')
            executable('rc-service', '''printf '%s\n' "$*" >> "$CALLS"
if [ "$FAILURE" = upgrade ] && [ "$2" = restart ] && [ ! -e "$MARKER" ]; then touch "$MARKER"; exit 1; fi
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
            script = re.sub(r'/(?:root|etc|run|sbin)\b', lambda match: str(root) + match[0], script)
            local = root / 'install.sh'
            local.write_text(script)
            calls = root / 'calls'
            env = dict(os.environ, HOME=str(home), PATH=f'{bin_dir}:' + os.environ['PATH'], CALLS=str(calls), FAILURE=failure, SERVICE=str(service), MARKER=str(root / "restart-failed"))
            result = subprocess.run(['sh', str(local), 'service'], env=env, capture_output=True, timeout=10)
            self.assertEqual((install / 'state/identity.json').read_text(), identity)
            if failure:
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertNotIn('后台服务已启动'.encode(), result.stdout)
                self.assertNotIn('常用命令'.encode(), result.stdout)
                if failure == 'existing':
                    self.assertEqual(service.read_text(), 'do not touch')
                if failure == 'symlink':
                    self.assertTrue(service.is_symlink())
                    self.assertFalse((root / 'missing').exists())
                if failure in ('nonroot', 'pending'):
                    self.assertFalse(service.exists())
            else:
                self.assertEqual(result.returncode, 0, result.stderr.decode())
                self.assertIn('后台服务已启动'.encode(), result.stdout)
                self.assert_commands(result.stdout, manager)
                self.assertTrue(service.exists())
                self.assertIn(str(install), service.read_text())
                self.assertEqual(service.stat().st_mode & 0o777, 0o644 if manager == 'systemd' else 0o755)
                if manager == 'openrc':
                    subprocess.run(['sh', '-n', str(service)], check=True)
                else:
                    self.assertIn('enable --now viorka-agent.service', calls.read_text())

                curl = bin_dir / 'curl'
                curl.write_text(f'''#!{sys.executable}
import hashlib, os, pathlib, sys
args=sys.argv[1:]
if os.environ['FAILURE']=='download': sys.exit(22)
data=b'#!/bin/sh\\n# new release\\n[ "$1" != version ] || echo "viorka-agent 0.1.0 (linux/amd64)"\\nexit 0\\n'
if args[-1].endswith('.sha256'): data=(hashlib.sha256(data).hexdigest()+'\\n').encode()
pathlib.Path(args[args.index('--output')+1]).write_bytes(data)
''')
                curl.chmod(0o700)
                initial_service = service.read_bytes()
                initial_config = (install / 'agent.json').read_bytes()
                command = ['sh', str(local), 'install', '--version=v0.1.0']
                for upgrade_failure in ('download', 'upgrade', '', ''):
                    old_binary = agent.read_bytes()
                    result = subprocess.run(command, env=dict(env, FAILURE=upgrade_failure), capture_output=True, timeout=15)
                    self.assertEqual(result.returncode == 0, not upgrade_failure, result.stderr.decode())
                    self.assertEqual(service.read_bytes(), initial_service)
                    self.assertEqual((install / 'agent.json').read_bytes(), initial_config)
                    self.assertEqual((install / 'state/identity.json').read_text(), identity)
                    self.assertFalse(list(install.glob('.install-*')))
                    if upgrade_failure:
                        self.assertEqual(agent.read_bytes(), old_binary)
                        if upgrade_failure == 'upgrade':
                            self.assertIn('已恢复旧 Agent 二进制'.encode(), result.stderr)
                            restart = 'restart viorka-agent.service' if manager == 'systemd' else 'viorka-agent restart'
                            self.assertEqual(calls.read_text().count(restart), 2)
                    self.assertNotIn('sing-box', calls.read_text())
                    self.assertNotIn('xray', calls.read_text())
                    if not upgrade_failure:
                        self.assertIn(b'new release', agent.read_bytes())
                        self.assert_commands(result.stdout, manager)

    def assert_commands(self, output, manager):
        for action in ('start', 'stop', 'restart', 'status'):
            command = f'systemctl {action} viorka-agent' if manager == 'systemd' else f'rc-service viorka-agent {action}'
            self.assertIn(command.encode(), output)
        if manager == 'systemd':
            self.assertIn(b'journalctl -u viorka-agent -n 100 --no-pager', output)
            self.assertIn(b'journalctl -u viorka-agent -f', output)
            self.assertNotIn(b'rc-service', output)
        else:
            self.assertIn(b"tail -f /var/log/messages | grep 'viorka-agent'", output)
            self.assertIn(b"logread -f | grep 'viorka-agent'", output)
            self.assertNotIn(b'systemctl', output)
        self.assertIn(b'install.sh\' | sh -s -- install\n', output)
        self.assertNotIn(b'--url=', output)
        self.assertNotIn(b'--token=', output)
        self.assertNotIn(b'--foreground', output)
        self.assertIn(b'Panel connected; session established', output)

    def test_service_modes(self):
        for manager in ('systemd', 'openrc'):
            for failure in ('', 'nonroot', 'existing', 'symlink', 'start', 'status', 'pending'):
                with self.subTest(manager=manager, failure=failure):
                    self.exercise(manager, failure)


if __name__ == '__main__':
    unittest.main()
