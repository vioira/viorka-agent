#!/usr/bin/env python3
"""Uninstall through a real shell/filesystem, with isolated init/proc fixtures."""
import os
import re
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class Uninstall(unittest.TestCase):
    def exercise(self, manager, scenario='', tmp_parent=None):
        with tempfile.TemporaryDirectory(dir=tmp_parent) as tmp:
            root = Path(tmp).resolve()
            home = root / 'root'
            install = home / '.viorka-agent'
            install.mkdir(parents=True)
            home.chmod(0o700)
            install.chmod(0o700)
            (install / 'state').mkdir()
            preserved = {
                install / 'agent.json': b'preserve configuration',
                install / 'state/identity.json': b'preserve identity',
                install / 'state/journal.json': b'preserve journal',
                install / 'proxy.conf': b'preserve proxy config',
            }
            for path, data in preserved.items():
                path.write_bytes(data)
            agent = install / 'viorka-agent'
            agent.write_text('#!/bin/sh\nexit 0\n')
            agent.chmod(0o700)
            previous = install / 'viorka-agent.previous'
            previous.write_text('previous binary')
            bin_dir = root / 'bin'
            bin_dir.mkdir()

            def executable(name, text):
                path = bin_dir / name
                path.write_text('#!/bin/sh\n' + text)
                path.chmod(0o700)

            executable('uname', 'echo Linux\n')
            executable('id', 'if [ "$FAILURE" = nonroot ]; then echo 1000; else echo 0; fi\n')
            executable('stat', f'''exec '{sys.executable}' -c 'import os,sys; s=os.stat(sys.argv[-1]); print(0 if sys.argv[2]=="%u" else format(s.st_mode & 0o777,"o"))' "$@"\n''')
            executable('timeout', 'shift\nexec "$@"\n')
            executable('systemctl', '''printf '%s\n' "$*" >> "$CALLS"
case "$1" in
 show)
  case "$4" in
   LoadState) if [ -f "$SERVICE" ]; then echo loaded; else echo not-found; fi;;
   FragmentPath) if [ "$FAILURE" = fragment ]; then echo /foreign.service; else echo "$SERVICE"; fi;;
   DropInPaths) if [ "$FAILURE" = dropin ]; then echo /foreign.conf; fi;;
   ActiveState)
    if [ "$FAILURE" = status ]; then exit 1; fi
    if [ "$FAILURE" = active ] || [ ! -e "$STOPPED" ]; then echo active; else echo inactive; fi;;
  esac;;
 stop) [ "$FAILURE" != stop ] || exit 1; touch "$STOPPED";;
 disable) [ "$FAILURE" != disable ] || exit 1;;
 daemon-reload) [ "$FAILURE" != reload ] || exit 1;;
esac
exit 0
''')
            executable('rc-service', '''printf '%s\n' "$*" >> "$CALLS"
case "$2" in
 stop) [ "$FAILURE" != stop ] || exit 1; touch "$STOPPED";;
 status)
  [ "$FAILURE" != status ] || exit 1
  [ "$FAILURE" != active ] || exit 0
  if [ -e "$STOPPED" ]; then exit 3; fi;;
esac
exit 0
''')
            executable('rc-update', '''printf 'rc-update %s\n' "$*" >> "$CALLS"
if [ "$1" = -a ]; then
 [ "$FAILURE" != disable ] || exit 1
 rm "$RUNLEVELS"/default/viorka-agent "$RUNLEVELS"/custom/viorka-agent
fi
''')
            for name in ('supervise-daemon', 'logger'):
                executable(name, 'exit 0\n')
            # Uninstall must never fetch or execute the Agent.
            executable('curl', 'echo unexpected-download >> "$CALLS"; exit 99\n')
            service = root / ('etc/systemd/system/viorka-agent.service' if manager == 'systemd' else 'etc/init.d/viorka-agent')
            service.parent.mkdir(parents=True)
            proxy_service = service.parent / 'sing-box'
            proxy_service.write_text('unrelated service')
            preserved[proxy_service] = proxy_service.read_bytes()
            (root / ('run/systemd/system' if manager == 'systemd' else 'run/openrc')).mkdir(parents=True)
            (root / 'sbin').mkdir()
            openrc = root / 'sbin/openrc-run'
            openrc.write_text('#!/bin/sh\n')
            openrc.chmod(0o700)
            proc = root / 'proc'
            (proc / 'self').mkdir(parents=True)
            (proc / 'self/exe').symlink_to(sys.executable)
            script = (ROOT / 'install.sh').read_text()
            script = re.sub(r'/(?:root|etc|run|sbin|proc)\b', lambda match: str(root) + match[0], script)
            # Relocate the ancestor-walk boundary along with /etc: the host's
            # temporary directory (often /tmp, mode 1777) is outside this fixture.
            boundary = 'while [ "$path" != / ]; do'
            self.assertEqual(script.count(boundary), 1)
            script = script.replace(boundary, f'while [ "$path" != "{root}" ]; do')
            local = root / 'install.sh'
            local.write_text(script)
            calls = root / 'calls'
            env = dict(os.environ, HOME=str(home), PATH=f'{bin_dir}:' + os.environ['PATH'],
                       CALLS=str(calls), SERVICE=str(service), STOPPED=str(root / 'stopped'),
                       RUNLEVELS=str(root / 'etc/runlevels'), FAILURE='')

            def run(*args, failure=''):
                return subprocess.run(['sh', str(local), *args], env=dict(env, FAILURE=failure), capture_output=True, timeout=10)

            foreground = scenario.startswith('foreground')
            if not foreground:
                result = run('service')
                self.assertEqual(result.returncode, 0, result.stderr.decode())
                if manager == 'openrc':
                    for level in ('default', 'custom'):
                        link = root / 'etc/runlevels' / level / 'viorka-agent'
                        link.parent.mkdir(parents=True)
                        link.symlink_to(service)
            initial_service = service.read_bytes() if service.exists() else None
            agent.write_text('#!/bin/sh\necho unexpected-agent-execution >> "$CALLS"\nexit 99\n')
            before = agent.read_bytes()
            args = ['uninstall', *(['--foreground'] if foreground or scenario == 'bypass' else [])]
            if scenario == 'custom':
                service.write_text('third-party service')
                initial_service = service.read_bytes()
            if scenario in ('symlink', 'binary_symlink'):
                target = service if scenario == 'symlink' else agent
                target.unlink()
                target.symlink_to(proxy_service)
            if scenario == 'permissions':
                agent.chmod(0o777)
            if scenario == 'service_directory_permissions':
                service.parent.chmod(0o775)
            if scenario == 'service_ancestor_permissions':
                (root / 'etc').chmod(0o757)
            if scenario == 'locked':
                (install / '.install-lock').mkdir()
            if scenario in ('running', 'foreground_running'):
                (proc / '123').mkdir()
                (proc / '123/exe').symlink_to(str(agent) + ' (deleted)')
            if scenario == 'no_proc':
                (proc / 'self/exe').unlink()
            if scenario == 'bad_args':
                for arg in ('--url=', '--version=', '--token=', '--ca-file=', '--reenroll-node=', '--purge'):
                    invalid = run('uninstall', arg)
                    self.assertNotEqual(invalid.returncode, 0, arg)
                    self.assertEqual(agent.read_bytes(), before)
                args.append('--url=https://localhost')
            if scenario == 'stopped':
                (root / 'stopped').touch()
            calls.write_text('')
            result = run(*args, failure=scenario)
            success = scenario in ('', 'foreground', 'stopped')
            self.assertEqual(result.returncode == 0, success, result.stdout.decode() + result.stderr.decode())
            for path, data in preserved.items():
                self.assertEqual(path.read_bytes(), data)
            self.assertNotIn('unexpected-', calls.read_text())
            self.assertNotIn('sing-box', calls.read_text())
            if scenario in ('service_directory_permissions', 'service_ancestor_permissions'):
                self.assertIn('服务目录权限不安全。'.encode(), result.stderr)
                self.assertNotIn('stop', calls.read_text())
            if success:
                self.assertFalse(service.exists())
                self.assertFalse(agent.exists())
                self.assertFalse(previous.exists())
                self.assertFalse(list((root / 'etc/runlevels').glob('*/viorka-agent')))
                self.assertIn('Agent 卸载完成'.encode(), result.stdout)
                # Repeat uninstall, including a missing binary, must be harmless.
                result = run(*args)
                self.assertEqual(result.returncode, 0, result.stderr.decode())
                if foreground:
                    self.assertEqual(calls.read_text(), '')
            else:
                self.assertNotIn('Agent 卸载完成'.encode(), result.stdout)
                self.assertTrue(previous.exists())
                if scenario == 'binary_symlink':
                    self.assertTrue(agent.is_symlink())
                else:
                    self.assertEqual(agent.read_bytes(), before)
                if scenario == 'symlink':
                    self.assertTrue(service.is_symlink())
                elif scenario == 'reload':
                    self.assertFalse(service.exists())
                    retry = run(*args)
                    self.assertEqual(retry.returncode, 0, retry.stderr.decode())
                    self.assertFalse(agent.exists())
                elif initial_service is not None:
                    self.assertEqual(service.read_bytes(), initial_service)
            if scenario != 'locked':
                self.assertFalse((install / '.install-lock').exists())

    def test_uninstall(self):
        for manager in ('systemd', 'openrc'):
            scenarios = ('', 'stopped', 'foreground', 'foreground_running', 'custom', 'symlink',
                         'binary_symlink', 'permissions', 'locked', 'running', 'no_proc',
                         'service_directory_permissions', 'service_ancestor_permissions',
                         'bad_args', 'nonroot', 'bypass', 'stop', 'status', 'active', 'disable')
            if manager == 'systemd':
                scenarios += ('dropin', 'fragment', 'reload')
            for scenario in scenarios:
                with self.subTest(manager=manager, scenario=scenario):
                    self.exercise(manager, scenario)

    def test_shared_temp_parent(self):
        with tempfile.TemporaryDirectory() as tmp:
            # Reproduce a Linux /tmp parent even on hosts with a private TMPDIR.
            Path(tmp).chmod(0o1777)
            for manager in ('systemd', 'openrc'):
                with self.subTest(manager=manager):
                    self.exercise(manager, tmp_parent=tmp)


if __name__ == '__main__':
    unittest.main()
