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
    service_preflight() {
        [ "$(uname -s)" = Linux ] || fail '后台服务仅支持 Linux。'
        [ "$(id -u)" -eq 0 ] || fail '后台服务安装需要 root；无 root 请使用 install --foreground。'
        # Fixed root-owned location avoids privileged execution from user-controlled paths.
        [ "$HOME" = /root ] || fail '后台服务要求 HOME=/root；其他目录请使用 --foreground。'
        for part in /root /root/.local /root/.local/share /root/.local/share/viorka-agent; do
            [ ! -L "$part" ] || fail '后台服务路径不能包含符号链接。'
            if [ -e "$part" ]; then
                [ "$(stat -c %u "$part")" = 0 ] || fail '后台服务路径必须归 root 所有。'
                mode=$(stat -c %a "$part")
                case "$mode" in *[2367][0-7]|*[0-7][2367]) fail '后台服务路径不能允许组或其他用户写入。' ;; esac
            fi
        done
        command -v timeout >/dev/null || fail '后台服务安装需要 timeout。'
        if [ -d /run/systemd/system ] && command -v systemctl >/dev/null; then
            service_manager=systemd
            service_file=/etc/systemd/system/viorka-agent.service
            load_state=$(timeout 15 systemctl show viorka-agent.service -p LoadState --value) || fail '无法访问 systemd。'
            [ "$load_state" = not-found ] || fail '已存在 viorka-agent 服务，请使用 systemctl 管理，拒绝覆盖。'
        elif [ -d /run/openrc ] && [ -x /sbin/openrc-run ] && command -v rc-service >/dev/null; then
            service_manager=openrc
            service_file=/etc/init.d/viorka-agent
            for tool in rc-update supervise-daemon logger; do
                command -v "$tool" >/dev/null || fail "OpenRC 后台服务需要 $tool。"
            done
            [ ! -e /etc/conf.d/viorka-agent ] && [ ! -L /etc/conf.d/viorka-agent ] || fail '已有 OpenRC 配置，拒绝接管。'
        else
            fail '未检测到运行中的 systemd/OpenRC；请使用 install --foreground 或外部 supervisor。'
        fi
        [ ! -e "$service_file" ] && [ ! -L "$service_file" ] || fail '服务文件已存在，拒绝覆盖。'
    }
    install_service() {
        # Do not clobber existing files, including dangling symlinks, on retries.
        if [ "$service_manager" = systemd ]; then
            (set -C; cat > "$service_file" <<'UNIT'
[Unit]
Description=Viorka Agent
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=60
StartLimitBurst=5

[Service]
Type=exec
WorkingDirectory=/root/.local/share/viorka-agent
ExecStartPre=/root/.local/share/viorka-agent/viorka-agent config-check --config /root/.local/share/viorka-agent/agent.json
ExecStart=/root/.local/share/viorka-agent/viorka-agent run --config /root/.local/share/viorka-agent/agent.json
Restart=on-failure
RestartSec=5
TimeoutStartSec=30
TimeoutStopSec=20
UMask=0077
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
UNIT
            ) || fail '创建 systemd 服务失败，身份已保留。'
            chmod 644 "$service_file"
            timeout 30 systemctl daemon-reload && timeout 45 systemctl enable --now viorka-agent.service &&
                timeout 15 systemctl is-active --quiet viorka-agent.service || fail '服务启动失败；身份已保留，请运行 journalctl -u viorka-agent 检查，勿重新注册。'
            printf 'Agent 后台服务已启动并设置开机自启；Panel 是否在线请查看面板或连接成功日志。\n日志：journalctl -u viorka-agent -f\n'
        else
            (set -C; cat > "$service_file" <<'RC'
#!/sbin/openrc-run
name="Viorka Agent"
supervisor=supervise-daemon
command=/root/.local/share/viorka-agent/viorka-agent
command_args="run --config /root/.local/share/viorka-agent/agent.json"
directory=/root/.local/share/viorka-agent
pidfile=/run/viorka-agent.pid
umask=0077
respawn_delay=5
respawn_max=5
respawn_period=60
retry="TERM/20/KILL/5"
output_logger="logger -t viorka-agent"
error_logger="logger -t viorka-agent"
depend() {
    use net logger
}
start_pre() {
    "$command" config-check --config /root/.local/share/viorka-agent/agent.json
}
RC
            ) || fail '创建 OpenRC 服务失败，身份已保留。'
            chmod 755 "$service_file"
            timeout 30 rc-update add viorka-agent default && timeout 45 rc-service viorka-agent start &&
                timeout 15 rc-service viorka-agent status || fail '服务启动失败；身份已保留，请检查 rc-service viorka-agent status 和系统日志，勿重新注册。'
            printf 'Agent 后台服务已启动并设置开机自启；Panel 是否在线请查看面板或连接成功日志。\n日志：系统 syslog 中的 viorka-agent 标签（常见 /var/log/messages 或 logread）；需运行 syslog 服务。\n'
        fi
    }
    action=${1:-}
    case "$action" in install|service) ;; *) fail '用法：sh install.sh install --url=https://panel.example.com [--token=TOKEN] [--version=vX.Y.Z] [--ca-file=/path/ca.pem] [--foreground]；已有安装转后台：sh install.sh service' ;; esac
    shift
    panel_url= release_tag= ca_file= enrollment_token= token_supplied=0 foreground=0
    for arg do
        plain_text "$arg"
        case "$arg" in
            --foreground) foreground=1 ;;
            --url=*|-url=*) panel_url=${arg#*=} ;;
            --version=*) release_tag=${arg#*=} ;;
            --ca-file=*) ca_file=${arg#*=} ;;
            --token=*)
                [ "$token_supplied" -eq 0 ] || fail '请勿重复指定 --token。'
                enrollment_token=${arg#*=}
                token_supplied=1 ;;
            *) fail '不支持的参数。仅支持 --url、--token、--version、--ca-file、--foreground；自动更新尚未实现。' ;;
        esac
    done
    unset arg
    set --
    if [ "$action" = service ]; then
        [ -z "$panel_url$release_tag$ca_file$enrollment_token" ] && [ "$token_supplied" -eq 0 ] && [ "$foreground" -eq 0 ] || fail 'service 不接受安装参数。'
        service_preflight
        cd /root/.local/share/viorka-agent
        for path in viorka-agent agent.json state state/identity.json; do
            [ -e "$path" ] && [ ! -L "$path" ] || fail '已有安装不完整或包含符号链接。'
            [ "$(stat -c %u "$path")" = 0 ] || fail '已有安装必须归 root 所有。'
            mode=$(stat -c %a "$path")
            case "$mode" in *[2367][0-7]|*[0-7][2367]) fail '已有安装不能允许组或其他用户写入。' ;; esac
        done
        ./viorka-agent config-check --config agent.json
        install_service
        return
    fi
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
    if [ "$foreground" -eq 0 ]; then service_preflight; fi
    # Only the fallback prompt requires a terminal; --token also works over pipes.
    if [ "$token_supplied" -eq 0 ]; then
        command -v stty >/dev/null || fail '请先安装 stty。'
        exec 3<>/dev/tty
        tty_state=$(stty -g <&3) || fail '需要交互终端以隐藏输入令牌。'
    fi
    install_dir="$HOME/.local/share/viorka-agent"
    if [ -e "$install_dir" ] || [ -L "$install_dir" ]; then
        fail '安装目录已存在，请检查已有 Agent，勿重复注册。'
    fi
    if [ -z "$release_tag" ]; then
        release_url=$(download --output /dev/null --write-out '%{url_effective}' https://github.com/vioira/viorka-agent/releases/latest) || fail '查询正式 Release 失败，请检查 GitHub 访问及发布状态；尚未创建安装目录。'
        case "$release_url" in
            https://github.com/vioira/viorka-agent/releases/tag/*) release_tag=${release_url##*/} ;;
            *) fail '未找到可用的正式 Release。请在 https://github.com/vioira/viorka-agent/releases 审核并发布含 Linux 二进制及 .sha256 的正式版本；草稿不可下载，已公开的预发布需显式指定 --version=vX.Y.Z。尚未创建安装目录。' ;;
        esac
        plain_text "$release_tag"
        printf '%s\n' "$release_tag" | grep -Eq '^v[0-9][0-9A-Za-z._-]*$' || fail '没有可用的正式 Release。'
    fi
    mkdir -p "$HOME/.local/share"
    mkdir "$install_dir" || fail '安装目录已存在，请检查已有 Agent，勿重复注册。'
    cd "$install_dir"
    install_dir=$(pwd -P)
    plain_text "$install_dir"
    printf '安装目录：%s\n' "$install_dir"
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
    if [ "$foreground" -eq 0 ]; then
        install_service
        return
    fi
    printf '注册完成，正在前台连接 Panel。请保持终端运行；是否在线以面板状态为准。\n'
    exec ./viorka-agent run --config "$install_dir/agent.json"
}
main "$@"
