#!/usr/bin/env python3
"""Interactively prepare a version commit and push a release tag."""
import argparse
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[1]
VERSION_FILE = "internal/version/version.go"
DECLARATION = re.compile(r'^const Version = "([^"]+)"$', re.MULTILINE)
SEMVER = re.compile(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?")


def style(text, tone="bold", *, stream=None):
    stream = sys.stdout if stream is None else stream
    if not stream.isatty() or os.environ.get("TERM") == "dumb" or "NO_COLOR" in os.environ:
        return text
    codes = {"bold": "1", "muted": "2", "version": "1;36",
             "success": "32", "warning": "33", "error": "31"}
    return f"\033[{codes[tone]}m{text}\033[0m"


def heading(title):
    print("\n" + style(f"── {title} ──"), flush=True)


def version_label(value):
    return style("v" + value.removeprefix("v"), "version")


def run(*args, capture=False, env=None, log=None):
    result = subprocess.run(args, cwd=ROOT, check=True, text=True,
                            stdout=log if log is not None else subprocess.PIPE if capture else None,
                            stderr=subprocess.STDOUT if log is not None else None,
                            env=env)
    return result.stdout.strip() if capture else None


def run_stage(label, *args, env=None):
    """Keep test subprocess chatter off the release screen; retain failures."""
    print(style(f"  {label}  检查中…", "muted"), flush=True)
    started = time.monotonic()
    with tempfile.NamedTemporaryFile(prefix="viorka-release-", suffix=".log", delete=False) as log:
        path = Path(log.name)
        try:
            run(*args, env=env, log=log)
        except (OSError, subprocess.CalledProcessError, KeyboardInterrupt):
            log.flush()
            print(style(f"  ✗ {label} 未完成，发布已停止。", "error", stream=sys.stderr),
                  file=sys.stderr, flush=True)
            with path.open('rb') as saved:
                saved.seek(max(0, path.stat().st_size - 8192))
                tail = saved.read().decode('utf-8', errors='replace').splitlines()[-30:]
            if tail:
                print("以下为日志末尾（最多 30 行）：", file=sys.stderr)
                print('\n'.join(tail), file=sys.stderr)
            print(f"完整日志：{path}", file=sys.stderr, flush=True)
            raise
    path.unlink()
    print(style(f"  ✓ {label}  通过", "success") +
          style(f"  {time.monotonic() - started:.1f} 秒", "muted"), flush=True)


def git(*args):
    return run("git", *args, capture=True)


def normalize_version(value):
    value = value.removeprefix("v")
    match = SEMVER.fullmatch(value)
    if not match or (match[4] and any(
        part.isdigit() and len(part) > 1 and part.startswith("0")
        for part in match[4].split(".")
    )):
        raise ValueError("版本须为 SemVer，例如 0.1.0-bate.1 或 1.0.0")
    return value


def ensure_tag_available(remote, tag):
    if git("tag", "--list", tag):
        raise ValueError(f"本地标签已存在：{tag}")
    if git("ls-remote", "--tags", remote, f"refs/tags/{tag}"):
        raise ValueError(f"远端标签已存在：{tag}")


def version_key(value):
    """SemVer precedence; build metadata does not affect ordering."""
    match = SEMVER.fullmatch(normalize_version(value))
    prerelease = match[4]
    identifiers = tuple((0, int(part)) if part.isdigit() else (1, part)
                        for part in prerelease.split('.')) if prerelease else ()
    return tuple(int(match[i]) for i in (1, 2, 3)) + (prerelease is None, identifiers)


def choose_version(current):
    current = normalize_version(current)
    match = SEMVER.fullmatch(current)
    major, minor, patch = (int(match[i]) for i in (1, 2, 3))
    base = f"{major}.{minor}.{patch}"
    stage = match[4]
    if stage:
        parts = stage.split('.')
        if parts[-1].isdigit():
            parts[-1] = str(int(parts[-1]) + 1)
        else:
            parts.append('1')
        preview = base + '-' + '.'.join(parts)
    else:
        preview = f"{major}.{minor}.{patch + 1}-bate.1"
    choices = {'1': f"{major}.{minor}.{patch + 1}",
               '2': f"{major}.{minor + 1}.0", '3': f"{major + 1}.0.0", '5': current}
    heading("选择发布版本")
    print(f"  当前版本  {version_label(current)}\n")
    for choice, label in [('1', '补丁版本'), ('2', '次版本'), ('3', '主版本')]:
        spacing = " " * (14 - len(label) * 2)
        default_hint = "  " + style("[默认]", "muted") if choice == '1' else ""
        print(f"  {style(choice + ')')} {label}{spacing}{version_label(choices[choice])}{default_hint}")
    print(f"  {style('4)')} 自定义版本\n"
          f"  {style('5)')} 发布当前版本  {version_label(current)}")
    print(f"  {style('6)')} 预发布版本    {version_label(preview)}\n"
          f"  {style('7)')} 转为正式版\n"
          f"  {style('0)', 'muted')} " + style("取消", "muted"))
    choice = input("\n" + style("请选择 [1]: ")).strip() or '1'
    if choice == '0':
        return None
    if choice == '4':
        selected = input("输入版本号（可带 v 前缀）: ").strip()
    elif choice == '6':
        selected = input(f"输入预发布版本 [{version_label(preview)}]: ").strip() or preview
    elif choice == '7':
        if not stage:
            raise ValueError("当前已是正式版本")
        selected = base
    elif choice in choices:
        selected = choices[choice]
    else:
        raise ValueError("无效菜单选项")
    selected = normalize_version(selected)
    if choice == '6' and SEMVER.fullmatch(selected)[4] is None:
        raise ValueError("请选择含预发布标识的版本，例如 0.1.0-bate.2")
    if selected != current and version_key(selected) <= version_key(current):
        raise ValueError("新版本必须高于当前版本（构建元数据不改变版本优先级）")
    return selected


def sync_remote(remote, branch):
    # Fetch the configured push destination too: a separate push URL must not
    # bypass branch/tag checks performed against a different fetch destination.
    urls = git('remote', 'get-url', '--push', '--all', remote).splitlines()
    if len(urls) != 1 or urls[0] != git('remote', 'get-url', remote):
        raise ValueError("发布要求远端只有一个且与 fetch URL 相同的 push URL")
    run('git', 'fetch', '--prune', '--tags', remote,
        f'+refs/heads/*:refs/remotes/{remote}/*')
    ref = f'refs/remotes/{remote}/{branch}'
    if not git('branch', '-r', '--list', f'{remote}/{branch}'):
        print(f"远端分支 {remote}/{branch} 不存在，将创建该分支。")
        return None
    try:
        git('merge-base', '--is-ancestor', ref, 'HEAD')
    except subprocess.CalledProcessError as exc:
        raise ValueError(f"本地分支落后或已与 {remote}/{branch} 分叉，请先同步") from exc
    return ref


def ensure_newer_than_tags(version):
    for tag in git('tag', '--list', 'v*').splitlines():
        try:
            existing = normalize_version(tag)
        except ValueError:
            continue
        if version_key(version) <= version_key(existing):
            raise ValueError(f"发布版本必须高于已有版本 {tag}")


def main():
    if git("status", "--porcelain"):
        raise ValueError("工作区不干净，请先提交或自行保存改动（包括未跟踪文件）")
    branch = git("symbolic-ref", "--quiet", "--short", "HEAD")
    if branch != 'main':
        raise ValueError("请切换到 main 分支发布")
    head = git("rev-parse", "HEAD")
    path = ROOT / VERSION_FILE
    original = path.read_text()
    declarations = DECLARATION.findall(original)
    if len(declarations) != 1:
        raise ValueError("未找到唯一版本声明")
    current = declarations[0]
    remotes = git("remote").splitlines()
    if not remotes:
        raise ValueError("未配置 Git 远端，请先添加发布远端")
    if len(remotes) == 1:
        remote = remotes[0]
    else:
        default = "origin" if "origin" in remotes else remotes[0]
        print("可用远端：", ", ".join(remotes))
        remote = input(f"推送远端 [{default}]: ").strip() or default
    if remote not in remotes or remote.startswith("-"):
        raise ValueError("请选择已配置的 Git 远端")
    remote_ref = sync_remote(remote, branch)
    version = choose_version(current)
    if version is None:
        print(style("已取消，未修改版本、提交、标签或推送。", "muted"))
        return
    tag = f"v{version}"
    ensure_tag_available(remote, tag)
    ensure_newer_than_tags(version)
    updated = DECLARATION.sub(f'const Version = "{version}"', original)
    path.write_text(updated)
    heading("发布前检查")
    print(style("  详细日志仅在失败时展示。", "muted"), flush=True)
    run_stage("[1/3] 完整验证", "sh", "scripts/validate.sh")
    with tempfile.TemporaryDirectory(prefix="viorka-release-") as directory:
        for step, arch in enumerate(("amd64", "arm64"), start=2):
            env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH=arch)
            run_stage(f"[{step}/3] Linux {arch} 构建", "go", "build", "-trimpath",
                      "-o", str(Path(directory) / arch), "./cmd/viorka-agent", env=env)
    print(style("\n  ✓ 全部检查通过", "success") +
          style("，尚未提交、打标签或推送。", "muted"), flush=True)
    if updated != original:
        heading("版本文件变更")
        run("git", "--no-pager", "diff", "--", VERSION_FILE)
    heading("发布确认")
    display_version = version_label(version) if current == version else f"{version_label(current)} → {version_label(version)}"
    print(f"  版本  {display_version}")
    prerelease = SEMVER.fullmatch(version)[4] is not None
    print("  类型  " + style("预发布（不替换 Latest）" if prerelease else "正式版",
                              "warning" if prerelease else "success"))
    print(f"  目标  {style(remote + '/' + branch)}")
    print(f"  标签  {version_label(version)}")
    print(style("  推送后由 GitHub Actions 构建并创建 Release 草稿。", "muted"))
    print("\n  待推送提交：")
    commits = git('--no-pager', 'log', '--format=%h %s',
                  f'{remote_ref}..HEAD' if remote_ref else 'HEAD')
    for line in commits.splitlines():
        print(f"    {line}")
    if not commits:
        print(style("    无", "muted"))
    if updated != original:
        print(style(f"    + 版本提交 chore: release {tag}（确认后创建）", "muted"))
    if input("\n" + style("确认提交、打标签并推送？") + " " + style("[y/N]", "warning") + ": ").strip().lower() != "y":
        print(style("已取消；版本文件改动保留，未提交、未创建标签、未推送。", "muted"))
        return
    if git("rev-parse", "HEAD") != head or git("symbolic-ref", "--short", "HEAD") != branch:
        raise ValueError("验证期间 HEAD 或分支发生变化，请重新执行")
    expected_status = f"M {VERSION_FILE}" if updated != original else ""
    if git("status", "--porcelain") != expected_status or path.read_text() != updated:
        raise ValueError("验证期间工作区发生额外变化，请重新检查")
    ensure_tag_available(remote, tag)
    sync_remote(remote, branch)
    ensure_newer_than_tags(version)
    if updated != original:
        run("git", "add", "--", VERSION_FILE)
        run("git", "commit", "-m", f"chore: release {tag}")
    if git("status", "--porcelain") or path.read_text() != updated:
        raise ValueError("提交后工作区或版本变化，请检查后重新发布")
    run("git", "tag", "-a", tag, "-m", f"Release {tag}")
    push = ["git", "push", "--atomic", remote, f"refs/heads/{branch}", f"refs/tags/{tag}"]
    try:
        run(*push)
    except subprocess.CalledProcessError:
        print("推送失败，本地提交和标签已保留；排查后重试：", file=sys.stderr)
        print(shlex.join(push), file=sys.stderr)
        raise
    print(style("\n✓ 已推送 ", "success") + version_label(version))
    print("  请在 GitHub Actions 查看构建结果，审核 Release 草稿后发布。")


if __name__ == "__main__":
    argparse.ArgumentParser(description="交互选择版本、验证、提交并推送 main 和标签，触发 Release 草稿构建。",
                            epilog="请先提交开发改动。支持补丁/次版本/主版本、自定义、预发布递增及转正式版。").parse_args()
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError, EOFError, KeyboardInterrupt) as exc:
        print(style(f"发布未完成：{exc}。", "error", stream=sys.stderr), file=sys.stderr)
        print("已有本地改动、提交和标签会保留，不自动回滚。", file=sys.stderr)
        sys.exit(1)
