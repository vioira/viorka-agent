# Agent 配置文件与配置部署

## 1. 当前可执行的校验

```bash
cd viorka-agent
go run ./cmd/viorka-agent config-check --config examples/agent.example.json
```

`config-check`、`enroll` 和 `run` 默认读取当前用户的 `~/.viorka-agent/agent.json`；可用 `--config PATH` 覆盖。安装器同时将二进制放在该目录，状态保存于 `~/.viorka-agent/state`。JSON 中的路径必须写成实际路径，`~` 不会自动展开。

初始配置 JSON（以下为 root 安装示例）：

```json
{
  "panel_url": "https://panel.example.com",
  "node_name": "nat-tokyo-01",
  "state_dir": "/root/.viorka-agent/state",
  "log_level": "info"
}
```

当前 `config-check` 只检验 JSON 结构与 HTTPS base URL 等基础字段；**不会连接服务器或启动 Agent**。文件内故意不放持久注册凭证。

可选 `ca_file` 指向私有 CA PEM，仍验证主机名；`enroll/run` 用法见 [GETTING_STARTED.md](GETTING_STARTED.md)。

## 2. Agent 配置分层

| 层 | 来源 | 内容 |
|---|---|---|
| Bootstrap | 本地 `agent.json` / CLI env | Panel Origin、节点名称、本地状态路径、日志级别 |
| Identity | 本地受保护存储 | node_id、密钥、会话状态，不应复制到源码仓库 |
| Desired Runtime | Panel immutable Revision | sing-box/Xray/Realm 具体目标配置 |
| Observed Runtime | Agent state store | 最后成功 Revision、配置 hash、引擎进程和健康 |

Panel 的 Desired 不直接覆盖 Bootstrap 或 Identity；Agent 也不把自己的临时运行状态当作中央 Desired。

## 3. 配置发布流程

```text
Receive Revision → Validate metadata/permission/capabilities
 → Fetch securely / receive config payload → SHA-256 check
 → Staging temp file → engine validate
 → Backup currently active config → Atomic switch
 → Restart / reload (capability-dependent)
 → Health-check → Persist observed revision → Ack success
                ↘ on failure → Try rollback → Report actual state
```

- 原子文件替换必须考虑系统文件权限、磁盘写入完成、跨文件系统 rename 等条件。
- 静态校验成功不意味着端口能监听或 TLS/REALITY 能成功完成握手；需要进程与网络健康检查。
- 失败回滚不能被描述成绝对保证；可能遇到旧文件损坏/运行时缺失，必须显式上报 degraded。

## 4. Revision 保护

Agent 收到旧于当前 Revision 的普通 apply 指令时应拒绝；如果操作者确实要恢复旧版本，应由 Panel 创建一条新的 Revision，内容引用历史快照，保证版本仍单调递增并可审计。

## 5. 本地配置与机密

- `agent.json` 不存长期 Authorization Token 或代理私钥明文。
- 文件目录权限需要 0700、敏感文件 0600（根据运行账户调整）。
- 完整代理配置可能仍包含业务 Secret，必须限定读权限和备份策略。
- 日志只保留配置 Revision、Hash、操作结果，异常堆栈须脱敏。

## 主机监控周期

可选 `metrics_seconds` 默认 30 秒，允许 10–300 秒，0 使用默认值。该周期独立于心跳；只有 Panel 确认 `node.metrics.v1` 扩展后才发送指标。指标范围、失败状态、资源上限及平台边界见 [MONITORING.md](MONITORING.md)。

## 发行版与公网地址资产

Agent 与 Panel 协商 `node.inventory.v1` 后，上线及每 5 分钟发送一次 `node.inventory`。Linux 读取 `/etc/os-release`，不存在时回退 `/usr/lib/os-release`，上报 ID、PRETTY_NAME 和 VERSION_ID；非 Linux 或读取失败保留基础 OS，并报告发行版未知。

公网 IPv4/IPv6 使用 [ipify 官方 API](https://www.ipify.org/) 的 `https://api.ipify.org` 与 `https://api6.ipify.org`。分别强制 TCP4/TCP6、直接出站，不使用 HTTP 代理、不携带 Panel 凭证、不跟随重定向，正常验证 TLS。每请求最长 4 秒、响应上限 64 字节；重连复用 5 分钟缓存。每族只记录该探测路径的一个公网出口地址，不表示全部网卡地址、入站可达性或 NAT 提供商端口映射。探测失败上报空值和对应 unavailable 错误，不声称没有该网络能力。

Panel 将最新快照保存在 `nodes.inventory_json`，断线后保留，页面显示采集时间。此扩展需升级双方；旧 Panel 不确认扩展时 Agent 不发资产消息，新 Panel 对旧 Agent 不添加 ACK 字段。未部署的新二进制不会改变现有节点上报。

## 每节点采集设置（node.settings.v2）

握手 collection_settings 覆盖本会话配置：采集 0–10 秒默认 0（随 WSS 上报采集），历史入库 30–180 秒默认 60，WSS 发送 1–5 秒默认 2。资产刷新独立 300 秒，心跳 30 秒。快采集通过有界样本批次发送，慢采集重复快照保持原采样时间。新 Agent 不宣告旧 node.settings.v1；旧 Panel 下使用本地 metrics_seconds，不将旧资产周期解释成历史周期。详见 [MONITORING.md](MONITORING.md)。

## 最后生效的节点采集设置

新版 Panel 通过已协商的 `node.settings.live.v1` 在线下发采集设置，Agent 先校验、
写入 `state_dir/node-settings.json`，再应用并回报 Revision。文件绑定 Panel/Node 身份，
使用状态目录的权限与原子写入规则；不修改本地 `agent.json`。启动时恢复此设置并独立采集，
断线只保留最新内存快照，不保存指标历史。旧 Panel 未下发设置时使用 metrics_seconds 回退值。
旧版本兼容、错误码和确认语义见 [MONITORING.md](MONITORING.md)。

### 新版监控接口过滤（开发中）

本地配置可增加 `network_filter`：

```json
{
  "network_filter": {
    "include": ["wg-public", "br-uplink"],
    "exclude": ["eth-backup"]
  }
}
```

这是完整 bootstrap 配置中的可选字段。默认排除 `lo`、`docker*`、`veth*`、
`br*`、`tun*`、`wg*`、`tailscale*`、`zerotier*`、`tap*`；显式包含覆盖默认排除，
显式排除最终生效。每个列表最多 64 项，每项 1–64 字节，仅接受 ASCII 字母、
数字、`._:-` 和末尾 `*` 前缀匹配；不接受重复项、正则或 Shell 表达式。
规则生成稳定的 SHA-256 filter_revision，列表重排不改变统计口径标识。

当前字段已通过配置校验并由独立 v3 网络采集器支持，但 v3 调度链路尚未启用，
**现有运行中的旧版指标采集不会应用此字段**。后续启用时，接口名称、ifindex、
boot_id 共同决定计数基线；切换采集周期保留基线，首次/身份变化/计数倒退/采集失败
恢复不输出伪造速率。默认过滤是启发式规则，不保证等同于服务商计费流量。

### 月流量账本文件（开发中）

新版月流量组件使用固定 `state_dir/traffic.dat`，不接受远程文件名或路径。
状态目录须为 0700，文件须为当前用户拥有的 0600 普通单链接文件；读取不跟随
最终符号链接，最多 64 KiB。文件包含版本化快照与 SHA-256 校验，绑定 Panel URL、
Node ID、UTC 月、boot_id、过滤规则、逐接口基线和累计。校验用于检测损坏，不是
对文件所有者恶意修改的认证保证。

保存沿用临时文件、文件 fsync、原子 rename、目录 fsync；正常保存/失败重试至少
间隔 60 秒，退出可作一次最终保存。损坏、权限不安全、身份不匹配或运行中被外部
替换时保留原文件、月流量降级 unknown；不得自动删除文件再从零开始。写盘失败
保留内存累计并标记 persist_failed。上下文可取消保存调度与检查点，但无法硬中断
正在执行的内核 fsync，慢磁盘退出行为仍需实机验证。

当前账本及文件恢复测试已实现，尚未连接客户端采集/保存/退出生命周期；现有 Agent
不会因这些开发中组件自动生成 traffic.dat。

### 新版监控设置文件（开发中）

v3 设置使用固定 `state_dir/node-telemetry-settings.json`，与旧
`node-settings.json` 分开保存并绑定 Panel/Node。读取限制 16 KiB、0700 目录、
当前用户拥有的 0600 普通单链接文件，拒绝最终符号链接及重复/未知 JSON 字段。
低 revision 或同 revision 不同内容均拒绝；只有原子写入成功后才更新已应用设置。
同 revision 重投仍执行耐久写入，以恢复先前可能失败的目录同步。

独立周期规则已实现并测试，但尚未与客户端 ready/租约/POST 调度接线：HTTP 下
C 控制 fast、R 控制 full/POST，允许 C>R；实时下 W 控制 fast/发送、R 保持 full，
C 只保存供退出实时后生效。修改 R 不重排 HTTP fast，跳过的采样窗口不补跑。

### 新监控启动开关（受控验收）

`telemetry_v3` 是本地 JSON 布尔字段，省略或 `false` 时不声明新能力；`true` 时声明 `node.telemetry.v3`，仍须 Panel 下发 ready 并应用独立设置后才启用双出口。此开关默认关闭，不表示长稳与平台验收已完成。修改后需重新启动 Agent，不能从远端任务修改本地开关。首次连接不支持新能力的 Panel 时保留旧协议协商路径。

受控联合测试需 Panel 同时设置 `VIORKA_TELEMETRY_V3=true`。仅配置 Agent 不会让未支持的 Panel 自动接受 POST。回退配置时保留身份、旧设置与 `traffic.dat`，不清理代理服务或历史数据；已协商 v3 的进程重连后会等待 10 秒 ready；若始终没有 ready，则有界停止 v3 runner、保存月账本并恢复旧版采集/上报。关闭失败会明确退出而非假称回退成功。已收到 ready 但设置应用失败不会静默走此回退。再次协商 v3 会建立新的运行代次；普通 v3 重连保留原代次。该机制不修改代理服务，完整跨版本失败矩阵仍须验收。

### v3 本地运行诊断

启用并完成协商后，采样调度器最多每分钟输出一条结构化 `Telemetry status`，管理 WSS 断开时仍可记录。字段包括当前模式、C/R/W、需求开关和租约剩余秒数、各组最后完成时间和错误标记、最后发送/收到成功回执时间、pending/inflight 样本数及编码样本字节、限额/冷却/失败窗口丢弃计数、拒绝次数与 HTTP/WSS 最早允许尝试前的等待秒数。日志不记录 Token、会话身份、采样内容或原始错误文本。采集返回有效对象但其中包含局部错误时，对应组错误标记仍为 true；恢复后清除。

零时间表示尚未发生；最后完成时间不是最后成功采样时间。`last_receipt_accepted` 表示收到入口接收回执，不保证 Panel 已提交 SQLite；刷盘状态应检查 Panel。`pending_sample_bytes` / `inflight_sample_bytes` 不包含外层 JSON、传输头及其他缓存，不能当作进程总内存。`dropped_failed_flight` 表示本地放弃所有权的失败窗口点数；丢失回执前 Panel 可能已接收部分或全部点，不能据此计算服务端丢点量。

退出额外记录一次 `Telemetry shutdown`，分别标记 worker 结束及最终月流量保存是否成功、整体调度关闭是否成功。普通保存状态由 `last_traffic_save_completed` 与 `traffic_save_error` 查看。Agent 只知道是否有实时需求，订阅者引用数量由 Panel 维护。
