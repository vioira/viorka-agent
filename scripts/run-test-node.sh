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

test_node_config() {
    go run ./scripts/test-node-config "$@"
}

panel_url=${VIORKA_TEST_PANEL_URL:-}
if [[ -n "$panel_url" ]] && present "$config"; then
    saved_url=$(test_node_config panel "$config")
    if [[ "$saved_url" != "$panel_url" ]]; then
        [[ -z "$requested_dir" ]] || fail '指定目录的 Panel 地址与环境变量不符。请去掉 --test-dir 以切换 Panel。'
        default_url=
        if present "$workspace/agent.json"; then
            check_file "$workspace/agent.json" 0022
            default_url=$(test_node_config panel "$workspace/agent.json")
        fi
        if [[ "$default_url" == "$panel_url" ]]; then
            test_dir=$workspace
        else
            panel_key=$(test_node_config panel-key "$panel_url")
            test_dir=$workspace/panel-$panel_key
            mkdir -p -- "$test_dir"
            check_directory "$test_dir"
        fi
        config=$test_dir/agent.json
        if present "$config"; then
            check_file "$config" 0022
            saved_url=$(test_node_config panel "$config")
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
remember_directory() {
    printf '%s\n' "$test_dir" > "$staging/active-dir"
    mv -f -- "$staging/active-dir" "$selection"
}

register_node() (
    unset token
    local prompt=$1
    shift
    IFS= read -rs -p "$prompt" token </dev/tty || fail '注册需要可隐藏令牌输入的交互终端。'
    printf '\n' >&2
    printf '%s' "$token" | "$binary" register --token-stdin "$@"
)

remember_directory
mv -f -- "$agent" "$binary"
case "$status" in
    needs-registration) register_node '请输入 Panel 注册令牌：' --config "$config" ;;
    registered) ;;
    *) fail 'Agent 返回了未知的注册状态。' ;;
esac

# Forward signals sent to the script PID while the Agent owns the state lock.
child=
stop_run() {
    trap '' INT TERM
    if [[ -n "$child" ]]; then
        kill -TERM "$child" 2>/dev/null || :
        wait "$child" || :
    fi
    exit 0
}

while :; do
    trap stop_run INT TERM
    "$binary" run --config "$config" &
    child=$!
    rc=0
    wait "$child" || rc=$?
    child=
    trap - INT TERM
    [[ "$rc" == 77 ]] || exit "$rc"

    info=$(test_node_config identity "$config")
    current_panel=${info%%$'\n'*}
    node_id=${info#*$'\n'}
    printf '认证失败（HTTP 401）\nPanel 地址：%s\n节点 ID：%s\n' "$current_panel" "$node_id" >&2
    [[ -t 0 ]] || { printf '请在交互终端选择恢复操作。\n' >&2; exit "$rc"; }
    while :; do
        IFS= read -r -p '1) 重试连接  2) 重新注册原节点  3) 注册新节点  0) 退出：' action || exit "$rc"
        case "$action" in
            0) exit "$rc" ;;
            1) continue 2 ;;
            2|3) break ;;
        esac
    done
    IFS= read -r -p "Panel 地址（直接回车使用 ${current_panel}）：" panel_url || exit "$rc"
    panel_url=${panel_url:-$current_panel}
    [[ "$panel_url" == *://* ]] || panel_url="https://$panel_url"
    if [[ "$action" == 2 ]]; then
        register_node "请输入目标 Panel 为原节点 $node_id 重新签发的令牌：" \
            --config "$config" --replace-node "$node_id" --panel-origin "$panel_url"
    else
        next_dir=$(mktemp -d "$workspace/node-XXXXXX")
        printf '新节点测试目录：%s\n' "$next_dir"
        next_config=$next_dir/agent.json
        (set -C; test_node_config new-config "$config" "$panel_url" "$next_dir/state" > "$next_config")
        "$binary" config-check --config "$next_config"
        register_node '请输入目标 Panel 的新节点注册令牌：' --config "$next_config"
        test_dir=$next_dir
        config=$next_config
        remember_directory
    fi
done
