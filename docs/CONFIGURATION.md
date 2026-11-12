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
  "log_level": "warn"
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

## 发行版与公网地址资产

Agent 与 Panel 协商 `node.inventory.v1` 后，上线及每 5 分钟发送一次 `node.inventory`。Linux 读取 `/etc/os-release`，不存在时回退 `/usr/lib/os-release`，上报 ID、PRETTY_NAME 和 VERSION_ID；非 Linux 或读取失败保留基础 OS，并报告发行版未知。

公网 IP 使用固定 HTTPS 来源按顺序探测，成功即返回，失败自动切换，无需识别云厂商。IPv4 顺序为 `https://www.visa.cn/cdn-cgi/trace`、`https://www.qualcomm.cn/cdn-cgi/trace`、`https://api.ipify.org`；IPv6 顺序为 `https://api6.ipify.org`、`https://ipv6.icanhazip.com`、`https://cloudflare.com/cdn-cgi/trace`。分别强制 TCP4/TCP6、直接出站，不使用 HTTP 代理、不携带 Panel 凭证、不跟随重定向，正常验证 TLS。每请求最长 4 秒，每个地址族最多 10 秒；纯文本响应上限 64 字节，trace 响应上限 4096 字节且只解析 `ip=` 行，均校验公网地址和地址族。旧版资产采集复用 5 分钟缓存，新版监控公网 IP 独立每 600 秒刷新。每族只记录该探测路径的一个公网出口地址；全部来源失败则上报空值和对应 unavailable 错误。

Panel 将最新快照保存在 `nodes.inventory_json`，断线后保留，页面显示采集时间。此扩展需升级双方；旧 Panel 不确认扩展时 Agent 不发资产消息，新 Panel 对旧 Agent 不添加 ACK 字段。未部署的新二进制不会改变现有节点上报。

### 新版监控接口过滤

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
新版采集应用该字段。接口名称、ifindex、
boot_id 共同决定计数基线；切换采集周期保留基线，首次/身份变化/计数倒退/采集失败
恢复不输出伪造速率。默认过滤是启发式规则，不保证等同于服务商计费流量。

### 月流量账本文件

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

### 新版监控设置文件

v3 设置使用固定 `state_dir/node-telemetry-settings.json`，绑定 Panel/Node。读取限制 16 KiB、0700 目录、
当前用户拥有的 0600 普通单链接文件，拒绝最终符号链接及重复/未知 JSON 字段。
低 revision 或同 revision 不同内容均拒绝；只有原子写入成功后才更新已应用设置。
同 revision 重投仍执行耐久写入，以恢复先前可能失败的目录同步。

独立周期规则已实现并测试，但尚未与客户端 ready/租约/POST 调度接线：HTTP 下
C 控制 fast、R 控制 full/POST，允许 C>R；实时下 W 控制 fast/发送、R 保持 full，
C 只保存供退出实时后生效。修改 R 不重排 HTTP fast，跳过的采样窗口不补跑。

### 新监控启动开关（受控验收）

`telemetry_v3` 省略或 `true` 时声明 `node.telemetry.v3`，必须经 Panel ready 和设置确认后才能上报。`false` 明确停用监控，不恢复旧采集。Panel 默认启用，可用 `VIORKA_TELEMETRY_V3=false` 停用；两端启动配置修改需要重启。

旧 `metrics_seconds` 已删除，严格配置解析会拒绝该字段；升级时从 bootstrap JSON 移除。旧 `node-settings.json` 不再读取或写入，不自动删除用户状态目录中的文件。没有 ready 的管理连接在 10 秒后报不支持错误并重连，不回退旧协议；正常 v3 重连保留采集运行代次。监控重新协商成功后清除 HTTP/WSS 错误冷却和上次 POST 的间隔等待，立即补报缓存的最新指标；已有上传先结束，旧连接的迟到结果不会重新设置冷却。服务端明确的限流、Retry-After 和永久身份拒绝仍然有效。此行为不修改代理服务。

### v3 本地运行诊断

启用并完成协商后，采样调度器最多每分钟输出一条结构化 `Telemetry status`，管理 WSS 断开时仍可记录。字段包括当前模式、C/R/W、需求开关和租约剩余秒数、各组最后完成时间和错误标记、最后发送/收到成功回执时间、pending/inflight 样本数及编码样本字节、限额/冷却/失败窗口丢弃计数、拒绝次数与 HTTP/WSS 最早允许尝试前的等待秒数。日志不记录 Token、会话身份、采样内容或原始错误文本。采集返回有效对象但其中包含局部错误时，对应组错误标记仍为 true；恢复后清除。

零时间表示尚未发生；最后完成时间不是最后成功采样时间。`last_receipt_accepted` 表示收到入口接收回执，不保证 Panel 已提交 SQLite；刷盘状态应检查 Panel。`pending_sample_bytes` / `inflight_sample_bytes` 不包含外层 JSON、传输头及其他缓存，不能当作进程总内存。`dropped_failed_flight` 表示本地放弃所有权的失败窗口点数；丢失回执前 Panel 可能已接收部分或全部点，不能据此计算服务端丢点量。

退出额外记录一次 `Telemetry shutdown`，分别标记 worker 结束及最终月流量保存是否成功、整体调度关闭是否成功。普通保存状态由 `last_traffic_save_completed` 与 `traffic_save_error` 查看。Agent 只知道是否有实时需求，订阅者引用数量由 Panel 维护。

### 节点日志等级

`log_level` 支持 `debug`、`info`、`warn`、`error`，省略默认 `warn`；新安装配置也使用 `warn`。
级别为最低输出阈值，例如 `warn` 输出警告和错误，`info` 额外输出连接及每分钟监控状态，
`debug` 额外输出配置应用诊断。退出失败按 ERROR 输出，CLI 致命错误仍写 stderr 并返回非零退出码。

Panel 节点设置中的日志等级随 `node.settings.v3`（首次连接为 `node.telemetry.ready.settings`）
和采集周期一起下发。沿用 revision、身份校验、原子持久化和应用回执；仅持久化成功后即时修改
并发安全的日志阈值，无需重启。无效等级、旧 revision、同 revision 冲突或写盘失败不改变当前等级。
保存于 `node-telemetry-settings.json`，Agent 重启时恢复已保存的 Panel 等级，优先于本地 bootstrap。
尚无 Panel 设置时使用本地等级；远程设置省略等级按 `warn` 处理。WSS 断开不重置等级。
此设置复用监控链路，需启用 `telemetry_v3` 并完成协商。


### Panel 管理的计费流量

声明 `node.traffic.v1` 后，`TelemetrySettings.traffic` 管理统计网卡、月计费周期、时区、计费方向与额度。显式网卡是精确名称白名单，默认汇总系统过滤后的网卡；不存在的指定网卡拒绝应用。网卡修改重建速率/累计增量基线，保留本周期累计值。

`traffic.dat` 保存当前周期边界、规则和校准幂等记录。重置日期 1–31，短月取最后一天；周期设置修改延迟至当前周期结束。重启恢复累计值，跨边界无法准确归属的流量不强行计入新周期，实时标记边界缺口。

`traffic.calibrate` 是唯一新增可变任务，参数仅包含目标周期及上下行累计字节数，不接收路径或命令。执行前保存任务意图，将校准目标与幂等记录一起原子写入账本。重复投递不会覆盖校准后增量；周期不匹配或未知账本拒绝成功，每周期最多 64 次校准。额度和计费方式仅供 Panel 展示，不执行限速或断网。

本次为开发契约更新，需匹配 Panel 协议与新开发数据库，不提供历史数据转换。

### 手动网络诊断

`viorka-agent doctor` 只输出本机基本信息，默认不读取配置、不发起网络请求。
显式检查使用以下选项，可同时指定：

```bash
viorka-agent doctor --public-ip
viorka-agent doctor --panel --config ~/.viorka-agent/agent.json
viorka-agent doctor --public-ip --panel
```

`--public-ip` 检查 IPv4/IPv6 的全部固定来源，成功后仍继续检查其余来源；复用正常采集的直连、TLS 校验、禁止重定向和响应大小限制。每来源最长 4 秒、每族最长 10 秒。JSON 的 `public_ip` 数组包含 `source`、`family`、`elapsed_ms`、`status`、成功时的 `address`，失败时的 `failure` 为 `dns`、`connection`、`tls`、`http` 或 `format`。耗尽地址族预算或取消后尚未开始的来源标记 `skipped/budget`；超时按发生阶段分类。诊断不改变正常采集的缓存、周期或上报协议。

`--panel` 读取 bootstrap 配置，依次检查 DNS、TCP、TLS，合计最长 10 秒；支持 `ca_file`，验证 CA 和主机名。各阶段记录状态及耗时，失败后后续阶段标记 `skipped`。直连 Panel，不使用环境代理；不读取节点身份、不注册、不发送 HTTP/WSS 请求。`connection=transport-checked` 表示已执行传输层检查，结果以 `panel` 各阶段为准。输出不包含凭证、响应正文或原始错误文本。

整次网络诊断最长 30 秒，支持 SIGINT/SIGTERM 取消。全部请求的检查成功退出 0；任一来源或阶段失败/跳过退出 1（包括节点没有 IPv6）；参数、配置或 CA 文件错误退出 2。`--config` 可与 `--panel` 或 `--public-ip` 一起使用；单独 `--public-ip` 不读取配置。

### 可选备用 DNS

本地 bootstrap 可设置 `"dns_fallback_servers": ["192.0.2.53:53", "[2001:db8::53]:53"]`
（示例地址须替换为实际可达服务器）。最多 3 个不同服务器，必须明确指定单播 IP 和
1–65535 端口；不接受域名、URL、未指定地址、组播或 IPv6 zone。

省略或空列表时保留系统 DNS 行为。设置后仍先使用系统解析，只有真实 DNS 查询错误
或超时才按配置顺序查询备用服务器；每次 DNS 解析最多 1 秒，受调用方整体 deadline
和取消约束。独立解析会按剩余服务器数分配短时限，避免 TCP 探测的系统查询耗尽全部预算。备用查询固定使用标准库 DNS over TCP，不要求 UDP，不修改系统 DNS。
系统解析成功后不会因 TCP 连接失败改用备用 DNS，也不自动添加公共 DNS。

配置同时用于 Panel HTTP/WSS、资产与监控公网 IP 探测，以及
`doctor --panel --config PATH` 和 `doctor --public-ip --config PATH`。
拨号保留标准库的多地址尝试、地址族筛选与 IPv4/IPv6 并行回退；DNS 计时不缩短后续
TCP 连接预算。HTTPS/WSS URL 和 TLS 主机名保持原始域名，仍验证 CA 和主机名。
公网 IP 的固定来源、直连规则与整体时间预算不变。Panel 使用环境代理时，备用解析
作用于本地拨号的代理主机；代理侧解析 Panel 域名仍由代理负责。

### TCP 线路质量（默认关闭）

面板「系统设置 → 通用 → 全局 TCP 探测目标」统一配置目标。节点默认继承，可在「节点详情 → 设置 → TCP 探测目标」选择独立配置；独立目标留空关闭该节点探测。全局默认为空。保存后在线 Agent 经版本化设置与回执直接应用，离线节点重连时应用，无需重启。

新 Panel 下发的目标覆盖本地 `targets`；连接不支持下发目标的旧 Panel 时仍使用下列本地配置。探测周期固定为 20 秒、窗口固定为 120 秒；面板目标默认使用 1000 毫秒超时：

```json
"tcp_probe": {
  "targets": ["example.com:443"],
  "timeout_ms": 1000
}
```

请将示例目标替换为要测量的公网服务。最多 3 个明确的 `host:port`，不接受 URL、端口范围和内网地址；每次 DNS 解析后再次检查全部地址，拒绝含内网或保留地址的答案，再直连选中的第一个公网 IP。复用系统 DNS 及 `dns_fallback_servers`，不执行 HTTP/TLS，不发送业务内容。

单目标解析与连接总超时可配置为 100–1000 毫秒。探测独立于 full 采集和上报调度，实时观看不会加速探测，不补发错过的轮次。

Panel/Agent 协商 `node.tcp_probe.v2` 后启用独立探测。每 20 秒一轮，最多三个目标串行、每目标一次 TCP 连接；单目标超时包含 DNS，RTT 不包含 DNS。各目标保留最近 120 秒、最多 6 个样本，ping 为成功样本中位数，loss 为失败数 × 100 / 总数的整数商。DNS、地址拦截及连接失败均记失败，停止取消不计入；全失败为 ping=-1、loss=100，无样本不生成记录。整轮完成后发布共用 sampled_at 的 targets 快照，随 full 上报。目标变化从零积累，相同目标改名、重排保留历史；旧 interval_seconds/window_seconds 字段仍可读取，但不覆盖固定周期与窗口。
