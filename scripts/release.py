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

ROOT = Path(__file__).resolve().parents[1]
VERSION_FILE = "internal/version/version.go"
DECLARATION = re.compile(r'^const Version = "([^"]+)"$', re.MULTILINE)
SEMVER = re.compile(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?")


def run(*args, capture=False, env=None):
    result = subprocess.run(args, cwd=ROOT, check=True, text=True,
                            stdout=subprocess.PIPE if capture else None,
                            env=env)
    return result.stdout.strip() if capture else None


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
    print(f"\n当前版本：{current}")
    for choice, label in [('1', '补丁版本'), ('2', '次版本'), ('3', '主版本')]:
        print(f"  {choice}) {label} {choices[choice]}")
    print(f"  4) 自定义版本\n  5) 发布当前版本 {current}\n"
          f"  6) 预发布版本（默认 {preview}）\n  7) 当前预发布转正式版\n  0) 取消")
    choice = input("请选择 [5]: ").strip() or '5'
    if choice == '0':
        return None
    if choice == '4':
        selected = input("输入版本号（可带 v 前缀）: ").strip()
    elif choice == '6':
        selected = input(f"输入预发布版本 [{preview}]: ").strip() or preview
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
    print("可用远端：", ", ".join(remotes))
    remote = input("推送远端 [origin]: ").strip() or "origin"
    if remote not in remotes or remote.startswith("-"):
        raise ValueError("请选择已配置的 Git 远端")
    remote_ref = sync_remote(remote, branch)
    version = choose_version(current)
    if version is None:
        print("已取消，未修改版本、提交、标签或推送。")
        return
    tag = f"v{version}"
    ensure_tag_available(remote, tag)
    ensure_newer_than_tags(version)
    updated = DECLARATION.sub(f'const Version = "{version}"', original)
    path.write_text(updated)
    print("执行完整验证和 Linux 双架构构建…", flush=True)
    run("sh", "scripts/validate.sh")
    with tempfile.TemporaryDirectory(prefix="viorka-release-") as directory:
        for arch in ("amd64", "arm64"):
            env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH=arch)
            run("go", "build", "-trimpath", "-o", str(Path(directory) / arch),
                "./cmd/viorka-agent", env=env)
    run("git", "diff", "--", VERSION_FILE)
    print(f"将发布 {tag}，推送当前分支 {branch} 及标签到 {remote}。")
    print("GitHub Actions 会创建 Release 草稿；含预发布标识的版本标为 prerelease。")
    print("推送包含当前分支上尚未推送的提交。")
    run('git', '--no-pager', 'log', '--oneline',
        f'{remote_ref}..HEAD' if remote_ref else 'HEAD')
    print(f"版本：{current} → {version}")
    print("类型：" + ("预发布（不替换 Latest）" if SEMVER.fullmatch(version)[4] else "正式版"))
    if input(f"输入 {tag} 确认提交、打标签并推送（回车取消）: ").strip() != tag:
        print("已取消；版本文件改动保留，未提交、未创建标签、未推送。")
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
    print(f"已推送 {tag}；请在 GitHub Actions 查看构建结果，审核 Release 草稿后发布。")


if __name__ == "__main__":
    argparse.ArgumentParser(description="交互选择版本、验证、提交并推送 main 和标签，触发 Release 草稿构建。",
                            epilog="请先提交开发改动。支持补丁/次版本/主版本、自定义、预发布递增及转正式版。").parse_args()
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError, EOFError, KeyboardInterrupt) as exc:
        print(f"发布未完成：{exc}。已有本地改动、提交和标签会保留，不自动回滚。", file=sys.stderr)
        sys.exit(1)
