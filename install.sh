#!/bin/sh
# Keep all work inside main: a truncated download must not start an installation.
main() {
    set +x
    set -eu
    umask 077
    export LC_ALL=C
    fail() { printf '%s\n' "$1" >&2; exit 1; }
    plain_text() {
        case "$1" in *'
'*) fail '参数或路径不能包含换行。' ;; esac
        if printf '%s' "$1" | grep -q '[[:cntrl:]]'; then fail '参数或路径不能包含控制字符。'; fi
    }
    download() {
        curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
            --tlsv1.2 --connect-timeout 15 --max-time 180 "$@"
    }
    json_string() {
        # Paths containing control characters are rejected before reaching here.
        printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
    }
    validate_token() {
        [ "${#enrollment_token}" -eq 64 ] || fail '注册令牌格式无效。'
        case "$enrollment_token" in *[!a-f0-9]*) fail '注册令牌格式无效。' ;; esac
    }
    [ "${1:-}" = install ] || fail '用法：sh install.sh install --url=https://panel.example.com [--token=TOKEN] [--version=vX.Y.Z] [--ca-file=/path/ca.pem]'
    shift
    panel_url= release_tag= ca_file= enrollment_token= token_supplied=0
    for arg do
        plain_text "$arg"
        case "$arg" in
            --url=*|-url=*) panel_url=${arg#*=} ;;
            --version=*) release_tag=${arg#*=} ;;
            --ca-file=*) ca_file=${arg#*=} ;;
            --token=*)
                [ "$token_supplied" -eq 0 ] || fail '请勿重复指定 --token。'
                enrollment_token=${arg#*=}
                token_supplied=1 ;;
            *) fail '不支持的参数。仅支持 --url、--token、--version、--ca-file；自动更新尚未实现。' ;;
        esac
    done
    unset arg
    set --
    if [ "$token_supplied" -eq 1 ]; then validate_token; fi
    # Only an HTTPS origin, never a WebSocket path or credentials.
    printf '%s\n' "$panel_url" | grep -Eq '^https://([A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?|\[[0-9A-Fa-f:]+\])(:[0-9]{1,5})?$' || fail '请提供 HTTPS Origin，不含路径、凭证、查询或片段。'
    if [ -n "$release_tag" ]; then
        printf '%s\n' "$release_tag" | grep -Eq '^v[0-9][0-9A-Za-z._-]*$' || fail '版本格式无效。'
    fi
    [ "$(uname -s)" = Linux ] || fail '此安装包仅支持 Linux。macOS 开发请从源码构建 Agent。'
    case "$(uname -m)" in
        x86_64) arch=amd64 ;;
        aarch64|arm64) arch=arm64 ;;
        *) fail '仅支持 Linux amd64/arm64。' ;;
    esac
    for tool in curl sha256sum; do
        command -v "$tool" >/dev/null || fail "请先安装 $tool。"
    done
    case "${HOME:-}" in /*) ;; *) fail 'HOME 必须为绝对路径。' ;; esac
    plain_text "$HOME"
    plain_text "$ca_file"
    if [ -n "$ca_file" ]; then
        case "$ca_file" in /*) ;; *) fail '--ca-file 必须为绝对路径。' ;; esac
        [ -r "$ca_file" ] || fail '无法读取 CA 文件。'
    fi
    # Only the fallback prompt requires a terminal; --token also works over pipes.
    if [ "$token_supplied" -eq 0 ]; then
        command -v stty >/dev/null || fail '请先安装 stty。'
        exec 3<>/dev/tty
        tty_state=$(stty -g <&3) || fail '需要交互终端以隐藏输入令牌。'
    fi
    install_dir="$HOME/.local/share/viorka-agent"
    mkdir -p "$HOME/.local/share"
    mkdir "$install_dir" || fail '安装目录已存在，请检查已有 Agent，勿重复注册。'
    cd "$install_dir"
    install_dir=$(pwd -P)
    plain_text "$install_dir"
    printf '安装目录：%s\n' "$install_dir"
    if [ -z "$release_tag" ]; then
        release_url=$(download --output /dev/null --write-out '%{url_effective}' https://github.com/vioira/viorka-agent/releases/latest)
        case "$release_url" in
            https://github.com/vioira/viorka-agent/releases/tag/*) release_tag=${release_url##*/} ;;
            *) fail '无法解析正式 Release。' ;;
        esac
        plain_text "$release_tag"
        printf '%s\n' "$release_tag" | grep -Eq '^v[0-9][0-9A-Za-z._-]*$' || fail '没有可用的正式 Release。'
    fi
    asset="viorka-agent-linux-$arch"
    base="https://github.com/vioira/viorka-agent/releases/download/$release_tag"
    download --output viorka-agent.download "$base/$asset"
    download --output viorka-agent.sha256 "$base/$asset.sha256"
    checksum=$(cat viorka-agent.sha256)
    [ "${#checksum}" -eq 64 ] || fail '安装包校验文件无效。'
    case "$checksum" in *[!a-fA-F0-9]*) fail '安装包校验文件无效。' ;; esac
    actual=$(sha256sum viorka-agent.download)
    actual=${actual%% *}
    checksum=$(printf '%s' "$checksum" | tr 'A-F' 'a-f')
    [ "$actual" = "$checksum" ] || fail '安装包 SHA-256 校验失败。'
    mv viorka-agent.download viorka-agent
    chmod 700 viorka-agent
    mkdir state
    printf '{"panel_url":"%s","node_name":"viorka-node","state_dir":"%s/state","log_level":"info","ca_file":"%s"}\n' \
        "$panel_url" "$(json_string "$install_dir")" "$(json_string "$ca_file")" > agent.json
    ./viorka-agent config-check --config agent.json
    if [ "$token_supplied" -eq 0 ]; then
        trap 'stty "$tty_state" <&3; unset enrollment_token' 0
        trap 'exit 1' 1 2 15
        stty -echo <&3
        printf '粘贴注册令牌后按 Enter（输入隐藏）：' >&3
        IFS= read -r enrollment_token <&3
        stty "$tty_state" <&3
        printf '\n' >&3
    fi
    validate_token
    printf '%s' "$enrollment_token" | ./viorka-agent enroll --config agent.json --token-stdin
    unset enrollment_token
    trap - 0 1 2 15
    if [ "$token_supplied" -eq 0 ]; then exec 3>&-; fi
    printf '注册完成，正在前台连接 Panel。请保持终端运行；是否在线以面板状态为准。\n'
    exec ./viorka-agent run --config "$install_dir/agent.json"
}
main "$@"
