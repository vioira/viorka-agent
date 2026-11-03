# Agent 发布与安装

`.github/workflows/release.yml` 在推送 `v*` tag 后运行测试并构建 Linux amd64/arm64 静态二进制，所有检查和构建成功后自动公开发布 Release：

- `viorka-agent-linux-amd64` 和 `viorka-agent-linux-arm64`
- 每个二进制对应的 `.sha256` 文件，内容为单独一行 SHA-256 十六进制摘要。

正式版本自动发布后，Panel 安装脚本可通过 GitHub `releases/latest` 发现它。含预发布标识的标签会自动公开发布为 prerelease，不替换 Latest，安装时需指定 `--version=实际标签`。工作流已配置不代表已有版本发布；需推送包含此工作流的 `v*` 标签并完成真实发布验收。标签必须与代码版本一致；已有草稿不会因修改工作流自动发布。

仓库根目录 `install.sh` 提供 POSIX sh 安装入口，Panel 注册弹框生成一行命令：

```sh
curl -fsSL --proto '=https' --proto-redir '=https' 'https://raw.githubusercontent.com/vioira/viorka-agent/main/install.sh' | sh -s -- install --url='https://panel.example.com' --token='面板生成的一次性令牌'
```

面板生成的命令携带 `--token`，复制执行后自动注册，无需再次输入。令牌仅可使用一次且有效期为 15 分钟；参数方式可能出现在 shell 历史和安装进程参数中，因此不要分享完整命令。安装器不打印或持久保存令牌，传给 Agent 时仍使用标准输入。省略 `--token` 时回退到终端隐藏输入。节点 ID、名称和类型由 Panel 令牌绑定，无需传入 `-id` 或名称。`--url`（兼容 `-url`）仅接受 HTTPS Origin，不是 `/wss` 地址。未实现的 `-secret`、`-auto_update` 等参数明确拒绝。

需要 Linux amd64/arm64、POSIX sh、curl、sha256sum，以及 stat、readlink、cmp、mktemp 等基础工具。默认后台安装要求 root、HOME=/root、timeout，以及运行中的 systemd 或 OpenRC（需 supervise-daemon、logger）。后台模式拒绝可由非 root 写入或含符号链接的安装路径。无 root 或无受支持服务管理器时，显式添加 `--foreground` 前台运行。仅隐藏输入模式额外需要 stty 和交互终端。可选 `--version=vX.Y.Z` 固定安装版本；默认解析最新正式 Release，再从同一版本下载二进制和 SHA-256。私有 CA 可追加 `--ca-file=/absolute/path/ca.pem`，仍验证主机名。

入口脚本来自 GitHub main，通过 HTTPS 获取；运行前可先下载并审阅，或将 URL 中 main 替换为审核过且包含安装器的提交/tag。SHA-256 验证的是发行二进制，不是入口脚本的独立签名。必须先将 `install.sh` 发布到对应 Git 引用，并发布正式 Release 资产；本地有文件不代表线上命令已经可用。

旧版 `$HOME/.local/share/viorka-agent` 不会自动迁移。迁移前先停止 Agent 管理进程并备份原目录，将完整目录（包括 `state` 身份）迁到 `$HOME/.viorka-agent`，更新 `agent.json` 的 `state_dir` 为新目录的绝对路径。原 systemd/OpenRC 服务仍引用旧路径；确认归本安装器所有后移除旧 Agent 服务文件，再用 `sh install.sh service` 创建新服务。保留备份，不要重新注册或停止代理引擎。

安装到当前用户 `$HOME/.viorka-agent`，新目录 0700，配置 0600。可重复执行同一条安装命令：

- 通过 Linux `/proc/PID/exe` 检查本安装路径的 Agent 进程；空目录或下载残留不再视为安装成功，下载 404 后修正 `--version` 即可重试，无需删除目录。
- 已存在 `state/identity.json` 时保留身份和配置，跳过注册，不需要令牌；本次 URL、CA 和令牌不会改动已有节点。已注册但停止的 Agent 同样复用身份。身份存在但配置缺失时拒绝重新注册，需恢复原配置。
- 二进制和校验文件下载到独立临时目录，SHA-256 和新二进制 `config-check` 成功后才原子替换。下载或校验失败不替换旧二进制，退出时清理本次临时文件。
- 现有服务必须与安装器模板完全匹配且权限安全（systemd 不允许 drop-in），才能复用并重启。自定义或第三方服务拒绝接管。更新前保留 `viorka-agent.previous`；服务重启失败返回非零并尝试恢复旧二进制和服务，恢复失败明确提示人工处理。
- 已运行的前台 Agent 不重复启动；只更新磁盘二进制，提示通过原 supervisor 或终端重启。未运行时启动前台 Agent。进程状态和服务状态不代表 Panel 握手成功。

每次更新仍按 `--version` 精确下载指定 Release，省略时使用最新正式 Release；这不是定时自动更新。`v0.1.0-beta2` 与 `v0.1.0-beta.2` 是两个不同标签，不自动纠正版本。404 提示检查标签及对应架构资产是否已发布。

安装器使用 `.install-lock` 目录排斥并发安装。常规退出会释放；SIGKILL/断电可能残留锁，确认无安装进程后可用 `rmdir "$HOME/.viorka-agent/.install-lock"` 解除。旧二进制备份和身份会保留，但断电后的自动事务恢复尚未实现。隐藏输入模式会恢复终端回显，两种令牌输入模式均通过标准输入传给 Agent。

默认安装注册完成后创建并启动 `viorka-agent` 服务、设置开机自启，然后返回命令行。systemd 服务文件为 `/etc/systemd/system/viorka-agent.service`，OpenRC 为 `/etc/init.d/viorka-agent`，使用 supervise-daemon。后台进程启动不等于 Panel 已在线；成功完成协议握手后记录 `Panel connected; session established`，重连成功也会记录。Agent 断开后自行重连；进程异常退出由服务管理器进行有界频率重启。没有服务管理器会在注册前拒绝默认安装，不静默退回前台。

已有前台安装可复用身份转为后台：先 Ctrl+C 停止前台 Agent，下载并审阅新版安装脚本，然后执行 `sh install.sh service`。此子命令不下载二进制、不注册、不覆盖已有服务；如果已有服务，直接用服务管理器启动或检查。旧二进制需更新到包含成功日志的版本才会输出该日志。

systemd：

```sh
systemctl start viorka-agent
systemctl stop viorka-agent
systemctl restart viorka-agent
systemctl status viorka-agent
journalctl -u viorka-agent -n 100 --no-pager
journalctl -u viorka-agent -f
```

OpenRC：

```sh
rc-service viorka-agent start
rc-service viorka-agent stop
rc-service viorka-agent restart
rc-service viorka-agent status
# 日志由 logger 写入系统 syslog，需启用 syslog 服务；常见查看方式：
grep 'viorka-agent' /var/log/messages | tail -n 100
tail -f /var/log/messages | grep 'viorka-agent'
# 若 syslog 使用内存缓冲区：
logread -f | grep 'viorka-agent'
```

安装和更新成功后，安装器会按实际使用的 systemd、OpenRC 或前台模式输出管理及日志命令。开头的进程检查明确标注为「安装前检查」，不代表安装后的状态。OpenRC 日志文件位置取决于系统 syslog 配置。

输出中的更新命令不含注册令牌，复用现有配置和身份，默认选择最新正式版，也可追加 `--version=vX.Y.Z`。后台模式更新成功后自动重启 Agent；前台模式保留 `--foreground`，已有进程需自行重启。单独执行 `sh install.sh service` 时未提供面板地址，输出的更新命令使用 `https://panel.example.com` 占位并提示替换。请使用原安装用户执行这些命令；后台模式为 root。

日志轮转和保留上限由系统 journald/syslog 配置负责；安装器不创建无界追加日志文件。服务启动失败保留身份及服务文件并返回非零状态，排查后通过上述管理命令恢复，不要重新注册。支持手动重复安装更新，无定时自动更新功能。

前台模式（`install --foreground`）或已有安装手动运行：

```sh
"$HOME/.viorka-agent/viorka-agent" run --config "$HOME/.viorka-agent/agent.json"
```

一键卸载（后台模式以 root、HOME=/root 执行；需先发布包含 `uninstall` 的脚本）：

```sh
curl -fsSL --proto '=https' --proto-redir '=https' \
  'https://raw.githubusercontent.com/vioira/viorka-agent/main/install.sh' \
  | sh -s -- uninstall
```

也可下载、审阅脚本后执行 `sh install.sh uninstall`。不需要面板地址、Token 或下载 Agent 二进制。安装后的命令提示也包含卸载入口。

- 自动识别运行中的 systemd / OpenRC，只处理与本安装器模板完全匹配的 `viorka-agent` 服务；自定义服务、systemd drop-in、不安全路径或权限均拒绝操作。
- 先停止 Agent 并确认停止，再取消开机自启（OpenRC 清理所有 runlevel 中该服务的链接）、移除 Agent 服务文件及 `viorka-agent` / `viorka-agent.previous`。systemd 同步执行 daemon-reload。
- 默认保留整个数据目录中的配置、身份、Journal 和其他数据，不停止或删除代理服务、代理配置、服务商映射，也不删除 Panel 节点记录。不提供隐式清空数据选项。
- 与安装/更新共用 `.install-lock`，可重复执行。停止、状态检查或取消自启失败均返回非零并保留服务文件和程序；删除服务后刷新 systemd 失败时保留程序，修复后可重试。失败前已完成的停止或禁用操作不会自动撤销。
- 前台安装由原用户先停止 Agent / 外部 supervisor，然后执行 `sh install.sh uninstall --foreground`（远程命令同样追加 `--foreground`）。脚本检查 `/proc` 中当前安装路径的进程；仍在运行时拒绝删除，不使用进程名批量杀进程。无法读取 `/proc/self/exe` 时拒绝卸载。
- 后台卸载要求 Linux、root、HOME=/root、运行中的服务管理器及 stat/readlink/cmp/timeout 等基础工具。若服务被手动改动、迁到其他目录或使用其他 supervisor，需按实际配置手动卸载。

安装器回归测试：`python3 scripts/test_install.py`；卸载回归测试：`python3 scripts/test_uninstall.py`。使用真实 shell、PTY、文件权限与 SHA-256，下载/平台/Agent 可执行文件使用本地 fixtures；卸载测试使用临时目录和服务管理器、`/proc` fixtures，覆盖保留数据、拒绝第三方服务、停止失败、残留进程、锁及重复卸载。不访问生产数据，不替代真实 Linux Release 安装验收。

SHA-256 用于完整性校验，发布来源依赖 GitHub HTTPS 与仓库发布权限；独立签名验证和真实 VPS Release 安装验收尚未完成。服务测试 `python3 scripts/test_services.py` 使用临时目录验证文件保护及错误处理；CI 中 `scripts/test_services_linux.sh` 使用真实 Ubuntu systemd 和 Alpine OpenRC 验证启动、崩溃恢复、停止、卸载、身份保留及无关进程存活（Agent 进程为本地 fixture）。该脚本仅允许在可丢弃的 root 测试环境运行，不用于生产主机。
