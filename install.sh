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
WorkingDirectory=/root/.viorka-agent
ExecStartPre=/root/.viorka-agent/viorka-agent config-check --config /root/.viorka-agent/agent.json
ExecStart=/root/.viorka-agent/viorka-agent run --config /root/.viorka-agent/agent.json
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
command=/root/.viorka-agent/viorka-agent
command_args="run --config /root/.viorka-agent/agent.json"
directory=/root/.viorka-agent
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
    "$command" config-check --config /root/.viorka-agent/agent.json
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
        for part in /root /root/.viorka-agent; do
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
            service_tools='rc-update supervise-daemon logger'
            [ "$action" != uninstall ] || service_tools=rc-update
            for tool in $service_tools; do
                command -v "$tool" >/dev/null || fail "OpenRC 后台服务需要 $tool。"
            done
            [ ! -e /etc/conf.d/viorka-agent ] && [ ! -L /etc/conf.d/viorka-agent ] || fail '已有 OpenRC 配置，拒绝接管。'
        else
            fail '未检测到运行中的 systemd/OpenRC；请使用 install --foreground 或外部 supervisor。'
        fi
        if [ -e "$service_file" ] || [ -L "$service_file" ]; then
            [ "$action" = install ] || [ "$action" = uninstall ] || fail '服务文件已存在，请使用服务管理器管理。'
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
    uninstall_agent() {
        [ "$(uname -s)" = Linux ] || fail '卸载脚本仅支持 Linux。'
        case "${HOME:-}" in /*) ;; *) fail 'HOME 必须为绝对路径。' ;; esac
        plain_text "$HOME"
        for tool in stat readlink cmp; do
            command -v "$tool" >/dev/null || fail "卸载需要 $tool。"
        done
        # Reject symlinked ancestors as well as the individual deletion targets.
        [ "$(cd "$HOME" && pwd -P)" = "$HOME" ] || fail 'HOME 必须是无符号链接的规范路径。'
        install_dir="$HOME/.viorka-agent"
        for path in "$HOME" "$install_dir" "$install_dir/viorka-agent" "$install_dir/viorka-agent.previous"; do
            [ ! -L "$path" ] || fail '卸载路径不能包含符号链接。'
            if [ -e "$path" ]; then
                case "$path" in
                    "$HOME"|"$install_dir") [ -d "$path" ] || fail '安装路径不是目录。' ;;
                    *) [ -f "$path" ] || fail 'Agent 程序不是普通文件。' ;;
                esac
                [ "$(stat -c %u "$path")" = "$(id -u)" ] || fail '卸载路径必须归当前用户所有。'
                mode=$(stat -c %a "$path")
                case "$mode" in *[2367][0-7]|*[0-7][2367]) fail '卸载路径不能允许组或其他用户写入。' ;; esac
            fi
        done
        # Serialize with install/update; never create a missing installation.
        if [ -d "$install_dir" ]; then
            cd "$install_dir"
            mkdir .install-lock 2>/dev/null || fail '另一个安装或卸载正在进行；请确认无安装进程后再处理残留锁。'
            trap 'rmdir .install-lock' 0
            trap 'exit 1' 1 2 15
        fi
        [ -d /proc ] && readlink /proc/self/exe >/dev/null || fail '无法检查 /proc 中的 Agent 进程，停止卸载。'
        if [ "$foreground" -eq 0 ]; then
            service_preflight
            # Privileged service removal is limited to trusted parent directories.
            path=$(dirname "$service_file")
            while [ "$path" != / ]; do
                [ "$(cd "$path" && pwd -P)" = "$path" ] || fail '服务目录包含符号链接。'
                [ "$(stat -c %u "$path")" = 0 ] || fail '服务目录必须归 root 所有。'
                mode=$(stat -c %a "$path")
                case "$mode" in *[2367][0-7]|*[0-7][2367]) fail '服务目录权限不安全。' ;; esac
                path=$(dirname "$path")
            done
            if [ "$existing_service" -eq 1 ]; then
                printf '正在停止 Agent 服务并取消开机自启（%s）...\n' "$service_manager"
                if [ "$service_manager" = systemd ]; then
                    timeout 45 systemctl stop viorka-agent.service || fail '停止 Agent 服务失败，保留服务文件和程序。'
                    active_state=$(timeout 15 systemctl show viorka-agent.service -p ActiveState --value) || fail '无法确认 Agent 已停止，保留服务文件和程序。'
                    case "$active_state" in inactive|failed) ;; *) fail 'Agent 服务尚未停止，保留服务文件和程序。' ;; esac
                    timeout 30 systemctl disable viorka-agent.service || fail '取消 Agent 开机自启失败，保留服务文件和程序。'
                else
                    timeout 45 rc-service viorka-agent stop || fail '停止 Agent 服务失败，保留服务文件和程序。'
                    status_code=0
                    timeout 15 rc-service viorka-agent status || status_code=$?
                    [ "$status_code" -eq 3 ] || fail '无法确认 OpenRC Agent 已停止，保留服务文件和程序。'
                    for link in /etc/runlevels/*/viorka-agent; do
                        if [ -e "$link" ] || [ -L "$link" ]; then
                            # -a also visits runlevels without this service and can
                            # return failure even after removing existing links.
                            runlevel=${link%/viorka-agent}
                            runlevel=${runlevel##*/}
                            timeout 30 rc-update del viorka-agent "$runlevel" || fail '取消 Agent 开机自启失败，保留服务文件和程序。'
                        fi
                    done
                fi
            fi
            if [ "$service_manager" = openrc ]; then
                for link in /etc/runlevels/*/viorka-agent; do
                    [ ! -e "$link" ] && [ ! -L "$link" ] || fail '仍有 Agent 开机自启链接，保留文件并停止卸载。'
                done
            fi
        elif [ "$HOME" = /root ]; then
            for path in /etc/systemd/system/viorka-agent.service /etc/init.d/viorka-agent /etc/conf.d/viorka-agent; do
                [ ! -e "$path" ] && [ ! -L "$path" ] || fail '检测到后台 Agent 服务，请使用 uninstall（不加 --foreground）。'
            done
        fi
        for proc in /proc/[0-9]*/exe; do
            executable=$(readlink "$proc" 2>/dev/null || :)
            case "$executable" in
                "$install_dir/viorka-agent"|"$install_dir/viorka-agent (deleted)"|"$install_dir/viorka-agent.previous"|"$install_dir/viorka-agent.previous (deleted)")
                    fail '仍有 Agent 进程运行，请通过原终端或 supervisor 停止后重试；未删除程序。' ;;
            esac
        done
        if [ "$foreground" -eq 0 ]; then
            if [ "$existing_service" -eq 1 ]; then
                rm "$service_file" || fail '删除 Agent 服务文件失败，程序已保留。'
            fi
            if [ "$service_manager" = systemd ]; then
                timeout 30 systemctl daemon-reload || fail '刷新 systemd 失败；服务文件可能已移除，程序已保留，请重试卸载。'
            fi
        fi
        rm -f "$install_dir/viorka-agent" "$install_dir/viorka-agent.previous" || fail '删除 Agent 程序失败，请检查权限后重试。'
        printf 'Agent 卸载完成（重复执行安全）。\n配置、身份和其他数据保留在：%s\n未停止或删除代理服务、代理配置及服务商映射；Panel 节点记录需在面板单独处理。\n' "$install_dir"
    }
    print_commands() {
        printf '\n常用命令（请使用本次安装用户执行）：\n'
        case "${service_manager:-foreground}" in
            systemd)
                printf '  服务管理：systemd\n'
                printf '  启动：systemctl start viorka-agent\n'
                printf '  停止：systemctl stop viorka-agent\n'
                printf '  重启：systemctl restart viorka-agent\n'
                printf '  状态：systemctl status viorka-agent\n'
                printf '  最近日志：journalctl -u viorka-agent -n 100 --no-pager\n'
                printf '  实时日志：journalctl -u viorka-agent -f\n'
                ;;
            openrc)
                printf '  服务管理：OpenRC\n'
                printf '  启动：rc-service viorka-agent start\n'
                printf '  停止：rc-service viorka-agent stop\n'
                printf '  重启：rc-service viorka-agent restart\n'
                printf '  状态：rc-service viorka-agent status\n'
                printf "  最近日志：grep 'viorka-agent' /var/log/messages | tail -n 100\n"
                printf "  实时日志：tail -f /var/log/messages | grep 'viorka-agent'\n"
                printf "  若 syslog 使用内存缓冲区：logread -f | grep 'viorka-agent'\n"
                printf '  日志由 logger 写入系统 syslog，需运行 syslog 服务；文件位置取决于 syslog 配置。\n'
                ;;
            foreground)
                printf '  运行方式：前台\n'
                printf '  启动："$HOME/.viorka-agent/viorka-agent" run --config "$HOME/.viorka-agent/agent.json"\n'
                printf '  停止：在运行 Agent 的终端按 Ctrl+C；若由外部 supervisor 管理，请使用其停止命令。\n'
                printf '  重启：停止后重新执行启动命令；若由外部 supervisor 管理，请使用其重启命令。\n'
                printf '  运行日志：直接显示在运行 Agent 的终端；外部 supervisor 的日志请按其配置查看。\n'
                ;;
        esac
        printf '\n更新 Agent（保留配置和身份，无需注册令牌）：\n'
        if [ -z "$panel_url" ]; then
            printf '  请将下面的 https://panel.example.com 替换为面板地址。\n'
        fi
        # panel_url is a validated HTTPS origin; never include enrollment credentials.
        printf "  curl -fsSL --proto '=https' --proto-redir '=https' 'https://raw.githubusercontent.com/vioira/viorka-agent/main/install.sh' | sh -s -- install --url='%s'" "${panel_url:-https://panel.example.com}"
        [ "$foreground" -eq 0 ] || printf ' --foreground'
        printf '\n  默认更新至最新正式版；指定版本可在命令末尾追加 --version=vX.Y.Z。\n'
        if [ "$foreground" -eq 0 ]; then
            printf '  更新成功后自动重启 Agent 服务。\n'
        else
            printf '  已运行的前台进程需自行重启才能使用新版。\n'
        fi
        printf '\n卸载 Agent（保留配置、身份和代理服务）：\n'
        printf "  curl -fsSL --proto '=https' --proto-redir '=https' 'https://raw.githubusercontent.com/vioira/viorka-agent/main/install.sh' | sh -s -- uninstall"
        [ "$foreground" -eq 0 ] || printf ' --foreground'
        printf '\n\nPanel 在线状态：请查看面板，或确认日志出现 Panel connected; session established。\n\n'
    }
    install_service() {
        if [ "${existing_service:-0}" -eq 1 ]; then
            if [ "$service_manager" = systemd ]; then
                timeout 30 systemctl daemon-reload && timeout 30 systemctl enable viorka-agent.service || fail '设置 Agent 服务失败。'
            else
                timeout 30 rc-update add viorka-agent default || fail '设置 Agent 服务失败。'
            fi
            restart_service || fail '新版 Agent 服务启动失败。'
            printf '\nAgent 服务已重启。\n'
            print_commands
            return
        fi
        # Do not clobber existing files, including dangling symlinks, on retries.
        if [ "$service_manager" = systemd ]; then
            (set -C; render_service > "$service_file") || fail '创建 systemd 服务失败，身份已保留。'
            chmod 644 "$service_file"
            timeout 30 systemctl daemon-reload && timeout 45 systemctl enable --now viorka-agent.service &&
                timeout 15 systemctl is-active --quiet viorka-agent.service || fail '服务启动失败；身份已保留，请运行 journalctl -u viorka-agent 检查，勿重新注册。'
        else
            (set -C; render_service > "$service_file") || fail '创建 OpenRC 服务失败，身份已保留。'
            chmod 755 "$service_file"
            timeout 30 rc-update add viorka-agent default && timeout 45 rc-service viorka-agent start &&
                timeout 15 rc-service viorka-agent status || fail '服务启动失败；身份已保留，请检查 rc-service viorka-agent status 和系统日志，勿重新注册。'
        fi
        printf '\nAgent 后台服务已启动并设置开机自启。\n'
        print_commands
    }
    action=${1:-}
    case "$action" in install|service|uninstall) ;; *) fail '用法：sh install.sh install --url=https://panel.example.com [--token=TOKEN] [--version=vX.Y.Z] [--ca-file=/path/ca.pem] [--foreground] [--reenroll-node=ID]；已有安装转后台：sh install.sh service；卸载：sh install.sh uninstall [--foreground]' ;; esac
    shift
    panel_url= release_tag= ca_file= enrollment_token= token_supplied=0 foreground=0 reenroll_node=
    for arg do
        plain_text "$arg"
        if [ "$action" = uninstall ]; then
            [ "$arg" = --foreground ] || fail 'uninstall 仅接受可选的 --foreground，不接受安装参数。'
        fi
        case "$arg" in
            --foreground) foreground=1 ;;
            --reenroll-node=*) reenroll_node=${arg#*=} ;;
            --url=*|-url=*) panel_url=${arg#*=} ;;
            --version=*) release_tag=${arg#*=} ;;
            --ca-file=*) ca_file=${arg#*=} ;;
            --token=*)
                [ "$token_supplied" -eq 0 ] || fail '请勿重复指定 --token。'
                enrollment_token=${arg#*=}
                token_supplied=1 ;;
            *) fail '不支持的参数。仅支持 --url、--token、--version、--ca-file、--foreground、--reenroll-node；自动更新尚未实现。' ;;
        esac
    done
    unset arg
    set --
    if [ "$action" = uninstall ]; then
        uninstall_agent
        return
    fi
    if [ "$action" = service ]; then
        [ -z "$panel_url$release_tag$ca_file$enrollment_token$reenroll_node" ] && [ "$token_supplied" -eq 0 ] && [ "$foreground" -eq 0 ] || fail 'service 不接受安装参数。'
        service_preflight
        cd /root/.viorka-agent
        mkdir .install-lock 2>/dev/null || fail '另一个安装或卸载正在进行，请稍后重试。'
        trap 'rmdir .install-lock' 0
        trap 'exit 1' 1 2 15
        for path in viorka-agent agent.json state state/identity.json; do
            [ -e "$path" ] && [ ! -L "$path" ] || fail '已有安装不完整或包含符号链接。'
            [ "$(stat -c %u "$path")" = 0 ] || fail '已有安装必须归 root 所有。'
            mode=$(stat -c %a "$path")
            case "$mode" in *[2367][0-7]|*[0-7][2367]) fail '已有安装不能允许组或其他用户写入。' ;; esac
        done
        if grep -Eq '"node_id"[[:space:]]*:[[:space:]]*""' state/identity.json; then
            fail '注册尚未完成，请先使用原配置和密钥完成 enroll，再安装服务。'
        fi
        ./viorka-agent config-check --config agent.json
        install_service
        return
    fi
    if [ "$token_supplied" -eq 1 ]; then validate_token; fi
    if [ -n "$reenroll_node" ]; then
        [ "$token_supplied" -eq 1 ] || fail '重新注册必须提供面板确认后生成的 --token。'
        case "$reenroll_node" in *[!a-zA-Z0-9_-]*) fail '重新注册节点 ID 无效。' ;; esac
        [ "${#reenroll_node}" -le 128 ] || fail '重新注册节点 ID 过长。'
    fi
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
    install_dir="$HOME/.viorka-agent"
    if [ ! -e "$install_dir" ] && { [ -e "$HOME/.local/share/viorka-agent" ] || [ -L "$HOME/.local/share/viorka-agent" ]; }; then
        fail '检测到旧目录 ~/.local/share/viorka-agent；请先按 packaging/README.md 迁移配置与身份到 ~/.viorka-agent，勿重复注册。'
    fi
    if [ "$foreground" -eq 0 ]; then service_preflight; fi
    # Validate even foreground paths before writing or executing anything there.
    for path in "$HOME" "$install_dir" "$install_dir/state"; do
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
        # Identity is persisted before the enrollment request. An empty node_id
        # in the Agent-written JSON is a pending key, not a registered node.
        # This is only a retry hint: enroll validates the full identity under
        # the state lock and refuses to overwrite an already registered node.
        if [ -z "$reenroll_node" ] && grep -Eq '"node_id"[[:space:]]*:[[:space:]]*""' state/identity.json; then
            registered=0
            printf '检测到未完成注册，将保留原配置和密钥，使用本次令牌继续注册（忽略本次 URL 和 CA）。\n'
        elif [ -z "$reenroll_node" ]; then
            unset enrollment_token
            printf '检测到已有身份，将保留配置和身份，忽略本次 URL、CA 和令牌，更新 Agent。\n'
        else
            printf '已选择重新注册：保留原配置，成功后替换 Agent 身份。\n'
        fi
    fi
    running=0
    for proc in /proc/[0-9]*/exe; do
        executable=$(readlink "$proc" 2>/dev/null || :)
        case "$executable" in "$install_dir/viorka-agent"|"$install_dir/viorka-agent (deleted)") running=1 ;; esac
    done
    if [ "$running" -eq 1 ]; then
        printf '安装前检查：检测到 Agent 进程正在运行。\n'
    else
        printf '安装前检查：未检测到正在运行的 Agent 进程。\n'
    fi
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
    if [ -n "$reenroll_node" ] && [ "${existing_service:-0}" -eq 1 ]; then
        if [ "$service_manager" = systemd ]; then
            timeout 45 systemctl stop viorka-agent.service || fail '停止 Agent 管理服务失败。'
        else
            timeout 45 rc-service viorka-agent stop || fail '停止 Agent 管理服务失败。'
        fi
    fi
    if [ "$registered" -eq 0 ] || [ -n "$reenroll_node" ]; then
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
        if [ -n "$reenroll_node" ]; then
            printf '%s' "$enrollment_token" | ./viorka-agent enroll --config agent.json --token-stdin --replace-node="$reenroll_node"
        else
            printf '%s' "$enrollment_token" | ./viorka-agent enroll --config agent.json --token-stdin
        fi
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
        print_commands
        return
    fi
    print_commands
    printf '注册完成，正在前台连接 Panel。请保持终端运行；是否在线以面板状态为准。\n'
    exec ./viorka-agent run --config "$install_dir/agent.json"
}
main "$@"
