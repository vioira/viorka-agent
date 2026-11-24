#!/usr/bin/env bash
set +x
set -euo pipefail
umask 077

fail() { printf '%s\n' "$*" >&2; exit 1; }
present() { [[ -e "$1" || -L "$1" ]]; }

check_mode() {
    local mode
    mode=$(stat -c '%a' "$1" 2>/dev/null || stat -f '%Lp' "$1")
    [[ "$mode" =~ ^[0-7]{3,4}$ ]] && (( (8#$mode & $2) == 0 )) || fail "权限不安全：$1"
}

check_directory() {
    [[ -d "$1" && ! -L "$1" && -O "$1" ]] || fail "测试目录不存在或归属不安全：$1"
    check_mode "$1" 0077
}

check_file() {
    [[ -f "$1" && ! -L "$1" && -O "$1" ]] || fail "文件不存在或归属不安全：$1"
    check_mode "$1" "$2"
}

json_string() {
    [[ "$1" != *[[:cntrl:]]* ]] || fail '初始配置值不能包含控制字符。'
    local value=${1//\\/\\\\}
    value=${value//\"/\\\"}
    printf '"%s"' "$value"
}

usage() {
    printf '%s\n' '用法：bash scripts/run-test-node.sh [--test-dir /已有测试目录]' \
        '发现多个旧测试目录时，可按编号选择一个，或输入 0 新建测试目录。' \
        '首次创建配置时会提示输入 Panel 域名，也可预设 VIORKA_TEST_PANEL_URL 和 VIORKA_TEST_NODE_NAME。' \
        '已有配置时，指定不同的 VIORKA_TEST_PANEL_URL 将选择该地址的独立测试目录。'
}

requested_dir=
case "${1-}" in
    '') [[ $# -eq 0 ]] || fail '请使用 --help 查看用法。' ;;
    -h|--help) usage; exit 0 ;;
    --test-dir)
        [[ $# -eq 2 && "$2" = /* ]] || fail '--test-dir 需要一个已有测试目录的绝对路径。'
        requested_dir=${2%/}
        [[ -n "$requested_dir" ]] || fail '请选择 Agent 测试目录。'
        ;;
    *) fail '请使用 --help 查看用法。' ;;
esac

cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
if ! branch=$(git symbolic-ref --quiet --short HEAD) || [[ "$branch" != main ]]; then
    fail '请在 main 分支运行测试节点脚本。'
fi
[[ "${HOME-}" = /* && "$HOME" != *[[:cntrl:]]* ]] || fail 'HOME 必须是有效的绝对路径。'
workspace=${HOME%/}/.viorka-agent-test
selection=$workspace/active-dir
binary=$workspace/viorka-agent
mkdir -p -- "$workspace"
check_directory "$workspace"

new_dir=
if [[ -n "$requested_dir" ]]; then
    test_dir=$requested_dir
elif present "$selection"; then
    check_file "$selection" 0077
    IFS= read -r test_dir < "$selection" || fail "无法读取测试目录记录：$selection"
elif present "$workspace/agent.json"; then
    test_dir=$workspace
else
    test_dir=$workspace
    previous=()
    for candidate in "${HOME%/}"/viorka-agent-test.*; do
        if [[ -d "$candidate" ]] && present "$candidate/agent.json"; then
            previous+=("$candidate")
            test_dir=$candidate
        fi
    done
    if [[ "$test_dir" != "$workspace" && ${#previous[@]} -gt 1 ]]; then
        printf '发现多个旧测试目录：\n' >&2
        for i in "${!previous[@]}"; do
            printf '  %d) %q\n' "$((i + 1))" "${previous[i]}" >&2
        done
        printf '  0) 新建测试目录\n' >&2
        [[ -t 0 ]] || fail '请选择交互终端运行，或用 --test-dir 指定一个旧测试目录。'
        while IFS= read -r -p '请输入目录编号：' choice; do
            if [[ "$choice" == 0 ]]; then
                new_dir=$(mktemp -d "$workspace/node-XXXXXX")
                test_dir=$new_dir
                break
            fi
            for i in "${!previous[@]}"; do
                if [[ "$choice" == "$((i + 1))" ]]; then
                    requested_dir=${previous[i]}
                    test_dir=$requested_dir
                    break 2
                fi
            done
            printf '请输入列表中的编号。\n' >&2
        done
        [[ -n "$requested_dir" || -n "$new_dir" ]] || fail '已取消选择测试目录。'
    fi
fi

[[ "$test_dir" = /* && "$test_dir" != *[[:cntrl:]]* ]] || fail '测试目录路径无效。'
check_directory "$test_dir"
config=$test_dir/agent.json
if present "$config"; then
    check_file "$config" 0022
elif [[ -z "$new_dir" ]] && { [[ -n "$requested_dir" || "$test_dir" != "$workspace" ]] || present "$workspace/state"; }; then
    fail "现有测试目录缺少配置：$config"
fi

panel_url=${VIORKA_TEST_PANEL_URL:-}
if [[ -n "$panel_url" ]] && present "$config"; then
    command -v python3 >/dev/null || fail '按 Panel 地址选择已有测试配置需要 python3。'
    configured_panel() {
        python3 -c 'import json, sys
with open(sys.argv[1]) as f:
    print(json.load(f)["panel_url"])' "$1"
    }
    saved_url=$(configured_panel "$config")
    if [[ "$saved_url" != "$panel_url" ]]; then
        [[ -z "$requested_dir" ]] || fail '指定目录的 Panel 地址与环境变量不符。请去掉 --test-dir 以切换 Panel。'
        default_url=
        if present "$workspace/agent.json"; then
            check_file "$workspace/agent.json" 0022
            default_url=$(configured_panel "$workspace/agent.json")
        fi
        if [[ "$default_url" == "$panel_url" ]]; then
            test_dir=$workspace
        else
            panel_key=$(python3 -c 'import hashlib, os; print(hashlib.sha256(os.environ["VIORKA_TEST_PANEL_URL"].encode()).hexdigest())')
            test_dir=$workspace/panel-$panel_key
            mkdir -p -- "$test_dir"
            check_directory "$test_dir"
        fi
        config=$test_dir/agent.json
        if present "$config"; then
            check_file "$config" 0022
            saved_url=$(configured_panel "$config")
            [[ "$saved_url" == "$panel_url" ]] || fail '目标测试目录的 Panel 地址不匹配。'
        elif present "$test_dir/state"; then
            fail "现有测试目录缺少配置：$config"
        fi
    fi
fi

printf '测试目录：%s\n' "$test_dir"
git pull --ff-only origin main
staging=$(mktemp -d "$workspace/.build-XXXXXX")
trap 'rm -rf -- "$staging"' EXIT
agent=$staging/viorka-agent
go build -o "$agent" ./cmd/viorka-agent

if ! present "$config"; then
    if [[ -z "$panel_url" ]]; then
        if ! IFS= read -r -p '请输入 Panel 域名（可含 https://）：' panel_url </dev/tty; then
            fail '无法读取 Panel 域名，请设置 VIORKA_TEST_PANEL_URL 后重试。'
        fi
        [[ -n "$panel_url" ]] || fail 'Panel 域名不能为空。'
        [[ "$panel_url" == *://* ]] || panel_url="https://$panel_url"
    fi
    node_name=${VIORKA_TEST_NODE_NAME:-$(hostname)}
    {
        printf '{\n  "panel_url": '
        json_string "$panel_url"
        printf ',\n  "node_name": '
        json_string "$node_name"
        printf ',\n  "state_dir": '
        json_string "$test_dir/state"
        printf ',\n  "log_level": "info"\n}\n'
    } > "$staging/agent.json"
    "$agent" config-check --config "$staging/agent.json"
    ln -- "$staging/agent.json" "$config"
fi

status=$("$agent" config-check --config "$config" --registration-status)
if present "$selection"; then check_file "$selection" 0077; fi
if present "$binary"; then check_file "$binary" 0022; fi
printf '%s\n' "$test_dir" > "$staging/active-dir"
mv -f -- "$staging/active-dir" "$selection"
mv -f -- "$agent" "$binary"
case "$status" in
    needs-registration)
        unset token
        if ! IFS= read -rs -p '请输入 Panel 注册令牌：' token </dev/tty; then
            fail '注册需要可隐藏令牌输入的交互终端。'
        fi
        printf '\n' >&2
        printf '%s' "$token" | "$binary" register --config "$config" --token-stdin
        unset token
        ;;
    registered) ;;
    *) fail 'Agent 返回了未知的注册状态。' ;;
esac

rm -rf -- "$staging"
trap - EXIT
exec "$binary" run --config "$config"
