# Monitoring v3 验收证据索引

截至 2026-11-03。对应实施计划 T01–T37；这是证据和缺口清单，不是全项通过声明。

路径前缀 A 为本 Agent 仓库，P 为相邻 Panel 仓库。表中测试函数可通过 `rg 'func Test名称' internal protocol` 定位。历史执行结果、二进制 SHA256、环境和日志位置见 [进度记录](MONITORING_REDESIGN_PROGRESS.md)；旧版四组合见 [兼容性报告](MONITORING_COMPATIBILITY.md)。测试名称只是定位入口，表中列明实际证明范围。确定性时钟、fixture、SQLite 重开、真实进程和浏览器证据不能互相替代。

| 项目 | 可定位证据与已覆盖范围 | 仍需验收/限制 |
| --- | --- | --- |
| T01 | A connection `TestTelemetryCadenceIndependentCollectionAndReporting`：180 秒 C30/R60 次数；`TestTelemetryHealthyPOSTJitterPreservesWindow`：正常限频不丢两条原样本；Ubuntu 默认周期 soak-session-24h 已重新启动，前两次失败保留 | 连续 24h 尚未完成；定时器次数不是整个 HTTP 批次落库证明 |
| T02 | 同 cadence 测试 C2/R60；P monitoring `TestTypedBucketAggregationSemantics`、storage `TestTelemetryHistoryWeightedMeansAndSlowOnly` | P TestTelemetryAgentProcessThirtyPointBatch真实C2/R60首POST30点，逐条验证采样间隔/身份和SQLite UTC桶计数，总计30，macOS race通过；桶边界使该次跨3桶，非固定2桶 |
| T03 | A connection `TestTelemetryRealtimeCadenceIgnoresCollectionInterval`：C60/W1 每秒 fast/WSS，无 POST，full 每60秒 | 确定性调度测试，不等于真实网络每秒时延保证 |
| T04 | 同测试 W2/full60；P storage `TestTelemetryRecorderReplayRestartAndGroupIdentity`、monitoring `TestTelemetryGroupFreshnessIndependentOfCachedReceipt` | 核对真实链路 full/IP 重发的统计与时间不变 |
| T05 | A connection `TestTelemetryWorkersIsolationAndFinalSave`、`TestTelemetryWorkersShutdownTimeoutIsVisible`：阻塞慢源无重复工作，fast继续，关闭超时可见 | 未完成真实阻塞系统调用与管理心跳/任务同时运行验证 |
| T06 | A collector `TestTelemetryFastGroupsAndContract`、`TestTelemetryNetworkIdentityAndRecovery`、`TestTelemetryDiskRatesQualityAndContract` | 首次基线、重启/回退/恢复由 fixture 验证；其他目标平台待实测 |
| T07 | A collector `TestTelemetryFastGroupsAndContract`：TCP inuse 2+4、UDP 3+5，tw99不计；IPv6缺失允许、损坏未知 | 未做各目标内核 socket 对照采样 |
| T08 | A collector `TestTelemetryFullContractAndIsolation`：两本地盘合计、不探测NFS、任一失败不报部分正常；`TestTelemetryCapacityPrecisionAndAliases`：身份去重与溢出 | 真实 bind/Btrfs 多挂载待验证 |
| T09 | A collector `TestTelemetryDiskRatesQualityAndContract`；P typed bucket/device 聚合测试 | 设备 fixture 与单台 Ubuntu 已有记录，其他平台待测 |
| T10 | A config network_filter_test.go、collector telemetry_network_test.go、state `TestTrafficFilterTransitionAndOverflow` | 过滤规则及 revision 有单测，真实隧道显式纳入待联调 |
| T11 | A state `TestTrafficLedgerAcrossProcesses`：三个独立子进程显式保存后退出，恢复仅增加新差值、再次恢复不双记；macOS race与Ubuntu nobody通过 | 真实进程/落盘，网卡累计计数与UTC时间为受控输入；非真实网卡重启实验 |
| T12 | A state traffic_file_test.go、traffic_test.go；connection `TestTelemetryWorkersFinalSaveFailure`、`TestTelemetrySourcesPreserveCorruptTraffic` | 损坏/写失败有注入；故障同时管理任务持续执行待串联 |
| T13 | A state `TestTrafficMonthBoundaryAndLongGap` | 已审计UTC月末fixture：跨一个月差值归新月且boundary_estimated，跨三个月不计旧差值且period_gap，MonthStart等于新UTC月起点；定向race1.016秒通过。不声称真实月末实机观测 |
| T14 | P httpapi `TestTelemetrySSEScopeActivatesGatewayLease`：两真实SSE引用2，关一个为1且无grace，关最后为0且grace≤15s；gateway `TestTelemetryDemandRenewalGraceAndReceipt`：grace到期 | 两真实浏览器用户/标签尚未完整联调 |
| T15 | P httpapi metric_events_test.go；web use-live-metrics.test.ts：隐藏/恢复、旧连接事件隔离、终态清理 | web测试替代hooks/EventSource；真实浏览器权限撤销、删除、分页待测 |
| T16 | A connection `TestTelemetryLeaseSupersededDisconnectAndExpiry`；P gateway `TestTelemetryDemandAdmissionDeadline` | 90秒及乱序由可控时钟验证，长稳不能替代该故障场景 |
| T17 | A connection telemetry_schedule_test.go、telemetry_lease_test.go、telemetry_window_test.go | 已逐断言核对：POST未完成无ACK、期间采集继续、完成后ACK及pending32/新33切WSS，POST1..31不重发；Panel匹配ACK前仅pending，旧ACK无效。双端定向race通过；未强制真实网络悬挂POST |
| T18 | A connection `TestTelemetryScheduleHTTPRealtimeFallback`、telemetry_wss_test.go、telemetry_retry_test.go | P TestTelemetryAgentProcessLostWSSReceipt：真实CLI+Gateway TLS转发仅丢成功回执，验证超时后HTTP仅最新单点、原身份、R间隔、实时恢复及退出；macOS race92.300秒和Ubuntu nobody90.31秒均通过 |
| T19 | P httpapi `TestTelemetryAgentProcessLostPOSTResponse`：真实Agent、TLS接收后丢响应，首批15点、30.01秒后仅最新序号30，SQLite计数16；`TestTelemetryWSSReadyAndHTTPDedup`验证双通道重复 | macOS race与Ubuntu nobody真实进程均通过；后续实时切换、任务与关闭也通过 |
| T20 | A connection telemetry_window_test.go：条数/字节上限、超大组、精确拆帧；telemetry_wss_test.go：真实socket控制消息 | 已核对301条淘汰、1MiB字节预算、分帧完整性、64KiB恰好/加1字节与跨重连；诊断记录pending/inflight字节及丢弃计数，定向race复验2.458秒。编码边界使用构造载荷，不称真实采集压力 |
| T21 | A connection `TestTelemetrySharedRefusalLadderAndSingleProbe`、`TestTelemetryRetryLatestDeadlineAndLateResult`、`TestRetryAfterDateAndOverflow` | 另有P TestTelemetryAgentProcessRefusalsAndTasks真实CLI前三次503及120秒恢复通过；全部七级和WSS拒绝仍由分层测试覆盖 |
| T22 | 拒绝计数独立于管理心跳；A retry tests，P真实进程测试验证通常模式切换下任务成功 | P TestTelemetryAgentProcessRefusalsAndTasks同一真实链路三次503期间三个任务成功，恢复及模式切换通过；macOS race272.325秒及Ubuntu nobody270.31秒均通过 |
| T23 | A connection session_tls_test.go、telemetry_post_test.go、telemetry_wss_test.go；四组合CLI测试有吊销后非零退出 | HTTP与WSS并存时禁用/吊销/跨节点完整矩阵仍需核对 |
| T24 | A connection `TestSessionCoordinatorCoalescesRefreshAndFencesLateResponses` | coordinator并发单测；真实默认周期主动刷新已在macOS/Ubuntu通过，约31秒恢复、任务成功；服务端POST/WSS同时过期仍待联调 |
| T25 | A/P protocol telemetry_decode_test.go、P monitoring bucket_stats_test.go | P TestTelemetryHTTPTimestampAdmissionAndAtomicity真实TLS/身份/SQLite覆盖R180下-480秒接受/-510秒拒绝，未来+290接受/+310拒绝，坏fast或过期IP使整批不写历史/不改live；IP-900秒接受且原时间保留。双方协议确定性测试覆盖-495/+300精确边界及超出1ns。HTTP使用时间裕量，不宣称纳秒网络测量 |
| T26 | P monitoring `TestTypedBucketAggregationSemantics`；storage `TestTelemetryHistoryWeightedMeansAndSlowOnly` | 已有AVG按独立count、NULL、MAX/latest、粗粒度断言 |
| T27 | P storage `TestTelemetryRecorderReplayRestartAndGroupIdentity`、`TestTelemetryHistoryWeightedMeansAndSlowOnly` | 已有组身份去重和slow-only；完整传输结合T04待核对 |
| T28 | P storage `TestTelemetryAtomicSnapshotRestart`：真实SQLite触发器提交失败回滚统计/检查点、重开恢复；兼容CLI测试有Panel kill/restart | P TestTelemetryRecorderCrashBoundary：独立子进程在Recorder接收后/刷盘前、刷盘成功后分别握手并被父进程强制kill；前者仅保留旧数据，后者恢复新数据及去重检查点。macOS race与Ubuntu nobody通过；这是Recorder/SQLite进程故障，非物理断电或完整HTTP ACK崩溃测试 |
| T29 | P storage atomic/constraints tests、monitoring recorder预算测试；`TestTelemetryWeekRotationForeignKeysAndLateArrival`验证删除后不复活 | 写失败/排队满/删除竞态全链路反馈仍需串联 |
| T30 | P storage `TestTelemetryWeekRotationForeignKeysAndLateArrival`、`TestTelemetryMissedWeeksAndRollback` | SQLite真实操作+模拟UTC时间；运行中周边界长稳未做 |
| T31 | P storage sqlite_test.go 初始化/升级/旧库拒绝测试；TestTelemetryMigrationReceiptFailureRollsBackDDL在DDL完成后拒绝迁移记录写入，验证完整schema回滚、记录未提交及重开重试结构一致 | 真实SQLite事务故障已验证；不声称覆盖断电/进程kill时刻 |
| T32 | P storage week rotation与weighted means测试，P httpapi历史接口测试 | TestTelemetryHistoryTwoPartitionsRangeAndGaps：两物理周表查询、168h精确边界及超1ns拒绝、范围外旧行不泄露、缺口不补桶、小时合并统计正确；真实SQLite固定时间race通过 |
| T33 | 四组实际旧/新CLI二进制均通过；A `TestTelemetryClientFallbackToLegacy`、`TestTelemetryClientReenableAfterFallback` | CLI矩阵新二进制早于expiry/jitter修复；最终构建需更新；不支持直接假定旧Panel可读新历史库 |
| T34 | A workers关闭与保存测试、CLI重启/吊销/哨兵；Ubuntu普通用户短测与管理断线测试 | 24h未结束；长期goroutine/FD资源趋势待报告 |
| T35 | 前端fixture视觉检查及mock生命周期测试；P真实SSE/Agent进程分别验证 | 真实Ubuntu Agent+Panel+浏览器已验证实时、1h历史、50→61分页、会话替换重新登录；后台/多标签引用及完整权限矩阵仍待验收（进度第十七批） |
| T36 | A cadence/settings测试；P REST settings及web设置测试 | 已有R不改fast相位、C不改W相位；REST0/61/1.5拒绝且值/Revision不变，定向race通过，web18项通过。真实UI已验证0拒绝与C60/R30/W1 applied；完整浏览器输入矩阵待补 |
| T37 | A cadence C60/R30、`TestTelemetryScheduleCachedHTTPAndManagementLoss`；P slow-only/dedup测试 | 缓存原身份与slow-only已有分层证据；真实CLI WSS丢成功回执→HTTP兜底→恢复WSS→HTTP退出，42个不同fast身份与SQLite逐桶计数一致，macOS race通过；新增逐桶落库断言Ubuntu nobody90.39秒也通过 |

## 独立发布门槛

- Ubuntu amd64 普通用户真实采集、正常切换、管理断线且 HTTP 继续、任务恢复已通过；不是 NAT。
- 修复版24h：`/tmp/viorka-monitoring.Bmu3rVeg/soak-inventory-24h`，启动2026-11-03T02:10:45Z；已于2026-11-03T02:39:56Z因模式未收敛退出1，1750.34秒；未通过24h。此前主动终止的旧构建观测不累计。
- Linux arm64、Alpine、低配NAT连接信息待用户提供。NAT72h尚未开始。
- 单节点soak不是50节点测试；网络字节、Agent goroutine、逐次flush时延仍缺报告。
- 交接§12：Agent及Panel已新增每分钟有界结构化诊断；引用/租约通过鉴权快照暴露。仍需最终构建双端实测与资源报告，私有计数器不单独算运维证据。
- 完成剩余门槛后复核双方暂存和未暂存diff、最终验证、commit/push；当前不宣称发布验收完成。
