# 节点监控改造交接文档

日期：2026-11-02（Asia/Shanghai）

状态：设计已确认，待实施；本文不代表功能已经实现或发布。

配套：[实施计划与验收清单](MONITORING_REDESIGN_IMPLEMENTATION_PLAN.md)。

## 1. 交接范围与决策优先级

本文覆盖 Agent 采集、HTTP/WSS 指标传输、Panel 实时订阅、固定 30 秒历史聚合、独立字段存储和周表轮换。当前处于开发测试阶段，不要求兼容现有历史数据表，不保留、转换或迁移旧监控历史；新表按全新初始化设计。仅交付文档，不修改业务代码、数据库、运行服务或已发布协议。

决策优先级：本次用户最终确认 > 本文明确规则 > 旧文档中的监控目标。协议实施仍必须先修改 Panel 的 `api/agent/v1` 权威 Schema，再同步 Agent，不能把本文中的候选消息名当作已存在的接口。

### 1.1 已确认、实施时不得改变的规则

| 编号 | 决策 |
| --- | --- |
| D01 | 保留一条 Agent 主动出站的管理 WSS，持续承载心跳、任务、设置及实时模式控制 |
| D02 | 无节点实时需求时，指标采用 HTTP POST；进入实时指标模式后，同一管理 WSS 承载指标，暂停指标 POST |
| D03 | HTTP 模式：快速采集始终按 C，整数 1–60 秒、默认 30 秒；C 与上报间隔 R 独立，不支持 0，也不要求 C≤R |
| D04 | 实时模式：实际快速采集周期始终为 WSS 上报间隔 W，忽略 C |
| D05 | 历史基础聚合窗口固定 30 秒，与 R、W、网络请求次数完全解耦 |
| D06 | TCP 和 UDP 均沿用现有 sockstat/sockstat6 的 inuse 统计逻辑；不改为扫描 tcp/udp 明细文件 |
| D07 | 完整采集每 R 秒执行，实时模式仍定期刷新；WSS 两次完整采集之间复用缓存；公网 IPv4/IPv6 独立每 600 秒检测 |
| D08 | 单个待发送窗口最多 300 个快速样本；同时限制字节数；上报失败清空已取出的窗口，不做离线历史补传 |
| D09 | CPU、已用内存、已用 Swap 取样本 AVG；速度、磁盘 I/O、进程/TCP/UDP 数量取 MAX；累计量、容量、负载等取最新有效观测 |
| D10 | 历史指标使用独立类型字段；保留可合并统计信息；设备明细使用关联表，节点资产使用独立结构化表 |
| D11 | 月流量独立保存在 Agent 本地 traffic.dat，按 UTC 自然月累计，与历史监控窗口和发送成功与否无关 |
| D12 | 本周/上一周历史表按周日 UTC 00:00 轮换，正常物理覆盖约 7–14 天；API 最多查询最近 168 小时 |
| D13 | 管理连接保留较快指数退避；“60 秒起步、最长 5 分钟”不再作为管理 WSS 的重连策略 |
| D14 | 不停止代理业务、不接管第三方配置、不新增 Agent 入站管理端口、不引入通用远程 Shell |

补充范围约定：旧历史表和旧历史数据迁移不在实施范围内。保留新历史表结构、固定 30 秒聚合、周轮换与查询设计；协议兼容与新表自身的重启恢复仍需验证，二者不等于旧历史迁移。

### 1.2 工程默认值

以下是本方案的实施默认值，不是当前代码已支持的配置；协议阶段应把它们固化到 Schema、配置校验和测试。

| 参数 | 默认/上限 | 说明 |
| --- | --- | --- |
| C / collection_seconds | 默认 30，整数 1–60 | HTTP 模式独立的快速采集周期；0 非法，不与 R 联动 |
| R / http_report_seconds | 默认 60，整数 30–180 | POST 调度及完整采集周期；不再控制历史聚合窗口 |
| W / wss_report_seconds | 默认 2，整数 1–5 | 实时模式快速采集和发送周期 |
| 历史基础窗口 | 固定 30 秒 | 不暴露可编辑设置 |
| 实时订阅退出宽限 | 15 秒 | 最后一个订阅离开后延迟退出 |
| 实时租约 / 续期 | 90 秒 / 30 秒 | 不持久化实时需求，租约使用单调时钟计时 |
| 管理 WSS 重连 | 基础 1、2、4、8、16、30 秒，之后基础 30 秒 | 加入 [0, 基础/2) 随机抖动，实际最长约 45 秒；健康持续 60 秒后重置 |
| 快速 / 完整采集超时 | 5 秒 / 15 秒 | 独立任务，超时不堆积下一次采集；内核调用不能假称可硬中断 |
| POST 请求超时 | 15 秒 | 单次至多一个在途指标 POST，复用 HTTP Transport |
| POST body / 待发送窗口字节上限 | 各 1 MiB | 按编码后字节计；最多 300 个快速样本，独立缓存/在途对象也计入预算 |
| WSS 帧上限 | 64 KiB | 批次按字节拆帧，不能仅把样本上限从 10 改为 300 |
| WSS 写/指标接收确认超时 | 各 10 秒 | 接收确认是入站校验和接收成功，不是历史已经落库 |
| Panel 聚合刷盘 | 每 5 秒检查脏桶，单批至多 512 个主桶快照 | 明细另计入写入字节预算；同节点一致性检查点必须原子提交 |
| Panel 暂存时间 | 最近 10 分钟的已接收数据 | 失败有界重试，超过期限记录丢失；允许范围内的未来时间单独计入上限 |
| traffic.dat 定时保存 | 60 秒 | 周期切换、正常关闭额外保存；失败报告退化状态 |

沿用现有每节点 120 条/分钟的 WSS 消息限制，计算指标、控制、确认的总预算，不得每种消息各自获得 120 条额度。分方向实施预算，指标为控制流留出余量。规模与内存不是已经验证的性能承诺，详见实施计划。

## 2. 已核对的真实基线

最初核对时两仓库工作区均干净；本次修订时 Agent 仅有这两份未跟踪的交接文档。后续实施前必须重新检查。本节描述旧实现事实，不是新方案要求。

| 仓库 | 路径 | 核对提交 |
| --- | --- | --- |
| Agent | `/Users/Kang/project/viorka/viorka-agent` | `9f83e5d165b33432b3275f23bd4e6b1e0a4e9543` |
| Panel | `/Users/Kang/project/viorka/viorka` | `fedad58072b291eed69dce4a19ac0620ce1451bc` |

主要事实及入口：

| 现状 | 源码入口 |
| --- | --- |
| Agent 已有注册、签名会话、WSS、心跳、任务和在线设置处理；没有指标 POST 路由客户端 | [client.go](../internal/connection/client.go) |
| C=0 当前始终使用 W；在线窗口最多 10 条，离线只保留最新快照 | [settings.go](../internal/connection/settings.go)、[sampling.go](../internal/connection/sampling.go) |
| 当前同一次采集会读取 CPU、内存、负载、磁盘、网络、主机计数等 | [metrics.go](../internal/collector/metrics.go)、[host_metrics.go](../internal/collector/host_metrics.go) |
| TCP/UDP 使用 sockstat{,6} inuse；磁盘 I/O 已有 bytes/s，尚无本方案要求的完整 IOPS/Await/Util | [host_metrics.go](../internal/collector/host_metrics.go) |
| 文件系统已使用 mountinfo 和受限 statfs、去重和过滤 | [filesystems.go](../internal/collector/filesystems.go) |
| OS 和公网 IP 当前同一个资产任务，正常每 5 分钟刷新 | [inventory.go](../internal/collector/inventory.go)、[sampling.go](../internal/connection/sampling.go) |
| 身份、设置有受限权限和原子写入；没有月流量账本 | [store.go](../internal/state/store.go) |
| Panel 当前历史表是 metric_rollups，stats_json 保存 sum/max/count；新桶已是 30 秒，兼容旧 60 秒桶；R 当前控制批量刷盘 | [history.go](../../viorka/internal/monitoring/history.go)、[metrics.go](../../viorka/internal/storage/metrics.go) |
| 前端已有节点/列表 SSE，隐藏页面会关闭流；目前订阅不控制 Agent 采集模式 | [metric_events.go](../../viorka/internal/httpapi/metric_events.go)、[use-live-metrics.ts](../../viorka/web/src/hooks/use-live-metrics.ts) |
| 新建节点会话会删除该节点已有会话，重复单独认证可能踢掉 WSS | [agents.go](../../viorka/internal/storage/agents.go) |
| 历史查询已有 1h/6h/24h/2d/7d；数据库迁移当前到 0009 | [metric_history.go](../../viorka/internal/httpapi/metric_history.go)、[migrations](../../viorka/migrations) |

Go 两仓库均为 1.26.0。Agent 当前仅依赖 coder/websocket；Linux 采集优先沿用标准库和 procfs，不为本次改造引入 gopsutil。现有部分文档的“尚未接入前端/历史”“分钟桶”等描述落后于源码，实施时更新，不沿用其陈旧结论。

## 3. 总体数据流与职责

```text
浏览器节点/列表指标 SSE
        │ 授权后的节点订阅引用计数
        ▼
Panel 实时需求管理 ──管理 WSS：启用/停用/续期──► Agent 调度器
        ▲                                          │
        │                                          ├─快速采集：HTTP 用 C，实时用 W
        │                                          ├─完整采集：R
        │                                          ├─公网 IP：600 秒
        │                                          └─流量账本：traffic.dat
        │
Panel 统一指标入口 ◄── HTTP POST 或同一 WSS 的指标流 ─┘
        ├─验证身份、能力、大小、时间、样本身份和去重
        ├─实时最新快照 / SSE
        └─30 秒 UTC 聚合 ─► 独立字段周表 ─► 最多 7 天历史 API
```

- 浏览器只连接 Panel；Agent 不监听浏览器连接。
- 管理 WSS 是否连接与指标使用哪种出口是两个状态，不能使用一个布尔值代替。
- Agent 调度器唯一拥有采集节奏、未发送窗口、发送选择和模式代次；WSS writer 唯一写 socket。
- Panel HTTP/WSS 两个接收入口调用同一个校验、去重、实时更新和历史聚合服务。
- 月流量累计是采集状态，不依赖 Panel 是否可达、是否有人看、是否上报成功。
- 遵循现有单 Panel 进程/SQLite 模型；本次不新增多实例订阅协调或分布式聚合承诺。

## 4. 采集调度和缓存

### 4.1 实际周期

| 指标模式 | 快速采集 | 发送 | 完整采集 | 公网 IP |
| --- | --- | --- | --- | --- |
| HTTP | C，整数 1–60 秒，默认 30 秒 | 每 R 秒 POST，携带窗口样本；无新样本可复用最新快照 | R | 600 秒 |
| 实时 WSS | W，忽略 C | 每 W 秒 WSS | R，发送时可复用缓存 | 600 秒 |

启动立即建立一次采集基线。周期起点使用单调时钟；UTC 时间只用于传输、历史归桶和日历周期。使用有界、不重入的调度，慢采集不会触发补跑风暴。

实时模式采集完成后交给发送器，避免同周期两个无序定时器总是发送上一份数据。采集未完成时允许跳过或发送原快照，但保留原 ID/时间，不能把缓存伪装成新样本。HTTP 截止时取窗口内已完成的采集，不等待完整采集阻塞心跳。

切换模式、修改 C/R/W、WSS 重连都不重建 CPU/网卡/磁盘计数基线。首个不同周期的速率仍按真实 elapsed 计算，不能用新配置值当分母。设置应用只重排受影响的后续 deadline，不补跑过去时刻：HTTP 下修改 R 不改变 fast 的周期和相位，只调整 POST/full；修改 C 才调整 fast。实时模式下修改 C 仅保存，退出实时后生效，修改 W 才调整实时 fast/发送。

HTTP 下允许 C 大于、等于或小于 R。例如 C=60、R=30 时每 60 秒采集、每 30 秒 POST；没有新 fast 样本时发送最新快照并保留原 ID/时间，Panel 不重复计数。定时 POST 和故障兜底都不能为了“最新”而额外触发 fast 采集。默认 C=30、R=60 时一次 POST 通常携带约 2 个 fast 样本；初始 CPU/速率仍需第二个有效样本才能计算。

### 4.2 指标分组

| 分组 | 内容 | 原始观测身份 |
| --- | --- | --- |
| fast | CPU、内存、Swap、网卡计数/速度、TCP/UDP inuse、运行时长；月流量视图 | Agent 本次进程 run_id + fast_seq + sampled_at |
| full | 去重文件系统容量、系统负载、进程数、设备磁盘 I/O、OS/系统信息 | run_id + full_seq + full_sampled_at；I/O 带 elapsed |
| public_ip | IPv4/IPv6 出口探测和分族错误 | run_id + ip_seq + ip_sampled_at |

run_id 每次 Agent 进程启动随机生成。它不同于宿主机 boot_id：同一宿主机上 Agent 重启不能复用已归零的序号。boot_id 用于本地网卡/磁盘及月流量恢复判断。传输层同一份原始观测永远保留同一个 ID。

完整组或 IP 组附在多个 WSS 快速样本中时，Panel 只按其自身身份和时间处理一次。缓存仅用于实时组合展示，不产生新的历史观测。可以在新协议中把 fast 样本数组、full/IP 更新分别编码，避免每秒重复发送完整设备数组。

公网 IP 检测继续使用已存在的固定 ipify 地址、TLS 和地址族校验，不接受 Panel 任意 URL。只把刷新周期改为 600 秒；每族独立报告失败，失败不把旧地址冒充新结果。完整任务首次执行后，常量系统信息可缓存，只需按周期验证必要变化。

### 4.3 指标定义

| 指标 | 数据来源与算法 | 未知/重置处理 |
| --- | --- | --- |
| CPU | `/proc/stat` 前八项差值，不重复加 guest；idle+iowait 视为空闲；`100×(Δtotal−Δidle)/Δtotal` | 首次、倒退、总差值为零、读取失败时无百分比；展示两位小数 |
| 内存 | `/proc/meminfo`：MemTotal−MemAvailable | 缺字段/不合法则未知，不用 MemFree 代替 |
| Swap | SwapTotal−SwapFree | 无 Swap 时总量/已用量可为 0，使用率为未知 |
| 网络 | `/proc/net/dev` 每个被选网卡 RX/TX 计数；速度为各网卡有效差值/真实秒数，再相加 | 首次、新网卡、计数回退、恢复失败时重建该网卡基线；不产生负值或尖峰 |
| TCP | `/proc/net/sockstat` 和 `sockstat6` 的 TCP/TCP6 inuse 合计 | 包含监听、不包含 TIME_WAIT；不是仅 ESTABLISHED；保持现有限制和错误语义 |
| UDP | 同上 UDP/UDP6 inuse 合计 | 为套接字数，不宣称 UDP 会话数；无 IPv6 内核允许缺 sockstat6 |
| 进程 | 分批枚举 `/proc` 顶层数字 PID 名称 | 当前命名空间可见进程数；不递归线程；权限导致视野受限时明确标注 |
| 负载 | `/proc/loadavg` 1/5/15 分钟 | full 时间，不随 fast 重新打时间戳 |
| 文件系统 | `/proc/self/mountinfo + statfs`，复用已实现的本地文件系统白名单/去重 | 不扫描目录树；拒绝网络/FUSE/autofs；失败不得把部分和当完整总量 |
| 磁盘 I/O | `/proc/diskstats`，每设备在 full 周期内取差值 | 首次、设备替换、计数倒退、不可用不产生速率；设备按身份分别处理 |

所有速率使用对应计数器实际采样间隔；沿用正常差值最大 600 秒约束，超出重建速率基线。缺值通过 nullable 字段和固定错误码表达，不能填零、NaN、Infinity。

### 4.4 磁盘容量和 I/O 明细

容量保留 total、used、available 三个字节字段；used=(blocks−bfree)×block_size，available=bavail×block_size。总览使用率沿用已实现的 `ceil(100×used/(used+available))` 口径，分母为零则未知。根文件系统旧系列与全本地文件系统合计是不同语义，不能无标识拼接历史。

Linux diskstats 的扇区统一按 512 bytes 换算：

- read/write bytes/s = 对应扇区差值 × 512 / elapsed_seconds。
- read/write IOPS = 对应完成 I/O 次数差值 / elapsed_seconds。
- read/write await_ms = 对应耗时毫秒差值 / 对应完成次数差值；完成次数为 0 时为 NULL。
- combined await_ms = (Δread_ms+Δwrite_ms)/(Δread_ios+Δwrite_ios)，分母为 0 时为 NULL。
- util_percent = 100 × Δio_ticks_ms / elapsed_ms；仅为内核设备忙碌时间口径，不等于 SSD/NVMe 带宽饱和度。非合理区间需报质量问题，不能无声钳制掩盖错误。

每设备保存和展示，不把整盘、分区、LVM/dm、RAID 层重复相加，也不把各设备 await/util 相加。首版不生成未经拓扑验证的“全机 I/O 总量”。设备名称/major:minor 只是可见身份的一部分，热插拔后需要重建基线，不能假称永久硬件 ID。

full 周期为 60 秒时，I/O 速率表达的是约 60 秒区间平均；归属到 full_sampled_at 所在的 30 秒桶。历史 MAX 只代表已观测区间速率的最大值，不代表桶内秒级峰值。不得把这一个区间平均复制到两个 30 秒桶。

## 5. 网络接口与月流量账本

### 5.1 网卡选择

默认排除 `lo`、`docker*`、`veth*`、`br*`、`tun*`、`wg*`、`tailscale*`、`zerotier*`、`tap*`。这是启发式默认值，不保证适用于所有 VPS。

新增受约束的本地包含/排除列表：先应用默认规则，再应用显式包含，最后显式排除；不执行 Shell，限制条目数/长度，仅支持字面名字及末尾 `*` 前缀匹配。首期配置最多各 64 项，每项最多 64 字节。修改统计规则生成 filter_revision，旧规则统计不能静默拼接为相同口径。

通过 ifindex、接口名和宿主机 boot_id 识别基线；允许明确包含桥接/隧道接口，但不能宣传其合计等于服务商计费流量。某个选中接口读取失败时，合计标注不完整，不能把部分速率当完整主机速度。

主机“累计流量”是当前选中接口内核累计计数的和，可能因重启、接口变化而下降。月流量是账本累计的非负增量，二者分开命名、存储和展示。

### 5.2 traffic.dat

位置固定为 `state_dir/traffic.dat`，由 Agent 控制文件名，禁止远程指定路径。目录 0700，文件 0600，按当前原子写入和目录同步原则实现；文件内使用有版本的结构化格式，`.dat` 不要求自定义二进制编码。

至少包含：format_version、Panel/Node 绑定、UTC month_start、宿主机 boot_id、filter_revision、每接口身份与最后计数、月 RX/TX 累计、saved_at、完整性/退化状态。整数使用无损解析和溢出检查。

| 情况 | 处理 |
| --- | --- |
| 正常采样 | 逐接口计算增量后累计；不对合计计数直接相减 |
| Agent 重启、主机未重启、网卡未变 | 从同一原子快照恢复基线和累计，可用当前计数补入尚未保存的差值，避免重复累计 |
| 主机重启/接口替换/计数倒退 | 保留已持久化月累计，建立新基线；不把负差值修成零后声称准确恢复 |
| 跨 UTC 月 | 先确定统计周期，再更新账本；跨界间隔无法精确切分的增量归入本次采样所在月，标记 boundary_estimated |
| 停机跨多个月 | 不能把整个停机期间未知归属的差值全记本月；重建基线并标记 period_gap |
| 修改网卡规则 | 保留已累计部分，更新 revision，新纳入接口先立基线；标记本月包含不同规则阶段 |
| 文件损坏/权限不安全/绑定不匹配 | 不覆盖原文件或假装月累计为 0；月流量报告 unknown/degraded，其他只读采集和管理可继续 |
| 写盘失败 | 保留内存累计并报告持久化退化；60 秒限频重试，不能宣称已耐久保存 |
| 正常退出 | 有界完成最后一次保存；超时如实记录，不无限阻塞关闭 |

月流量差值恢复和瞬时速率基线分开：长间隔可能仍能恢复同一主机/月份的累计差值，但不输出失真的实时速度。主机断电导致计数清零、上次保存后的未知流量和边界误差不得被描述为精确计费。

## 6. 实时订阅与传输状态机

### 6.1 Panel 订阅归属

复用现有 `/api/v1/metrics/events` SSE：`node=...` 为节点详情需求，`through=...` 为现有列表作用域需求。只对服务端解析并授权成功、实际纳入订阅集合的节点增加引用计数。列表范围沿用现有已加载节点集合，不能因为全局 `/api/v1/events` 存在就提升全部节点。

同一节点多个用户/页面共享一份 Agent 实时流。节点列表动态变更、权限撤销、用户退出、SSE 断开、隐藏页面关闭流均释放对应引用；服务端检查权限，不能信任客户端自报订阅数量。只打开历史查询、不订阅实时指标，不触发提频。

首个引用触发启用；最后一个引用释放后进入 15 秒宽限，有新引用则取消退出。到期发送停用。订阅由进程拥有，不持久化；Panel 重启从真实 SSE 重连恢复需求。

### 6.2 租约

控制命令绑定当前管理会话代次和单调递增的 demand_revision，包含 enabled、lease_id、ttl_seconds。Agent 回执实际模式和已应用的 demand_revision。迟到的启用/停用/续期不能覆盖新代次。

Agent 收到有效启用后启动 90 秒单调时钟租约；Panel 有需求时每 30 秒续期。没有新需求时不续期。命令和回执超时不能显示“实时已启用”；Panel 可显示 pending/degraded，并按有界策略重发当前状态。旧租约在连接断开、会话换代、Agent 重启后失效。

### 6.3 状态迁移

管理连接状态和指标模式分别维护；以下表描述指标模式。

| 触发 | 原模式 | 新模式与行为 |
| --- | --- | --- |
| 新能力已确认、无实时需求 | 任意初始状态 | HTTP；已保存设置生效；不以管理 socket 存在为暂停 POST 的理由 |
| 有效启用命令 | HTTP | 暂停新 POST 调度，等待/取消已有 POST（至多其 15 秒 deadline），转移未尝试窗口到 WSS 出口，应用 W，再发送模式回执 |
| 宽限后停用或租约过期 | realtime | 保留未尝试样本和计数基线，应用 C/R，恢复 POST 调度；窗口到期可发送，刚发送过则等待至 R 下限 |
| WSS 写失败/接收确认超时/普通断线 | realtime | 丢弃本次已取出的失败窗口，退出 realtime，恢复 HTTP；符合条件时尝试一次最新快照兜底 |
| 管理 WSS 重连 | HTTP | 管理先恢复，按 Panel 当前需求重新协商实时；不自动沿用旧租约 |
| 指标熔断 | 任意 | 停止指标尝试；采集、月累计、完整缓存继续，窗口不无限积压；管理心跳和合法任务继续 |
| 永久身份/TLS 阻断 | 任意 | 同一身份相关 HTTP/WSS 均停止；不重新 Enrollment 绕过；代理业务不受影响 |

模式切换由协调器串行处理。单次已在途 POST 的结束属于切换过程，不视为“双路常态上报”；启用回执必须在进入新模式后发送。没有在途 POST 时应快速启用，验收目标为两次 W 周期内看到首个有效实时点；有在途/超时则按状态如实展示。

### 6.4 窗口所有权及发送结果

- pending 窗口最多 300 个快速样本/1 MiB；发送器原子取走窗口后，采集器立即使用新窗口。
- 取走窗口与在途请求分别有上限；实现需记录实际最大内存，不能只限制 pending 而漏掉复制和编码缓冲。
- 成功、拒绝、网络失败、超时均释放该次取走的窗口；拆帧中途失败则丢弃该次窗口剩余部分。
- 没有发送机会的冷却期间，每个 R 边界释放积压，只保留最新快照；恢复时先发最新，不把冷却积压当补传队列。
- 上限溢出丢最旧样本，记录丢弃原因/数量；未发生发送的窗口在正常模式切换时可转交另一出口，不算失败补发。
- WSS 失败立即 POST 兜底时，使用最近已完成的 fast 快照；如恰有调度产生的新样本则使用它，否则保留缓存原 ID 供 Panel 去重。兜底不额外触发 fast 采集，也不能把失败窗口重新提交。
- POST 最小尝试间隔 R 以尝试开始时间计算，包括兜底；频繁断线/模式切换不能绕过。
- 网络发送成功但无确认不算“服务端已接受”。指标不提供 exactly-once 或持久化不丢失保证。

## 7. 失败分类、退避与熔断

| 分类 | 指标处理 | 管理 WSS |
| --- | --- | --- |
| DNS/TCP/超时等普通网络失败 | 本窗口丢弃；POST 下一次至少距上次尝试 R | 基础 1→30 秒指数退避加抖动 |
| WSS 指标写失败 | 尝试一次符合限频/认证/熔断条件的 POST 最新快照 | socket 不可用时进入重连 |
| POST 429 | 遵守 Retry-After，同时满足 R 下限；无法解析时按受控拒绝处理 | 不因纯指标限流关闭健康管理连接 |
| 5xx/明确暂时不可接收 | 计入连续拒绝，走指标熔断 | 心跳/任务可继续 |
| 暂时协议异常/会话失效 | 相关传输冷却 120 秒，再受控协商或刷新；失败窗口不补发 | 若仅指标协议异常，保留可用管理功能；会话级异常则暂停双方认证使用 |
| 明确不支持新协议/POST 路由不存在 | 停止该新功能，等待重新协商并回退已确认旧协议；不每 R 秒探测不存在接口 | 使用已确认管理协议 |
| 签名错误、节点吊销/禁用、身份替换 | 永久阻断该身份，需本地修复或明确重新注册流程 | 不能切换通道规避 |
| TLS CA/hostname 校验失败 | 停止该 Origin 的请求，报告错误 | 不跳过 TLS 校验 |

“连续拒绝”指经认证、可归类的指标拒绝/服务端不可用响应；普通网络超时单独计数。认证错误不应被泛化为可通过普通熔断解决的拒绝。

| 连续拒绝次数 | 指标暂停时间 |
| --- | --- |
| 1–2 | 下一次按 R 尝试；结束实时发送以免每 W 秒重复拒绝 |
| 3 | 2 分钟 |
| 4 | 3 分钟 |
| 5 | 5 分钟 |
| 6 | 7 分钟 |
| ≥7 | 10 分钟 |

两个指标出口共享拒绝计数、熔断时间和身份阻断状态。到期只允许一个半开探测，默认用 POST 最新快照；成功接受后清除指标拒绝计数，再根据有效实时需求恢复实时发送。旧租约已过期则必须重新授权实时模式。心跳成功、TCP 连接成功、其他接口成功均不能清除指标拒绝计数。

最终可尝试时间取 R 限频、120 秒协议冷却、熔断截止、Retry-After 中最晚者。Retry-After 可长于 10 分钟，10 分钟只是本地熔断阶梯上限。

## 8. 身份、协议与兼容

### 8.1 HTTP 身份复用

POST 与 WSS 使用同一节点身份、同一严格 TLS 策略和协调后的短期会话。凭证只放 Authorization header，不放 URL、样本、日志或公网 IP 探测请求。

现有 Panel 的 ConsumeChallenge 会删除该节点旧会话，因此必须增加 Agent 进程级会话协调：缓存 token/expiry、串行刷新、阻止并发 challenge、供 POST 和 WSS 共同使用。刷新会话需要计划一次管理重连，旧 token 的延迟响应不能把新会话误判吊销。不能让每次 POST 都单独创建会话。

首版不引入第二套永久监控令牌或无签名 node_id 认证。POST 不要求当下 WSS 在线，但要求已经协商过对应能力并持有可验证的节点会话；首次启用新能力通过管理握手完成。Panel 重启后的会话恢复仍使用现有签名挑战，不能用注册令牌代替。

Agent 现有 post() 把所有 401/403 当永久身份拒绝，新实现需根据受版本约束的安全错误码区分过期/可刷新与真实吊销；不能放宽已有拒绝边界。

### 8.2 新协议轮廓

以下路径/名字为待 P0 在权威 Schema 中定稿的候选，当前并不存在：

| 契约 | 候选形式 | 必须包含的语义 |
| --- | --- | --- |
| 新采集设置 | node.settings.v3 / 独立 telemetry_settings | C、http_report_seconds、W、revision；明确 auto_http_wss 行为 |
| 双出口指标 | node.metrics.v3 | fast 样本、full/IP 更新、身份/序号、单位、组错误、各组真实采样时间 |
| POST | POST /api/v1/agents/metrics | Agent 鉴权、最大 300 条/1 MiB、同 WSS 的验证与去重 |
| 实时需求 | node.metrics.demand / result | enabled、会话代次、demand_revision、租约和实际模式 |
| 接收确认 | node.metrics.receipt / POST 响应 | batch_id、accepted/rejected、accepted_count、duplicate_count、稳定错误码 |

新语义不能塞回旧 NodeSettings 并继续宣称 v2。特别是 report_seconds 从“刷盘周期”变成 HTTP 周期，必须在新结构中明确命名和版本；旧字段/旧样例保留原意义。外层协议能否继续 1.0，由 P0 证明兼容性后确定，不能为了少改代码跳过版本化。

采用全批结构校验：非法类型、超限、跨节点、错误时间等在进入状态前拒绝整批；合法重复样本不累计，响应计入 duplicate_count。协议对整数、JSON 深度、未知字段、设备数、路径长度、错误枚举和非有限数字均有限制。数据量超限不能只依赖反向代理配置。

时间校验需适应 HTTP 窗口：fast 原始采样允许最早为 `received_at − (R + 300秒 + 15秒)`，最晚为 `received_at + 300秒`；R 最大 180 时过去上限为 495 秒。full/IP 缓存另按自身周期和身份验证，不能用 fast 的 5 分钟旧限制拒绝合法 10 分钟 IP 缓存。过期缓存可显示 stale，不作为新观测入历史。

### 8.3 去重和重启恢复

唯一观测身份为 `(node_id, run_id, group, sequence)`；batch_id 仅标识提交，不能替代样本身份。跨 HTTP/WSS 使用同一序号空间的同一组，不允许切换出口时重新编号。

Panel 每组按序处理：批内序号递增；重复或低于已处理水位的观测不累计。对迟到低序号采用明确 stale/duplicate 计数，而非无界重排序；这符合允许丢样本、不补发的约定。UTC 时间回拨不改变序号顺序，但仍需满足时间范围校验。

持久化高水位与对应历史桶、设备明细或资产更新在同一事务提交，不能先保存高水位、后漏掉历史；重启恢复已提交桶和水位，不能把旧样本重新累加。单次提交按节点建立一致快照：包含该检查点覆盖的全部脏桶/明细，不按 512 条 SQL 机械截断；512 是主桶数量上限，不是明细行数上限。若放不下一个节点的完整一致快照，将该节点留到下一批，不先提交半份水位。实际事务字节/时间上限在 P1 用最大设备数测试确定，超预算需明确报告，不能无限延期。

尚未提交内存数据可能在 Panel 崩溃时丢失，接收 ACK 不能承诺其耐久性。对新 run_id 的接受绑定当前认证会话；失效会话的旧 run_id 不得无限占用缓存。每节点最多保留 4 个活动/近期 run_id 的检查点集合，非活动代次经过 10 分钟接收窗口后可淘汰；淘汰代次必须同时失去上传资格，不能把其旧样本作为首次接收重新入库。

### 8.4 兼容矩阵

| Panel / Agent | 行为 |
| --- | --- |
| 新 Panel / 旧 Agent | 保持旧 WSS 指标和旧设置语义，不发送实时控制、新字段或 POST 要求；新表记录 legacy 口径 |
| 旧 Panel / 新 Agent | 只运行已协商的旧 WSS 采样路径，不尝试未知 POST；不能把新 C=1–60/默认30 的设置语义强加给旧协议 |
| 新 Panel / 新 Agent | 新设置、新样本、新租约及双出口全部确认后启用 |
| Panel 回退/新功能被禁用 | 结束新实时租约，重新协商受支持路径；不死循环探测 POST，不静默丢失管理功能 |

v3 设置单独版本化保存，避免把 v3 字段写入旧二进制严格解码的 node-settings.json。可采用新受控文件 node-telemetry-settings.json；兼容路径保留旧文件，身份绑定/权限/原子写入仍一致。新能力未全部就绪时不宣告支持。

## 9. 固定 30 秒历史聚合

### 9.1 归桶和写入时机

`bucket_start = floor(sampled_at_utc_unix / 30) × 30`，窗口为 `[bucket_start, bucket_start+30秒)`。边界时刻属于下一个桶。显示时间可用最后一个有效样本时间，但主键必须使用 bucket_start。

网络批次不是历史桶：一次 R=60 的 POST 可覆盖两个或更多边界桶；R=180 可覆盖约六个桶；跨界起点不同可多一个部分桶。WSS 每秒发送也只更新所属的 30 秒桶。未来/迟到样本在允许范围内按原始时间处理，不按 received_at 归桶。

Panel 每 5 秒检查可刷新的脏桶，批量保存绝对统计快照；已结束桶可以因合法延迟到达的 HTTP 样本继续更新，不在 30 秒刚结束时永久封桶。历史有到达延迟：HTTP 模式最长通常约 R 加网络与刷盘延迟；固定 30 秒不等于每 30 秒必定已有数据库数据。

### 9.2 聚合规则

| 字段组 | 桶内统计 | 较长时间范围再聚合 |
| --- | --- | --- |
| CPU、RAM used、Swap used | 各自 sum/count，API 输出 AVG | 分别合并 sum 和 count 后求均值，不对均值再求简单均值 |
| net RX/TX speed | MAX，另存有效计数/质量信息 | MAX |
| 每设备 I/O 速度、IOPS、Await、Util | 各字段 MAX，保留设备身份和原始观测时间 | 每设备逐字段 MAX；不同设备不能相加 await/util |
| TCP/UDP、进程数 | MAX | MAX |
| RAM/Swap total、文件系统容量 | 最新有效观测 | 按各自观测时间选最新 |
| 网卡累计、月累计、负载、运行时长 | 最新有效观测 | 按各自观测时间选最新；跨月最新月累计不等于整个查询期流量 |
| IP、OS 等资产 | 更新资产表 | 如需过去变化，查询独立资产变更记录；不复制进每个历史桶 |

AVG 是样本均值，不是时间加权均值。HTTP/WSS 切换导致频率变化时，高频时段样本权重较大；首版按已确认规则保留此定义。COUNT 逐指标保存，不用一个 sample_count 给所有字段当分母。

最新值不回填：同一 full 缓存反复附带到 fast 报文中，只更新一次 full_sampled_at 对应桶；没有 full 观测的桶其 slow 字段为 NULL。实时界面可以组合最新 slow 缓存，但必须显示真实时间/过期状态。

### 9.3 必须固定的示例

| 场景 | 正确历史结果 |
| --- | --- |
| C=2，R=60，W=1，无实时订阅；样本时间为 00:00:00、02…58 | 一次 POST 生成两个 30 秒桶，每桶约 15 个 fast 样本 |
| 默认 C=30，R=60，无实时订阅 | 每 30 秒采集一次，一次 POST 通常携带约 2 个 fast 样本，分别进入其真实所属桶 |
| C=60，R=30，无实时订阅 | 每 60 秒才有一个新 fast 样本；中间 POST 复用原快照不重复计数。full 每 30 秒可生成 slow-only 桶，其中 CPU/网速等 fast 字段为 NULL，不插值 |
| W=1，有实时订阅，00:00:00…29 | 一个桶约 30 个 fast 样本，无论发送了多少次 |
| full 每 60 秒一次，W=1 | 容量/负载/进程/I/O 只在真正 full 观测所属桶出现，不能把缓存算 60 次 |
| 一个桶有效 CPU 为 10、20、30，另一个字段只有 2 个有效点 | CPU AVG=20，其他字段使用自身 count；失败样本不按 0 参与 |
| 两桶 CPU 分别 sum=300/count=30、sum=90/count=1 | 合并 AVG=390/31，而非 (10+90)/2 |
| 同一 fast 样本经 WSS 接受后又出现在 POST 最新快照 | 只统计一次，重复提交不增加 count |

每 30 秒无样本不创建占位行。可以有仅 slow 观测的桶，fast_sample_count=0，对应 fast 指标 NULL；不能为凑固定频率生成虚假 CPU/网速。

## 10. 数据表目标结构

以下为全新建库的字段级设计，不是可直接执行的 SQL；不提供旧历史表转换规则。主历史指标不使用 stats_json。字段单位写进 Schema/OpenAPI；整数无损传输，前端若可能超过 Number 安全范围应采用十进制字符串 DTO，不可静默损失精度。

### 10.1 主表 metrics_history / metrics_history_old

两张表保持相同列、约束和索引定义。

| 字段 | 类型/规则 | 含义 |
| --- | --- | --- |
| node_id | TEXT，外键 | 节点；删除节点级联清理 |
| bucket_start | INTEGER，UTC Unix 秒，30 秒对齐 | 与 node_id 组成主键 |
| window_end | 由 bucket_start+30 得到 | 可由查询计算，避免可写冗余 |
| last_sample_at | INTEGER，UTC 毫秒 | 桶内最后一个实际观测时间 |
| fast_sample_count / full_sample_count | INTEGER ≥0 | 各组去重后的真实观测数 |
| metric_semantics | 受约束版本标识 | 区分新网卡规则、旧根盘口径等，不是任意字符串；桶内规则改变另标 mixed_semantics |
| quality_flags | 受约束位标记 | 部分采集、规则变化等；网络丢样本计数不能无依据摊给每桶 |
| cpu_sum / cpu_count | REAL / INTEGER | CPU 样本均值的充分统计量；count=0 时 sum=NULL |
| ram_used_sum / ram_used_count | REAL / INTEGER | 已用内存字节累计及有效样本数 |
| swap_used_sum / swap_used_count | REAL / INTEGER | 已用 Swap 字节累计及有效样本数 |
| ram_total / swap_total / memory_sampled_at | INTEGER，nullable | 最新有效总量及对应采样时间 |
| net_in_speed_max / net_out_speed_max | REAL，nullable，bytes/s | 窗口峰值；各自附有效 count |
| net_in_total / net_out_total / net_sampled_at | INTEGER，nullable | 选中网卡内核累计及时间 |
| net_month_in / net_month_out / traffic_month_start / traffic_sampled_at | INTEGER，nullable | UTC 月账本视图及时间 |
| traffic_quality / traffic_filter_revision | 受约束类型，nullable | 累计完整性和网卡选择规则版本 |
| tcp_count_max / udp_count_max / process_count_max | INTEGER，nullable | 现有 inuse/进程口径 MAX；分别附有效 count |
| disk_total / disk_used / disk_available / disk_sampled_at | INTEGER，nullable | 去重本地文件系统合计及对应 full 时间 |
| load_1 / load_5 / load_15 / load_sampled_at | REAL，nullable；时间为 INTEGER | 最新 load 观测 |
| uptime_seconds / uptime_sampled_at | REAL / INTEGER，nullable | 最新运行时长 |
| updated_at | INTEGER | Panel 写入时间，不代替采样时间 |

API 可以输出用户期望的 cpu、ram_used、swap_used、net_in_speed、net_out_speed，分别由 sum/count 或 max 映射；数据库用统计含义明确的列名。NULL 表示没有观测，0 表示有观测且值为零。不能把主字段设 DEFAULT 0 混淆二者。

主键 `(node_id,bucket_start)` 用于单节点范围查询；另建 bucket_start 索引用于跨节点时间维护。所有表名由程序固定枚举，查询参数不得拼出任意表名。

### 10.2 明细和辅助表

| 表族 | 主键/核心字段 | 生命周期 |
| --- | --- | --- |
| metrics_disk_io / metrics_disk_io_old | node_id、bucket_start、device_id；read/write bytes/s、IOPS、read/write/combined await、util 的 max/count、last_sample_at | 与对应周表同事务轮换；设备级统计 |
| metrics_filesystems / metrics_filesystems_old | node_id、bucket_start、filesystem_id；mount/type、total/used/available、sampled_at | 只在真实 full 观测产生记录，最多 32 个文件系统 |
| node_system_info | node_id；OS/内核/架构等已有或明确新增字段、sampled_at、错误状态 | 当前资产快照，不随周表轮换 |
| node_public_addresses | node_id、family；address、sampled_at、error_code | 当前 IPv4/IPv6 快照 |
| node_asset_changes | node_id、change_time、类型化变化字段 | 仅记录实际变化；明确保留策略，首期最多 14 天 |
| metric_ingest_checkpoints | node_id、run_id、group；committed_sequence、对应时间 | 与聚合写入原子更新；有界保留活动/近期代次 |
| metric_history_partitions | 固定分区角色、week_start、schema_version、轮换状态 | 判断周归属，不靠表名猜测日期 |

辅助表也使用类型字段，不能把 stats_json 迁到另一个 payload_json 后声称“全部展开”。未请求的新系统信息字段不能因为表存在就凭空采集。设备标识仅用于观测关联，不授予管理设备/路径权限。

可选的资产变更表用于过去 IP/系统变化查询；如果产品不提供此查询，实施可先只落当前快照，但必须在交付范围中明确，不把当前资产当历史值返回。

## 11. 周轮换、查询和开发测试初始化

### 11.1 周边界和查询

每周日 00:00 UTC 是周起点。北京时间对应周日 08:00，但逻辑始终用 UTC；不能依赖操作系统当地时区。

- metrics_history 保存当前 UTC 周，metrics_history_old 保存上一 UTC 周；明细表同时轮换。
- 正常轮换后物理覆盖约 7 天，下一次轮换前接近 14 天；API 无论物理存量都只允许最近 168 小时。
- 查询同时扫描两张表并合并，不以“跨周”为理由漏点；原始桶以 bucket_start 归属周。
- 固定 30 秒是底层粒度；保留现有查询粒度：1h→30秒、6h→180秒、24h→720秒、2d→1440秒、7d→3600秒。
- 粗粒度查询合并 sum/count、MAX 和按时间 latest，不能为缺失桶补零或插值。
- 查询起点落在桶中间时仅能以完整原始桶精度返回，响应说明边界裁剪规则，不能假称恢复桶内任意时刻原始样本。
- 节点管理 offline 不代表最新 POST 指标无效：API 分别输出 management_online、metrics_received_at、组 sampled_at/stale；HTTP 成功不伪造可下发任务的在线状态。

fast 过期阈值建议 `max(90秒, 3×当前有效采集周期, 3×当前发送周期)`；full 使用自身周期，IP 使用 600 秒周期。management_online 与指标新鲜度分开判定，修正当前 SSE 把非 online 节点一概标 stale 的行为。

### 11.2 轮换算法

启动以及运行时定时检查当前 UTC week_start，并与分区元数据比较：

1. 本周一致：无需操作。
2. 正好跨一周：在受控写入锁和事务内删除最旧分区、把本周分区转为上一周、创建新本周及明细、索引，更新元数据。
3. 跨过两周及以上：原分区均超出当前/上一周范围，按保留策略清理并建立正确分区，不把多周前数据误标为“上一周”。
4. 系统时钟后跳：不倒转分区、不破坏已有数据；记录异常，按已建立周归属验证写入。

轮换期间接收可以进入有界内存，实际写入串行协调。跨周迟到的合法样本根据 bucket_start 写上一周表；迟到超出保留范围则记录并拒绝历史写入。表重命名对 SQLite 外键、索引名、预编译语句和明细引用的影响必须真实集成测试，不能仅测试 SQL 字符串。

不在每次轮换后执行全库 VACUUM；删除后页面可被复用，数据库文件不承诺立即缩小。正常保留期之外的物理数据在停机时可能暂时存在，启动追赶后恢复规则。

### 11.3 开发测试初始化，不迁移旧历史

- 直接以第 10 节的新表为目标更新监控建库定义、存储层、查询和测试夹具；可以按开发阶段整理监控相关建表 SQL，不建设旧数据生产升级链。
- 全新数据库初始化必须一次完成主表、明细、资产、检查点、周分区元数据和索引，并经过真实 SQLite 测试。
- 不导入 metric_rollups/stats_json，不保留旧 30/60 秒桶、legacy 历史表、旧历史查询适配器或分钟边界双写切换流程。
- 不做旧历史对账、回填、迁移备份/恢复演练，也不要求等待旧数据超过 7 天后才清理兼容代码。
- 新历史只从新实现实际接收的观测开始产生。旧 Agent 协议兼容如保留，其新到达样本由统一入口归入新表，不涉及读取旧历史数据。
- 使用隔离的临时/测试数据库验证初始化、重复启动和新表自身的崩溃恢复。需要重建现有测试监控表时，应明确目标数据库和表范围，不将“无需旧历史迁移”扩大为清空账号、节点、任务或其他数据。
- 本次仍只修改交接文档；未执行删表、重建或清空数据库。开发测试旧库不承诺直接升级，遇到不匹配结构应明确提示使用新测试库，不能伪报初始化成功。

## 12. 可观测性、资源和关闭

最少记录/暴露：当前指标模式、实际 C/R/W、需求数量及租约剩余、最后采集/发送/接受/刷盘时间、各组错误、窗口样本数/字节数、丢弃原因、当前冷却和拒绝计数、traffic.dat 保存状态、初始化/轮换状态。使用限频结构化日志，不输出 token、私钥、完整凭证或任意敏感进程内容。

沿用文件读取 256 KiB、网卡 64、磁盘设备 64、文件系统 32、proc 枚举 65536 的已有边界；若真实环境超限，报告 unknown/degraded，不无限增大。Panel 继续最多 1024 个监控节点的逻辑上限，但“可配置上限”不等于已验收容量。聚合器另设全局字节预算，实施初值 64 MiB，实测后再调整并记录；达到预算时明确拒绝新观测/报告过载，不能接受 ACK 后无痕丢弃。

完整采集慢、DNS 探测慢、SQLite 写入慢都不应阻塞 WSS 心跳/任务。标准库 syscall 的 context 检查不能保证中断卡住的内核调用，不通过无限后台 goroutine 假装超时回收。

关闭顺序：停止新调度/新订阅 → 取消在途 I/O → 有界结束 worker → 保存月账本 → Panel 有界刷盘 → 关闭网络/存储。指标允许丢失尚未发送或未落盘窗口；任务结果/设置回执沿用其可靠性要求，不适用指标丢弃规则。停止 Agent 不停止任何代理业务进程。

## 13. 交付边界

必须连同代码交付：权威协议和样例、生成模型、OpenAPI、开发测试初始化说明、新表重启恢复测试、状态机测试、TLS 双进程集成测试、Linux 实机采集与资源报告、前端状态和字段说明、版本兼容矩阵。

未验证前不得宣称：精确流量计费、离线历史补传、跨所有虚拟网卡的自动准确流量归属、毫秒级 I/O 峰值、所有发行版运行通过、Panel 多实例安全聚合、WSS 写成功即历史耐久保存。

本文交接时仅完成源码核对与设计整理；实现、新库初始化、发布、平台运行验证均未执行。详细任务顺序、验收用例和发布门槛见[实施计划](MONITORING_REDESIGN_IMPLEMENTATION_PLAN.md)。
