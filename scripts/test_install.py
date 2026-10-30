#!/usr/bin/env python3
"""Exercise the installer in a real PTY with local release fixtures, never network.

Only platform detection/download transport and the Agent executable are fixtures.
The shell, terminal echo, SHA-256 verification and filesystem effects are real.
"""
import json
import os
import pathlib
import pty
import select
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]
TOKEN = "a" * 64


class InstallerTests(unittest.TestCase):
    def run_install(self, failure="", extra=(), arch="x86_64", pinned=False, token_arg=False):
        with tempfile.TemporaryDirectory(prefix="viorka-install-") as tmp:
            home = pathlib.Path(tmp) / 'home "quote" \\ slash'
            home.mkdir()
            bin_dir = pathlib.Path(tmp) / "bin"
            bin_dir.mkdir()
            def executable(name, contents):
                path = bin_dir / name
                path.write_text(contents)
                path.chmod(0o700)
            executable("uname", f'#!/bin/sh\ncase "$1" in -s) echo Linux;; *) echo {arch};; esac\n')
            fixture = b'''#!/bin/sh
set -eu
printf '%s\\n' "$1" >> calls
case "$1" in
 config-check) test "$FAILURE" != config ;;
 enroll)
   token=$(cat)
   test "${#token}" -eq 64
   test "$FAILURE" != enroll
   printf registered > state/identity.json ;;
 run) test -f state/identity.json ;;
esac
'''
            payload = pathlib.Path(tmp) / "payload"
            payload.write_bytes(fixture)
            executable("curl", f'''#!{sys.executable}
import hashlib, os, pathlib, sys
args=sys.argv[1:]; url=args[-1]
if os.environ['FAILURE']=='download': sys.exit(22)
if url.endswith('/latest'):
    print('https://github.com/vioira/viorka-agent/releases/tag/v0.1.0', end='')
else:
    assert '/v0.1.0/viorka-agent-linux-' in url, url
    output=pathlib.Path(args[args.index('--output')+1])
    data=pathlib.Path(os.environ['PAYLOAD']).read_bytes()
    if url.endswith('.sha256'):
        digest=hashlib.sha256(data).hexdigest()
        if os.environ['FAILURE']=='checksum': digest='0'*64
        if os.environ['FAILURE']=='manifest': digest='not a digest'
        output.write_text(digest+'\\n')
    else: output.write_bytes(data)
''')
            install = home / ".local/share/viorka-agent"
            if failure == "existing":
                install.mkdir(parents=True)
                (install / "identity").write_text("preserve")
            args = ["sh", str(ROOT / "install.sh"), "install", "--url=https://localhost:8143", *extra]
            if pinned:
                args.append("--version=v0.1.0")
            if token_arg:
                args.append("--token=" + TOKEN)
                env = dict(os.environ, HOME=str(home), PATH=f"{bin_dir}:{os.environ['PATH']}", FAILURE=failure, PAYLOAD=str(payload))
                # A new session with pipe stdin has no controlling terminal.
                result = subprocess.run(["sh", "-s", "--", *args[2:]], input=(ROOT / "install.sh").read_bytes(),
                                        stdout=subprocess.PIPE, stderr=subprocess.STDOUT, env=env,
                                        start_new_session=True, timeout=15)
                output = result.stdout
                status = result.returncode << 8
                self.assertNotIn('输入隐藏'.encode(), output)
            else:
                pid, fd = pty.fork()
                if pid == 0:
                    os.environ.update(HOME=str(home), PATH=f"{bin_dir}:{os.environ['PATH']}", FAILURE=failure, PAYLOAD=str(payload))
                    if pinned:
                        # Exercise `sh -s -- install ...` with script stdin separate from /dev/tty.
                        script_fd = os.open(ROOT / "install.sh", os.O_RDONLY)
                        os.dup2(script_fd, 0)
                        os.close(script_fd)
                        args = ["sh", "-s", "--", *args[2:]]
                    os.execvp(args[0], args)
                output = b""
                sent = False
                status = None
                try:
                    end = time.monotonic() + 15
                    while time.monotonic() < end:
                        if select.select([fd], [], [], .05)[0]:
                            try:
                                chunk = os.read(fd, 65536)
                            except OSError:
                                chunk = b""
                            output += chunk
                            if not sent and '（输入隐藏）：'.encode() in output:
                                os.write(fd, (TOKEN + "\n").encode())
                                sent = True
                        done, status_value = os.waitpid(pid, os.WNOHANG)
                        if done:
                            status = status_value
                            break
                    self.assertIsNotNone(status, "installer timed out")
                finally:
                    if status is None:
                        os.kill(pid, signal.SIGKILL)
                        os.waitpid(pid, 0)
                    os.close(fd)
            self.assertNotIn(TOKEN.encode(), output, "token leaked to terminal")
            for path in install.rglob("*"):
                if path.is_file():
                    self.assertNotIn(TOKEN.encode(), path.read_bytes(), f"token persisted in {path.name}")
            code = os.waitstatus_to_exitcode(status)
            calls = (install / "calls").read_text().splitlines() if (install / "calls").exists() else []
            if not failure and not extra and arch != "riscv64":
                self.assertEqual(code, 0, output.decode())
                self.assertEqual(calls, ["config-check", "enroll", "run"])
                config = json.loads((install / "agent.json").read_text())
                self.assertEqual(config["state_dir"], str(install.resolve() / "state"))
                self.assertEqual(config["panel_url"], "https://localhost:8143")
                self.assertEqual(install.stat().st_mode & 0o777, 0o700)
                self.assertEqual((install / "agent.json").stat().st_mode & 0o777, 0o600)
                self.assertEqual((install / "state").stat().st_mode & 0o777, 0o700)
            else:
                self.assertNotEqual(code, 0, output.decode())
                self.assertNotIn("run", calls)
                if failure not in ("config", "enroll"):
                    self.assertEqual(calls, [])
                if failure == "config":
                    self.assertEqual(calls, ["config-check"])
                if failure == "enroll":
                    self.assertEqual(calls, ["config-check", "enroll"])
                if failure in ("checksum", "manifest"):
                    self.assertTrue((install / "viorka-agent.download").exists())
                    self.assertFalse((install / "viorka-agent").exists())
                if failure == "existing":
                    self.assertEqual((install / "identity").read_text(), "preserve")

    def test_install_and_permissions(self):
        for arch in ("x86_64", "aarch64"):
            with self.subTest(arch=arch):
                self.run_install(arch=arch, pinned=arch == "aarch64")

    def test_token_argument_without_terminal(self):
        self.run_install(token_arg=True)
        self.run_install(token_arg=True, failure="enroll")

    def test_failures(self):
        for failure in ("existing", "download", "checksum", "manifest", "config", "enroll"):
            with self.subTest(failure=failure):
                self.run_install(failure=failure)
        self.run_install(arch="riscv64")

    def test_bad_arguments(self):
        for arg in ("--url=http://localhost", "--url=https://host/path", "--url=https://user:pass@host", "--url=https://host\nBAD", "--url=https://host'$(id)'", "--auto_update=1", "-secret=do-not-log", "--version=../../bad", "--token=", "--token=short", "--token=" + "g" * 64, "--token=" + "a" * 63 + "\'"):
            with self.subTest(arg=arg):
                self.run_install(extra=(arg,))


if __name__ == "__main__":
    if not shutil.which("sha256sum"):
        sys.exit("sha256sum required")
    unittest.main()
