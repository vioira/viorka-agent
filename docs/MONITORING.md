# 节点主机监控

当前只支持 `node.telemetry.v3`，旧版 Agent 监控设置、REST 设置接口、v1/v2 指标上报和自动回退路径已移除。外层 Envelope 仍为 1.0；监控能力必须单独协商。Panel 与 Agent 需要配套升级，不能再依赖旧版监控兼容。

默认启用 v3。Panel 的 `VIORKA_TELEMETRY_V3=false` 或 Agent 的 `telemetry_v3:false` 可明确停用新版监控，但不会恢复旧协议。管理连接、身份、只读任务和代理进程生命周期保持独立。

- 常规快速采集 C 为 1–60 秒，默认 30；HTTP 上报及完整采集 R 为 30–180 秒，默认 60；实时采集/上报 W 为 1–5 秒，默认 2。C 与 R 独立。
- 无浏览器订阅时通过认证 HTTP POST 上报；有效实时需求租约使用 WSS。每组保留独立采样时间、水位与错误状态，未知值不补零。
- fast 包含 CPU、内存/Swap、运行时长、网络与套接字；full 包含负载、进程数、文件系统和设备 IO；公网 IP 每 600 秒刷新。资产探测独立运行。
- 设置使用 `node.settings.v3` / `node.settings.v3_result`，保存于固定 `node-telemetry-settings.json`，先验证和原子持久化再确认。旧 `node-settings.json` 不再读取或写入。`metrics_seconds` 已删除，旧 bootstrap 中应移除此字段。
- Panel 设置入口仅为 `/api/v1/nodes/{id}/telemetry-settings` 及 `/status`；保存 Desired 不代表 Agent 已应用。旧 `/settings` 接口返回 404。
- 实时窗口最多 600 点、10 分钟；历史按 UTC 30 秒桶聚合，查询保留最近 168 小时。管理连接离线不使仍新鲜的 HTTP 数据自动过期。
- 文件系统使用率按 used/(used+available) 计算，排除保留空间；磁盘 IO 由 Agent 筛选整盘底层设备、去除分层重复后汇总；实时和历史只显示汇总图表，可切换速度、IOPS、加权等待时间和最高利用率，未知留缺口，部分数据有标记。大整数以十进制字符串传给浏览器。

Agent 仅主动出站；断线、停用监控或停止 Agent 不停止代理服务。Linux 采集不依赖 root，权限不足报告未知；非 Linux 报告 unsupported_platform。macOS 自动化检查不能替代 Debian/Ubuntu/Alpine、Linux arm64 与低配 NAT VPS 的实机长稳验收。

权威监控契约位于配套 Panel 仓库 `api/agent/v1/TELEMETRY_V3.md`；本地 Schema 与生成模型必须和该版本一致。详细本地文件权限、网络过滤和月流量账本见 [CONFIGURATION.md](CONFIGURATION.md)。
