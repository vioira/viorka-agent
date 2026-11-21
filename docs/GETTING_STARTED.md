# Agent 入门指南

已实现 `register --config PATH --token-stdin`、`run --config PATH`：Ed25519 本地身份、HTTPS challenge、WSS、心跳、退避重连和只读 `node.inspect`。`doctor` 不连接 Panel，明确报告 `not-checked`。

Linux 用户可直接使用 Panel 生成的一行安装命令，安装器通过 `--token-stdin` 读取命令中的令牌，执行时无需再次粘贴。批量注册命令另带 `--registration`，原节点重新注册命令另带 `--reenroll-node=ID`。安装器参数、发布前提和重启方式见 [安装说明](../packaging/README.md)。本地源码开发仍可使用下述命令。

配置沿用 `examples/agent.example.json`，可选 `ca_file` 用于私有 CA，仍严格验证主机名。Panel URL 必须为 HTTPS Origin，不带路径、query 或凭证，且与 Panel 的 `VIORKA_ORIGIN` 一致。节点显示名称由管理员创建注册令牌时指定。

```sh
go build -o /tmp/viorka-agent ./cmd/viorka-agent
/tmp/viorka-agent config-check --config /absolute/path/agent.json
/tmp/viorka-agent register --config /absolute/path/agent.json --token-stdin
/tmp/viorka-agent run --config /absolute/path/agent.json
```

Token 通过标准输入安全提供（或从 0600 文件重定向），不放在 CLI 参数、URL 或日志。注册响应丢失可在同一令牌有效期内从同一状态目录重试；不会覆盖已完成注册身份。

状态目录必须 0700，拒绝目录本身为符号链接；系统父路径先规范化。身份和日志文件 0600，通过临时文件、fsync、rename、目录 fsync 落盘。`agent.lock` 使用内核文件锁，进程退出释放；同一目录只允许一个进程，禁止复制身份到多节点。

只有 `node.inspect` 可执行，不启动/停止服务，不执行 Shell，不安装引擎。任务日志最多 1000 条/4 MiB，保存真实检查结果后回传；相同幂等键必须匹配 task/deadline。返回旧结果时更新 transport attempt，避免再次执行。截止时间 24 小时后可清理旧日志。其他任务明确拒绝并关闭当前协议连接。

会话到期重新认证；临时连接失败采用 1–30 秒指数退避和最多 50% 抖动。身份拒绝或 TLS 验证失败退出，不通过重新注册绕过吊销。SIGTERM 结束连接，不改变任何已有代理进程。

规范唯一来源仍为 Panel `api/agent/v1`，生成代码标注 Schema SHA-256；本地同步不代表正式 release tag。生成类型不赋予新操作权限。

本机已进行 Go/race、持久日志重投、身份权限和双进程 TLS 注册/任务/重连/吊销测试。Linux amd64/arm64 交叉构建单独验证；真实 NAT VPS、Debian/Alpine 运行、长期压力和内存占用仍待验收。已提供用户目录安装器；代理配置/引擎生命周期、升级签名和系统服务安装尚未实现。

主机监控已增加 Linux CPU、内存/Swap、负载、根文件系统、运行时长和逐网卡流量采集，通过能力协商发送给支持监控的 Panel。默认 30 秒采样；非 Linux 明确报告不支持。完整语义和验收边界见 [MONITORING.md](MONITORING.md)。

## 从 main 分支运行服务器测试节点

服务器上准备好 Bash、Git 和 Go 1.26+，克隆仓库并切到 `main`。已有仓库首次使用此脚本前，先拉取包含脚本的提交。之后每次在仓库根目录运行同一条命令：

```sh
bash scripts/run-test-node.sh
```

每次执行都会从 `origin/main` 快进更新、重新构建，然后前台运行 Agent，用 `Ctrl+C` 退出。新节点使用 `~/.viorka-agent-test` 保存配置和身份。首次创建配置时，若未设置 `VIORKA_TEST_PANEL_URL` 或其值为空，脚本会提示输入 Panel 域名（自动补全 `https://`）或完整 HTTPS 地址。本地节点名默认使用主机名，可通过 `VIORKA_TEST_NODE_NAME` 指定。首次注册和注册未完成时会隐藏输入并提示令牌，重试复用已保存的密钥。注册成功后，后续启动直接复用配置和身份。

已注册节点切换 Panel 域名时，先退出正在运行的测试脚本，再执行以下命令（需要 Python 3）：

```bash
VIORKA_TEST_PANEL_URL='https://panel.example.com' bash scripts/run-test-node.sh
```

本地身份绑定到完整 Panel 地址。脚本为新地址选择独立测试目录，首次连接时需要新域名对应的注册令牌。旧目录保留；后续不带环境变量启动时复用最近选择的目录。要切回旧配置，也可以通过 `--test-dir` 指定旧目录。

如果之前用 `mktemp` 创建过 `~/viorka-agent-test.*`，脚本会复用并记住唯一包含 `agent.json` 的旧目录。若有多个，可运行 `bash scripts/run-test-node.sh --test-dir "$HOME/viorka-agent-test.实际后缀"` 选择一次，后续继续用无参数命令。切换到脚本前先退出占用同一状态目录的旧测试进程。

脚本使用独立的 Agent 身份与日志配置，托管引擎仍属于同一台主机。首次注册会读取本机已有的 Viorka 托管引擎。若使用另一节点的令牌，而原引擎 ID 仍由既有节点持有，Panel 会拒绝恢复。需要沿用既有节点时，在该节点的设置中取得重新注册令牌，再继续运行脚本。
