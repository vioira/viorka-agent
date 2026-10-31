# 节点主机监控

Agent 在已认证的出站 WSS 中发送 `node.metrics`，不开放新的端口、不执行 Shell、不修改代理服务。当前采集目标是 Linux amd64/arm64，读取当前主机命名空间的 `/proc` 和根文件系统 `statfs`；不将容器 cgroup 配额误称为主机总量。非 Linux 返回 `unsupported_platform`，不会返回伪造的零指标。

## 指标与语义

- CPU：`/proc/stat` 聚合 CPU 前八项的差值（不再次累加 guest）；idle 与 iowait 计为空闲。显示全机 0–100% 使用率和逻辑 CPU 数。
- 内存：`MemTotal - MemAvailable` 为已用量，另报可用量、总量、Swap 已用/总量。缺少 `MemAvailable` 不猜测替代值。
- 负载与运行时长：`/proc/loadavg` 的 1/5/15 分钟负载，以及 `/proc/uptime`。
- 磁盘：仅根文件系统 `/` 的总量、已用量、普通用户可用量；保留块使已用加可用不一定等于总量。不扫描文件树，不枚举远端挂载。
- 网络：`/proc/net/dev` 每个非回环网卡的累计 RX/TX 字节，以及两个成功采样间的字节/秒速率。首次采样、网卡新增、计数器倒退、采集失败后的恢复，以及大于 600 秒的间隔不产生速率。虚拟网卡可能重复记录同一业务流量，合计仅用于观察主机接口活动；不是今日、月度、代理用户用量或服务商计费。

CPU 首次采样或计数器倒退也只报告 CPU 数，等待下一次有效差值。每个采集组单独报告 `*_unavailable`，不把未知量写为 0。错误只使用固定代码，不发送本地路径或系统错误文本。

## 配置和资源上限

每节点设置 v2：采集 `collection_seconds` 0–10，默认 0；历史入库周期 `report_seconds` 30–180，默认 60；实时上报 `wss_report_seconds` 1–5，默认 2。0 表示随实时上报采集，不创建零周期定时器。采集快于上报时积累并发送期间样本；慢于上报时复用快照及原始 sampled_at，Panel 不重复计数。资产信息独立每 300 秒刷新，心跳独立每 30 秒。

旧 Panel 不下发 v2 设置时，Agent 使用本地 `metrics_seconds`（默认 30，范围 10–300，0 使用默认）。每个内核文件最多读 256 KiB、最多 64 个非回环网卡。采集不需要 root，权限不足时报告未知。

连接循环拥有采集与发送，保持单 reader/writer、64 KiB 帧、5 秒写超时。内存样本最多 10 条，按帧大小拆分；ticker 合并过期 tick，不追赶历史采集。断线丢弃未发送数据，不做磁盘补传。资产探测及系统调用可能延迟该循环，周期是调度目标，并非实时保证。任务回执沿用既有持久化机制。

## Panel 与界面

详情实时监控每 2 秒轮询，列表沿用每 30 秒刷新。历史每 30 秒查询，仅活动面板发起请求；失败保留原内容，通过全局 Toast 提示。所有接口要求 viewer 或以上实例级权限。

`GET /api/v1/nodes/{id}/metrics` 保留已有实时快照及兼容的一小时内存历史契约，后者不是持久化均值。新历史接口 `GET /api/v1/nodes/{id}/metrics/history?range=1h|6h|24h|7d` 返回已落库分钟统计：1h/6h 为 60 秒，24h 为 300 秒，7d 为 900 秒，最多 674 个点。记录各指标 sum/max/count，样本均值为 sum/count，峰值是已采样到的最大值，不是采样间隔内绝对峰值。不同采样频率的样本均值不等于连续时间加权平均。

历史包括 CPU、内存/Swap、根文件系统用量、1/5/15 分钟负载、网络收发速率。缺失指标省略，Swap 总量为零时使用率未知；没有采样的时间桶不创建。图表使用真实时间坐标和独立路径展示断线缺口，并提供可访问的数值列表。Arc LineChart 会把缺失值视为零，因此此处使用最小静态 SVG，范围切换、按钮及设置继续使用 Arc。

采样时间必须在 Panel 时间前后 5 分钟内，按 UTC sampled_at 分分钟桶。重复、迟到采样不覆盖最新水位；v2 批次必须有序且为 1–10 个样本，整批校验通过后接收。每连接仍最多 120 条消息/分钟。离线或超过 max(90 秒, 3×有效采样间隔, 3×上报间隔) 时实时值过期，不用上次值填充缺失区间。

## 持久化与兼容性

SQLite 单表保留 7 天，按配置周期批量写入，不同步阻塞 WSS 接收。正常退出先关闭 Gateway，再停止聚合循环并最多等待 5 秒刷盘；异常退出可能丢失尚未落库的数据。后台存储故障记录错误，缓存有时间和节点数上限，详见 Panel 仓库的 docs/DATABASE.md。最多缓存 1024 个节点，目标规模为 50 个节点。

外层协议继续为 1.0，权威 Schema 在 Panel `api/agent/v1`：

| 组合 | 行为 |
| --- | --- |
| v1 Agent / 新 Panel | 仍协商 metrics_version=1、接收 node.metrics 并汇总；不下发新设置或旧语义的 node_settings |
| 新 Agent / v1 Panel | 宣告 node.metrics.v1 与 v2；按确认版本发送，无监控确认则不发送；不宣告 node.settings.v1，沿用本地周期 |
| 新 Agent / 新 Panel | node.metrics.v2 对应 metrics_version=2 与 node.metrics_batch；node.settings.v2 对应 collection_settings |

旧 node_settings 的 Schema 保留为 LegacyNodeSettings，新参数使用独立 collection_settings，避免严格解码的旧 Agent 接收不兼容字段。未协商批量消息、跨节点身份或非法指标会关闭连接。发布需要分别发布匹配的 Panel/Agent 制品，工作区实现不等于已发布。

## 验证边界

单元和集成测试覆盖周期边界、零周期、批量、重复与乱序、旧版兼容、TLS/WSS、任务共存、权限、SQLite 重启恢复与重试、过期清理及缺失图表数据。macOS 不执行真实 Linux procfs 采集；Debian/Ubuntu/Alpine 与低配 NAT VPS 的资源开销、长时间运行仍需实机验收。当前没有告警、流量计费、任意挂载点监控、容器配额或离线补传。
