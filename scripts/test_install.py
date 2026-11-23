#!/usr/bin/env python3
"""Exercise the installer in a real PTY with local release fixtures, never network.

Only platform detection/download transport and the Agent executable are fixtures.
The shell, terminal echo, SHA-256 verification and filesystem effects are real.
"""
import fcntl
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
from types import SimpleNamespace

ROOT = pathlib.Path(__file__).resolve().parents[1]
TOKEN = "a" * 64


class InstallerTests(unittest.TestCase):
    def run_install(self, failure="", extra=(), arch="x86_64", pinned=False, token_arg=False, retry=False, running=False, retry_failure="", reenroll=False, replacement=False, choice=None, locked_state=False, omit_url=False, retry_omit_url=False, registration=False, token_stdin=False, retry_latest=False, current_probe="", github_proxy="", expected_downloads=None):
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
            executable("stat", f'''#!{sys.executable}
import os, sys
s=os.stat(sys.argv[-1])
print(s.st_uid if sys.argv[2]=='%u' else format(s.st_mode & 0o777, 'o'))
''')
            executable("timeout", f'''#!{sys.executable}
import os, subprocess, sys
args=sys.argv[1:]
if args[0]=='-k': args=args[2:]
seconds=float(args.pop(0))
if os.environ.get('FAILURE')=='version_timeout': seconds=0.1
try:
    sys.exit(subprocess.run(args, timeout=seconds).returncode)
except subprocess.TimeoutExpired:
    sys.exit(124)
''')
            executable("flock", f'#!{sys.executable}\nimport fcntl, sys\nfcntl.flock(int(sys.argv[-1]), fcntl.LOCK_EX | fcntl.LOCK_NB)\n')
            fixture = b'''#!/bin/sh
set -eu
if [ "$1" = version ]; then
  if [ "$0" = ./viorka-agent ]; then
    case "${CURRENT_PROBE:-}" in
      unavailable) exit 1 ;;
      older) echo 'viorka-agent 0.0.9 (linux/amd64)'; exit 0 ;;
      other_arch) echo 'viorka-agent 0.1.0 (linux/arm64)'; exit 0 ;;
      oversized) exec yes 'excessive version output' ;;
      multiline) printf 'viorka-agent 0.1.0 (linux/%s)\\nextra output\\n' "$TEST_ARCH"; exit 0 ;;
    esac
    echo "viorka-agent 0.1.0 (linux/$TEST_ARCH)"
    exit 0
  fi
  case "$FAILURE" in
    version_timeout) exec sleep 30 ;;
    version) echo 'viorka-agent 9.9.9 (linux/amd64)' ;;
    platform) echo 'viorka-agent 0.1.0 (darwin/arm64)' ;;
    *) echo "viorka-agent 0.1.0 (linux/$TEST_ARCH)" ;;
  esac
  exit 0
fi
printf '%s\\n' "$1" >> calls
case "$1" in
 config-check)
   if [ "$FAILURE" = config ]; then
     echo 'invalid configuration: fixture error' >&2
     exit 2
   fi
   echo 'configuration format OK (connection not attempted)' ;;
 register)
   token=$(cat)
   test "${#token}" -eq 64
   if [ "$FAILURE" = enroll ]; then
     printf '{"node_id":"","private_key":"retained-key"}' > state/identity.json
     exit 1
   fi
   printf '{"node_id":"%s","private_key":"retained-key"}' "${NODE_ID:-node-1}" > state/identity.json ;;
 run) test -f state/identity.json ;;
esac
'''
            payload = pathlib.Path(tmp) / "payload"
            payload.write_bytes(fixture)
            executable("curl", f'''#!{sys.executable}
import hashlib, os, pathlib, sys
args=sys.argv[1:]; url=args[-1]
with pathlib.Path(os.environ['PAYLOAD']).with_name('download-urls').open('a') as log:
    log.write(url+'\\n')
if os.environ['FAILURE']=='download' or (os.environ['FAILURE']=='missing_checksum' and url.endswith('.sha256')): sys.exit(22)
if url.endswith('/latest'):
    if 'https://api.github.com/' in url:
        print('{{"tag_name":"v0.1.0","prerelease":false}}' if os.environ['FAILURE'] != 'bad_proxy_metadata' else '{{}}')
        sys.exit(0)
    if url.startswith('https://api-proxy.example/'):
        assert '--no-location' in args and '%{{redirect_url}}' in args
        sys.exit(0)
    prefix, _, _ = url.rpartition('https://github.com/')
    if prefix:
        assert '--no-location' in args and '%{{redirect_url}}' in args
    if os.environ['FAILURE']=='no_release':
        print('https://github.com/vioira/viorka-agent/releases', end='')
        sys.exit(0)
    if os.environ['FAILURE']=='invalid_release':
        print('https://github.com/vioira/viorka-agent/releases/tag/invalid', end='')
        sys.exit(0)
    print(prefix+'https://github.com/vioira/viorka-agent/releases/tag/v0.1.0', end='')
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
            fixture = fixture.replace(b"$TEST_ARCH", b"arm64" if arch == "aarch64" else b"amd64")
            payload.write_bytes(fixture)
            install = home / ".viorka-agent"
            if failure == "preinstalled":
                install.mkdir(mode=0o700)
                (install / "viorka-agent").write_bytes(fixture)
                (install / "viorka-agent").chmod(0o700)
            if failure == "legacy":
                legacy = home / ".local/share/viorka-agent"
                legacy.mkdir(parents=True)
                (legacy / "agent.json").write_text("preserve")
            if failure == "existing":
                install.mkdir(parents=True)
                (install / "identity").write_text("preserve")
            if failure in ("symlink", "locked", "missing_config"):
                install.mkdir(parents=True)
                if failure == "symlink":
                    (install / "viorka-agent").symlink_to(payload)
                elif failure == "locked":
                    (install / ".install-lock").mkdir()
                else:
                    (install / "state").mkdir()
                    (install / "state/identity.json").write_text("preserve")
            args = ["sh", str(ROOT / "install.sh"), "install", "--foreground", "--url=https://localhost:8143", *extra]
            if github_proxy:
                args.append(f"--github-proxy={github_proxy}")
            if registration:
                args.append("--registration")
            if omit_url:
                args.remove("--url=https://localhost:8143")
            if pinned:
                args.append("--version=v0.1.0")
            if token_stdin:
                args.append("--token-stdin")
                env = dict(os.environ, HOME=str(home), PATH=f"{bin_dir}:{os.environ['PATH']}", FAILURE=failure, PAYLOAD=str(payload))
                result = subprocess.run(args, input=(TOKEN + "\n").encode(), stdout=subprocess.PIPE, stderr=subprocess.STDOUT, env=env, start_new_session=True, timeout=15)
                output = result.stdout
                status = result.returncode << 8
            elif token_arg:
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
            if expected_downloads is not None:
                log = payload.with_name("download-urls")
                downloads = log.read_text().splitlines() if log.exists() else []
                self.assertEqual(downloads, expected_downloads, output.decode())
            for path in install.rglob("*"):
                if path.is_file():
                    self.assertNotIn(TOKEN.encode(), path.read_bytes(), f"token persisted in {path.name}")
            code = os.waitstatus_to_exitcode(status)
            calls = (install / "calls").read_text().splitlines() if (install / "calls").exists() else []
            platform = "linux/arm64" if arch == "aarch64" else "linux/amd64"
            current_label = '当前 Agent 版本（磁盘）：'
            installed_label = '已安装 Agent 版本：'
            release_progress = '[安装中] 正在查询 GitHub 上的最新正式版…'
            download_progress = '[安装中] 正在下载 Agent 安装包…'
            skip_progress = '[安装中] 本地 Agent 版本和架构与目标一致，跳过下载。'
            self.assertEqual(current_label.encode() in output, failure == "preinstalled")
            if failure in ("", "existing", "preinstalled") and not extra and arch != "riscv64":
                self.assertEqual(code, 0, output.decode())
                self.assertIn(f'目标 Agent 版本：v0.1.0 ({platform})'.encode(), output)
                progress = [line for line in output.decode().splitlines() if line.startswith('[安装中]')]
                self.assertEqual(progress, [
                    *([] if pinned else [release_progress]),
                    *([skip_progress] if failure == "preinstalled" else [
                        download_progress,
                        '[安装中] 正在下载 SHA-256 校验文件…',
                        '[安装中] 正在校验安装包的 SHA-256、版本和架构…',
                    ]),
                    '[安装中] 正在校验 Agent 配置…',
                    *([] if failure == "preinstalled" else ['[安装中] 正在写入 Agent 程序…']),
                    '[安装中] 正在向 Panel 提交 Agent 注册请求…',
                ])
                self.assertLess(output.index('安装目录：'.encode()), output.index((skip_progress if failure == 'preinstalled' else download_progress).encode()))
                self.assertIn(f'{installed_label}0.1.0 ({platform})'.encode(), output)
                self.assertNotIn(b'configuration format OK', output)
                self.assertIn('安装前检查：未检测到正在运行的 Agent 进程。'.encode(), output)
                self.assertIn('运行方式：前台'.encode(), output)
                self.assertIn(b'Ctrl+C', output)
                proxy_arg = f" --github-proxy='{github_proxy.rstrip('/')}'" if github_proxy else ""
                self.assertIn(f"| sh -s -- install{proxy_arg} --foreground".encode(), output)
                self.assertIn(b'"$HOME/.viorka-agent/viorka-agent" run --config "$HOME/.viorka-agent/agent.json"', output)
                self.assertNotIn(b'--token=', output)
                self.assertNotIn(b'systemctl', output)
                self.assertNotIn(b'rc-service', output)
                self.assertEqual(calls, ["config-check", "register", "run"])
                config = json.loads((install / "agent.json").read_text())
                self.assertEqual(config["state_dir"], str(install.resolve() / "state"))
                self.assertEqual(config["panel_url"], "https://localhost:8143")
                self.assertEqual(install.stat().st_mode & 0o777, 0o755 if failure == "existing" else 0o700)
                self.assertEqual((install / "agent.json").stat().st_mode & 0o777, 0o600)
                self.assertEqual((install / "state").stat().st_mode & 0o777, 0o700)
            else:
                self.assertNotEqual(code, 0, output.decode())
                self.assertNotIn(installed_label.encode(), output)
                self.assertNotIn("run", calls)
                if failure not in ("config", "enroll"):
                    self.assertEqual(calls, [])
                if failure in ("no_release", "invalid_release") or (failure == "download" and not pinned):
                    self.assertFalse(install.exists(), "release lookup must not leave an installation directory")
                if failure == "download":
                    self.assertIn((download_progress if pinned else release_progress).encode(), output)
                    self.assertNotIn('[安装中] 正在下载 SHA-256 校验文件…'.encode(), output)
                if failure == "legacy":
                    self.assertFalse(install.exists())
                    self.assertEqual((legacy / "agent.json").read_text(), "preserve")
                if failure == "no_release":
                    self.assertIn('草稿不可下载'.encode(), output)
                    self.assertIn(b'--version=', output)
                if failure == "config":
                    self.assertEqual(calls, ["config-check"])
                    self.assertIn(b'invalid configuration: fixture error', output)
                    self.assertFalse((install / "agent.json").exists())
                    self.assertFalse((install / "state").exists())
                if failure == "enroll":
                    self.assertEqual(calls, ["config-check", "register"])
                if failure in ("checksum", "manifest"):
                    self.assertFalse(list(install.glob(".install-*")))
                    self.assertFalse((install / "viorka-agent").exists())
                if failure == "existing":
                    self.assertEqual((install / "identity").read_text(), "preserve")

            if retry:
                before = (install / "state/identity.json").read_bytes() if (install / "state/identity.json").exists() else None
                config = (install / "agent.json").read_bytes() if (install / "agent.json").exists() else None
                env = dict(os.environ, HOME=str(home), PATH=f"{bin_dir}:{os.environ['PATH']}", FAILURE=retry_failure, PAYLOAD=str(payload), CURRENT_PROBE=current_probe)
                if replacement:
                    env["NODE_ID"] = "node-2"
                    (install / "state/old-journal.json").write_text("old node history")
                retry_script = ROOT / "install.sh"
                if running:
                    proc = pathlib.Path(tmp) / "proc/123"
                    proc.mkdir(parents=True)
                    (proc / "exe").symlink_to(install.resolve() / "viorka-agent")
                    retry_script = pathlib.Path(tmp) / "install.sh"
                    retry_script.write_text((ROOT / "install.sh").read_text().replace('/proc/', str(pathlib.Path(tmp) / 'proc') + '/'))
                old_binary = (install / "viorka-agent").read_bytes() if (install / "viorka-agent").exists() else None
                agent = install / "viorka-agent"
                old_inode = agent.stat().st_ino if agent.exists() else None
                log = payload.with_name("download-urls")
                log.write_text("")
                held_lock = None
                if locked_state:
                    held_lock = (install / "state/agent.lock").open("a")
                    fcntl.flock(held_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                retry_args = ["sh", str(retry_script), "install", "--foreground", "--url=https://localhost:8143", "--version=v0.1.0", *((["--token=" + TOKEN, "--reenroll-node=node-1"] if reenroll else (["--token=" + TOKEN] if replacement else [])) if before and failure != "enroll" else ["--token=" + TOKEN])]
                if retry_omit_url:
                    retry_args.remove("--url=https://localhost:8143")
                if retry_latest:
                    retry_args.remove("--version=v0.1.0")
                if choice is None:
                    result = subprocess.run(retry_args, env=env, capture_output=True, start_new_session=True, timeout=15)
                else:
                    pid, fd = pty.fork()
                    if pid == 0:
                        os.environ.update(env)
                        script_fd = os.open(retry_script, os.O_RDONLY)
                        os.dup2(script_fd, 0)
                        os.close(script_fd)
                        os.execvp("sh", ["sh", "-s", "--", *retry_args[2:]])
                    output = b""
                    status = None
                    sent = False
                    try:
                        end = time.monotonic() + 15
                        while time.monotonic() < end:
                            if select.select([fd], [], [], .05)[0]:
                                try:
                                    output += os.read(fd, 65536)
                                except OSError:
                                    pass
                                if not sent and "默认 2]：".encode() in output:
                                    os.write(fd, (choice + "\n").encode())
                                    sent = True
                            done, value = os.waitpid(pid, os.WNOHANG)
                            if done:
                                status = value
                                break
                        self.assertIsNotNone(status, "confirmation timed out")
                    finally:
                        if status is None:
                            os.kill(pid, signal.SIGKILL)
                            os.waitpid(pid, 0)
                        os.close(fd)
                    result = SimpleNamespace(returncode=os.waitstatus_to_exitcode(status), stdout=output, stderr=output)
                if held_lock:
                    held_lock.close()
                replaced = replacement and choice == "1" and not retry_failure and not running and not locked_state
                self.assertEqual(result.returncode == 0, replaced or not (retry_failure or replacement), result.stderr.decode())
                if old_binary is not None and retry_failure != "missing_url":
                    current = {
                        "": f'0.1.0 ({platform})',
                        "older": '0.0.9 (linux/amd64)',
                        "other_arch": '0.1.0 (linux/arm64)',
                    }.get(current_probe, '无法读取，将继续安装。')
                    self.assertIn(f'{current_label}{current}'.encode(), result.stdout)
                if result.returncode == 0:
                    self.assertIn(f'{installed_label}0.1.0 ({platform})'.encode(), result.stdout)
                else:
                    self.assertNotIn(installed_label.encode(), result.stdout)
                if result.returncode == 0 and old_inode is not None:
                    urls = log.read_text().splitlines()
                    if current_probe:
                        self.assertEqual(len([url for url in urls if '/download/' in url]), 2)
                    else:
                        self.assertEqual(urls, ["https://github.com/vioira/viorka-agent/releases/latest"] if retry_latest else [])
                        self.assertIn(skip_progress.encode(), result.stdout)
                        self.assertEqual(agent.stat().st_ino, old_inode)
                        self.assertFalse((install / "viorka-agent.previous").exists())
                if running and not retry_failure and not replacement:
                    self.assertIn('安装前检查：检测到 Agent 进程正在运行。'.encode(), result.stdout)
                    self.assertIn('常用命令'.encode(), result.stdout)
                after_calls = (install / "calls").read_text().splitlines()[len(calls):]
                expected = ["config-check"] if running else (["config-check", "run"] if before and failure != "enroll" and not reenroll else ["config-check", "register", "run"])
                if replacement:
                    expected = ["config-check"] if choice == "1" else []
                    if choice is None and not retry_omit_url:
                        self.assertIn("没有交互终端".encode(), result.stderr)
                    elif choice != "1" and not retry_omit_url:
                        self.assertIn("已退出".encode(), result.stderr)
                    if replaced:
                        expected = ["config-check", "register", "run"]
                        backups = list(install.glob("retired-node-*"))
                        self.assertEqual(len(backups), 1)
                        self.assertEqual((backups[0] / "state/identity.json").read_bytes(), before)
                        self.assertEqual((backups[0] / "agent.json").read_bytes(), config)
                        self.assertEqual(backups[0].stat().st_mode & 0o777, 0o700)
                        self.assertEqual((backups[0] / "state/old-journal.json").read_text(), "old node history")
                        self.assertFalse((install / "state/old-journal.json").exists())
                        self.assertEqual(json.loads((install / "state/identity.json").read_text())["node_id"], "node-2")
                    self.assertEqual((install / "viorka-agent").read_bytes(), old_binary)
                    self.assertNotIn(TOKEN.encode(), result.stdout + result.stderr)
                if retry_failure:
                    expected = ["config-check"] if retry_failure == "config" else []
                    if replacement and retry_failure == "enroll":
                        expected = ["config-check", "register"]
                        backups = list(install.glob("retired-node-*"))
                        self.assertEqual(len(backups), 1)
                        self.assertEqual((backups[0] / "state/identity.json").read_bytes(), before)
                        self.assertEqual(json.loads((install / "state/identity.json").read_text())["node_id"], "")
                        self.assertIn("重新安装未完成".encode(), result.stderr)
                    self.assertEqual((install / "viorka-agent").read_bytes(), old_binary)
                self.assertEqual(after_calls, expected)
                if before and failure == "enroll" and not retry_failure:
                    self.assertIn("检测到未完成注册".encode(), result.stdout)
                    self.assertEqual(json.loads((install / "state/identity.json").read_bytes())["private_key"], json.loads(before)["private_key"])
                if before and failure != "enroll" and not replaced and not (replacement and retry_failure == "enroll"):
                    self.assertEqual((install / "state/identity.json").read_bytes(), before)
                if config:
                    self.assertEqual((install / "agent.json").read_bytes(), config)
                self.assertFalse(list(install.glob(".install-*")))

    def test_update_without_url_or_token(self):
        self.run_install(token_arg=True, retry=True, retry_omit_url=True)
        self.run_install(token_arg=True, retry=True, retry_omit_url=True, running=True)
        for failure in ("download", "checksum", "config"):
            with self.subTest(failure=failure):
                self.run_install(token_arg=True, retry=True, retry_omit_url=True, retry_failure=failure,
                                 current_probe="" if failure == "config" else "older")

    def test_current_version_printed_when_latest_lookup_fails(self):
        self.run_install(token_arg=True, retry=True, retry_omit_url=True, retry_latest=True, retry_failure="download")

    def test_first_registration_reuses_matching_binary(self):
        self.run_install(failure="preinstalled", pinned=True, token_stdin=True, expected_downloads=[])

    def test_mismatched_or_unreadable_current_version_downloads_release(self):
        for probe in ("older", "other_arch", "unavailable", "oversized", "multiline"):
            with self.subTest(probe=probe):
                self.run_install(token_arg=True, retry=True, retry_omit_url=True, current_probe=probe)

    def test_empty_release_tag_is_rejected(self):
        self.run_install(extra=("--version=",), token_arg=True)

    def test_github_proxy_pins_binary_and_checksum_to_resolved_release(self):
        repo = "https://github.com/vioira/viorka-agent/releases"
        prefix = "https://mirror.example:8443/github"
        asset = f"{prefix}/{repo}/download/v0.1.0/viorka-agent-linux-amd64"
        self.run_install(github_proxy=prefix + "///", token_stdin=True,
                         expected_downloads=[f"{prefix}/{repo}/latest", asset, asset + ".sha256"])
        self.run_install(github_proxy=prefix, pinned=True, token_stdin=True,
                         expected_downloads=[asset, asset + ".sha256"])
        self.run_install(failure="checksum", github_proxy=prefix, pinned=True, token_stdin=True,
                         expected_downloads=[asset, asset + ".sha256"])

    def test_github_proxy_resolves_latest_via_metadata_when_no_redirect(self):
        prefix = "https://api-proxy.example"
        repo = "https://github.com/vioira/viorka-agent/releases"
        queries = [f"{prefix}/{repo}/latest",
                   f"{prefix}/https://api.github.com/repos/vioira/viorka-agent/releases/latest"]
        asset = f"{prefix}/{repo}/download/v0.1.0/viorka-agent-linux-arm64"
        self.run_install(github_proxy=prefix, arch="aarch64", token_stdin=True,
                         expected_downloads=[*queries, asset, asset + ".sha256"])
        self.run_install(failure="bad_proxy_metadata", github_proxy=prefix, token_stdin=True,
                         expected_downloads=queries)

    def test_invalid_github_proxies_fail_before_download(self):
        for proxy in ("", "http://proxy.example", "https://user:pass@proxy.example",
                      "https://proxy.example/?url=", "https://proxy.example/#frag",
                      "https://proxy.example/'$(id)'", "https://proxy.example/\n"):
            with self.subTest(proxy=proxy):
                self.run_install(extra=(f"--github-proxy={proxy}",), token_stdin=True,
                                 expected_downloads=[])

    def test_missing_url_requires_registered_install(self):
        self.run_install(failure="missing_url", token_arg=True, omit_url=True)
        self.run_install(failure="missing_config", token_arg=True, omit_url=True)
        self.run_install(failure="enroll", token_arg=True, retry=True,
                         retry_omit_url=True, retry_failure="missing_url")
        self.run_install(token_arg=True, retry=True, replacement=True,
                         retry_omit_url=True, retry_failure="missing_url")

    def test_new_token_does_not_silently_reuse_old_identity(self):
        self.run_install(token_arg=True, retry=True, replacement=True)

    def test_reinstall_confirmation(self):
        for choice in ("1", "2", "", "invalid"):
            with self.subTest(choice=choice):
                self.run_install(token_arg=True, retry=True, replacement=True, choice=choice)

    def test_reinstall_preserves_data_when_locked_or_download_invalid(self):
        self.run_install(token_arg=True, retry=True, replacement=True, choice="1", locked_state=True)
        for failure in ("checksum", "config", "enroll"):
            self.run_install(token_arg=True, retry=True, replacement=True, choice="1", retry_failure=failure,
                             current_probe="older" if failure == "checksum" else "")

    def test_reinstall_rejects_running_foreground(self):
        self.run_install(token_arg=True, retry=True, replacement=True, choice="1", running=True)

    def test_explicit_reenrollment_preserves_config(self):
        self.run_install(token_arg=True, retry=True, reenroll=True, retry_latest=True)

    def test_running_agent_does_not_start_duplicate(self):
        self.run_install(token_arg=True, retry=True, running=True)

    def test_failed_update_preserves_install(self):
        for failure in ("download", "checksum", "manifest", "missing_checksum", "version", "platform", "version_timeout", "config"):
            with self.subTest(failure=failure):
                self.run_install(token_arg=True, retry=True, retry_failure=failure, current_probe="older")

    def test_pinned_download_retry(self):
        self.run_install(failure="download", pinned=True, token_arg=True, retry=True)

    def test_retry_and_update(self):
        for failure in ("", "existing", "download", "checksum", "config", "enroll"):
            with self.subTest(failure=failure):
                self.run_install(failure=failure, token_arg=True, retry=True)

    def test_install_and_permissions(self):
        for arch in ("x86_64", "aarch64"):
            with self.subTest(arch=arch):
                self.run_install(arch=arch, pinned=arch == "aarch64")

    def test_batch_registration_rejects_unsafe_combinations(self):
        self.run_install(extra=("--registration",), token_arg=True)
        self.run_install(registration=True, extra=("--reenroll-node=node",))

    def test_batch_registration_terminal_and_stdin(self):
        self.run_install(registration=True)
        self.run_install(registration=True, token_stdin=True)
        self.run_install(registration=True, token_stdin=True, failure="enroll")

    def test_token_argument_without_terminal(self):
        self.run_install(token_arg=True)
        self.run_install(token_arg=True, failure="enroll")

    def test_failures(self):
        for failure in ("legacy", "existing", "symlink", "locked", "missing_config", "no_release", "invalid_release", "download", "checksum", "manifest", "config", "enroll"):
            with self.subTest(failure=failure):
                self.run_install(failure=failure)
        self.run_install(arch="riscv64")

    def test_bad_arguments(self):
        for arg in ("--url=http://localhost", "--url=https://host/path", "--url=https://user:pass@host", "--url=https://host\nBAD", "--url=https://host'$(id)'", "--auto_update=1", "-secret=do-not-log", "--version=../../bad", "--version=v01.0.0", "--version=v1.0", "--version=v1.0.0-beta.01", "--token=", "--token=short", "--token=" + "g" * 64, "--token=" + "a" * 63 + "\'"):
            with self.subTest(arg=arg):
                self.run_install(extra=(arg,))


if __name__ == "__main__":
    if not shutil.which("sha256sum"):
        sys.exit("sha256sum required")
    unittest.main()
