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
    render_service() {
        if [ "$service_manager" = systemd ]; then
            cat <<'UNIT'
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
        else
            cat <<'RC'
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
        fi
    }
    service_preflight() {
        existing_service=0
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
            [ "$load_state" = not-found ] || [ -f "$service_file" ] || fail '存在非本安装器服务，拒绝接管。'
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
        if [ -e "$service_file" ] || [ -L "$service_file" ]; then
            [ "$action" = install ] || fail '服务文件已存在，请使用服务管理器管理。'
            [ -f "$service_file" ] && [ ! -L "$service_file" ] || fail '服务文件不安全。'
            [ "$(stat -c %u "$service_file")" = 0 ] || fail '服务文件必须归 root 所有。'
            mode=$(stat -c %a "$service_file")
            case "$mode" in *[2367][0-7]|*[0-7][2367]) fail '服务文件权限不安全。' ;; esac
            render_service | cmp -s - "$service_file" || fail '已有服务与安装器模板不符，拒绝覆盖。'
            if [ "$service_manager" = systemd ]; then
                [ "$(timeout 15 systemctl show viorka-agent.service -p FragmentPath --value)" = "$service_file" ] || fail 'systemd 服务来源不符。'
                drop_ins=$(timeout 15 systemctl show viorka-agent.service -p DropInPaths --value) || fail '无法检查 systemd 覆盖配置。'
                [ -z "$drop_ins" ] || fail '存在自定义 systemd 配置，请手动更新。'
            fi
            existing_service=1
        fi
    }
    restart_service() {
        if [ "$service_manager" = systemd ]; then
            timeout 45 systemctl restart viorka-agent.service &&
                timeout 15 systemctl is-active --quiet viorka-agent.service
        else
            timeout 45 rc-service viorka-agent restart && timeout 15 rc-service viorka-agent status
        fi
    }
    install_service() {
        if [ "${existing_service:-0}" -eq 1 ]; then
            if [ "$service_manager" = systemd ]; then
                timeout 30 systemctl daemon-reload && timeout 30 systemctl enable viorka-agent.service || fail '设置 Agent 服务失败。'
            else
                timeout 30 rc-update add viorka-agent default || fail '设置 Agent 服务失败。'
            fi
            restart_service || fail '新版 Agent 服务启动失败。'
            printf 'Agent 服务已重启；Panel 在线状态请查看面板或连接日志。\n'
            return
        fi
        # Do not clobber existing files, including dangling symlinks, on retries.
        if [ "$service_manager" = systemd ]; then
            (set -C; render_service > "$service_file") || fail '创建 systemd 服务失败，身份已保留。'
            chmod 644 "$service_file"
            timeout 30 systemctl daemon-reload && timeout 45 systemctl enable --now viorka-agent.service &&
                timeout 15 systemctl is-active --quiet viorka-agent.service || fail '服务启动失败；身份已保留，请运行 journalctl -u viorka-agent 检查，勿重新注册。'
            printf 'Agent 后台服务已启动并设置开机自启；Panel 是否在线请查看面板或连接成功日志。\n日志：journalctl -u viorka-agent -f\n'
        else
            (set -C; render_service > "$service_file") || fail '创建 OpenRC 服务失败，身份已保留。'
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
    for tool in curl sha256sum stat readlink cmp mktemp; do
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
    install_dir="$HOME/.local/share/viorka-agent"
    # Validate even foreground paths before writing or executing anything there.
    for path in "$HOME" "$HOME/.local" "$HOME/.local/share" "$install_dir" "$install_dir/state"; do
        [ ! -L "$path" ] || fail '安装路径不能包含符号链接。'
        if [ -e "$path" ]; then
            [ -d "$path" ] || fail '安装路径不是目录。'
            [ "$(stat -c %u "$path")" = "$(id -u)" ] || fail '安装路径必须归当前用户所有。'
            mode=$(stat -c %a "$path")
            case "$mode" in *[2367][0-7]|*[0-7][2367]) fail '安装路径不能允许组或其他用户写入。' ;; esac
        fi
    done
    for name in agent.json viorka-agent state/identity.json viorka-agent.previous; do
        path="$install_dir/$name"
        [ ! -L "$path" ] || fail '安装文件不能为符号链接。'
        if [ -e "$path" ]; then
            [ -f "$path" ] && [ "$(stat -c %u "$path")" = "$(id -u)" ] || fail '安装文件类型或归属不安全。'
            mode=$(stat -c %a "$path")
            case "$mode" in *[2367][0-7]|*[0-7][2367]) fail '安装文件权限不安全。' ;; esac
        fi
    done
    if [ -z "$release_tag" ]; then
        release_url=$(download --output /dev/null --write-out '%{url_effective}' https://github.com/vioira/viorka-agent/releases/latest) || fail '查询正式 Release 失败，请检查 GitHub 访问及发布状态；尚未创建安装目录。'
        case "$release_url" in
            https://github.com/vioira/viorka-agent/releases/tag/*) release_tag=${release_url##*/} ;;
            *) fail '未找到可用的正式 Release。请在 https://github.com/vioira/viorka-agent/releases 审核并发布含 Linux 二进制及 .sha256 的正式版本；草稿不可下载，已公开的预发布需显式指定 --version=vX.Y.Z。尚未创建安装目录。' ;;
        esac
        plain_text "$release_tag"
        printf '%s\n' "$release_tag" | grep -Eq '^v[0-9][0-9A-Za-z._-]*$' || fail '没有可用的正式 Release。'
    fi
    mkdir -p "$install_dir"
    cd "$install_dir"
    install_dir=$(pwd -P)
    mkdir .install-lock 2>/dev/null || fail '另一个安装正在进行；若此前被强制中断，确认无安装进程后删除 .install-lock 空目录。'
    staging= tty_state= swapped=0 committed=0
    cleanup() {
        code=$?
        trap - 0
        if [ -n "$tty_state" ]; then stty "$tty_state" <&3 || :; fi
        unset enrollment_token
        if [ "$swapped" -eq 1 ] && [ "$committed" -eq 0 ] && [ -f viorka-agent.previous ]; then
            if mv -f viorka-agent.previous viorka-agent; then
                printf '更新失败，已恢复旧 Agent 二进制。\n' >&2
                if [ "${existing_service:-0}" -eq 1 ]; then
                    restart_service || printf '旧 Agent 服务恢复失败，请人工检查服务日志。\n' >&2
                fi
            else
                printf '旧 Agent 二进制恢复失败，请人工处理。\n' >&2
            fi
        fi
        [ -z "$staging" ] || rm -rf "$staging"
        rmdir .install-lock
        exit "$code"
    }
    trap cleanup 0
    trap 'exit 1' 1 2 15
    registered=0
    if [ -f "$install_dir/state/identity.json" ]; then
        [ -f "$install_dir/agent.json" ] || fail '身份存在但配置缺失，请恢复原配置，勿重复注册。'
        registered=1
        unset enrollment_token
        printf '检测到已有身份，将保留配置和身份，忽略本次 URL、CA 和令牌，更新 Agent。\n'
    fi
    running=0
    for proc in /proc/[0-9]*/exe; do
        executable=$(readlink "$proc" 2>/dev/null || :)
        case "$executable" in "$install_dir/viorka-agent"|"$install_dir/viorka-agent (deleted)") running=1 ;; esac
    done
    printf 'Agent 进程状态：%s（1=运行中，0=未检测到）。\n' "$running"
    [ "$running" -eq 0 ] || [ "$registered" -eq 1 ] || fail 'Agent 正在运行但身份不完整，请先检查已有进程。'
    if [ "$running" -eq 1 ] && [ "$foreground" -eq 0 ] && [ "${existing_service:-0}" -eq 0 ]; then
        fail '检测到未由本安装器服务管理的 Agent，请先停止前台进程再转为后台。'
    fi
    staging=$(mktemp -d "$install_dir/.install-XXXXXX")
    printf '安装目录：%s\n' "$install_dir"
    asset="viorka-agent-linux-$arch"
    base="https://github.com/vioira/viorka-agent/releases/download/$release_tag"
    download --output "$staging/viorka-agent" "$base/$asset" || fail "下载 Agent 失败：请确认 Release $release_tag 及 $asset 已发布（版本标签必须精确匹配），可直接重试。"
    download --output "$staging/viorka-agent.sha256" "$base/$asset.sha256" || fail '下载校验文件失败，可直接重试。'
    checksum=$(cat "$staging/viorka-agent.sha256")
    [ "${#checksum}" -eq 64 ] || fail '安装包校验文件无效。'
    case "$checksum" in *[!a-fA-F0-9]*) fail '安装包校验文件无效。' ;; esac
    # GNU sha256sum escapes filenames containing backslashes and prefixes the
    # digest with a backslash. Hash stdin so path spelling cannot affect parsing.
    actual=$(sha256sum < "$staging/viorka-agent")
    actual=${actual%% *}
    checksum=$(printf '%s' "$checksum" | tr 'A-F' 'a-f')
    [ "$actual" = "$checksum" ] || fail '安装包 SHA-256 校验失败。'
    chmod 700 "$staging/viorka-agent"
    mkdir -p state
    if [ ! -f agent.json ]; then
        printf '{"panel_url":"%s","node_name":"viorka-node","state_dir":"%s/state","log_level":"info","ca_file":"%s"}\n' \
            "$panel_url" "$(json_string "$install_dir")" "$(json_string "$ca_file")" > "$staging/agent.json"
        mv "$staging/agent.json" agent.json
    fi
    "$staging/viorka-agent" config-check --config agent.json
    if [ -f viorka-agent ]; then
        cp viorka-agent "$staging/previous"
        mv -f "$staging/previous" viorka-agent.previous
    fi
    mv -f "$staging/viorka-agent" viorka-agent
    swapped=1
    if [ "$registered" -eq 0 ]; then
        if [ "$token_supplied" -eq 0 ]; then
            command -v stty >/dev/null || fail '请先安装 stty。'
            exec 3<>/dev/tty
            tty_state=$(stty -g <&3) || fail '需要交互终端以隐藏输入令牌。'
            stty -echo <&3
            printf '粘贴注册令牌后按 Enter（输入隐藏）：' >&3
            IFS= read -r enrollment_token <&3
            stty "$tty_state" <&3
            printf '\n' >&3
        fi
        validate_token
        printf '%s' "$enrollment_token" | ./viorka-agent enroll --config agent.json --token-stdin
        unset enrollment_token
        if [ "$token_supplied" -eq 0 ]; then tty_state=; exec 3>&-; fi
    fi
    if [ "$foreground" -eq 0 ]; then
        install_service
        committed=1
        return
    fi
    committed=1
    rm -rf "$staging"
    staging=
    rmdir .install-lock
    trap - 0 1 2 15
    if [ "$running" -eq 1 ]; then
        printf '磁盘上的 Agent 已更新；已有进程继续运行旧版本，请通过原 supervisor 或终端重启。\n'
        return
    fi
    printf '注册完成，正在前台连接 Panel。请保持终端运行；是否在线以面板状态为准。\n'
    exec ./viorka-agent run --config "$install_dir/agent.json"
}
main "$@"
