# Monitoring v3 资源验收记录（未完成）

2026-11-03。此文件保存已测事实与失败，不是容量承诺。当前未通过全部实施计划性能场景；默认v3开关仍关闭。

## Ubuntu amd64：50 节点同步 HTTP 突发

环境为已授权 Ubuntu 24.04.2 amd64、约1613MiB内存，以nobody在独立临时目录运行。测试使用真实TLS路由、50条WSS协商/心跳、每节点独立身份和临时SQLite；客户端是协议级合成负载，不执行50份真实Agent采集。procfs资源包含同进程Panel与负载生成器。测试期间另一独立单节点soak也在主机运行，不能称独占性能实验。

夹具：Panel `internal/httpapi/telemetry_load_test.go` 的 `TestTelemetryFiftyNodesHTTP`，显式设置 `VIORKA_TEST_LOAD_HTTP=1` 和绝对新文件路径 `VIORKA_TEST_LOAD_OUTPUT`。计划三轮同步50个POST，真实R60秒；每POST两个C30 fast点及一个full点，末尾验证每节点fast_count6/full_count3、管理仍online。每5秒尝试真实Flush并记录耗时，每10秒记录资源。协议示例读取自工作目录的`../../api/agent/v1/examples/node.metrics.v3.json`。Go测试deadline应至少5分钟。

**实际结果：首轮失败，没有执行完三轮。** 初版遇到503即终止；改进诊断后重测，26个请求接受、24个请求失败，首错误503。没有调大生产并发预算或自动重放请求以掩盖失败。当前HTTP上传与共享解码使用有界并发；需继续区分稳态50节点能力和同步突发保护，并补充拒绝后的恢复证据。

计数版Linux测试二进制SHA256：`0fe8f92449b8cdc738cafa610bb54c3dd417bf4447baeeaedf5724616d9d95cb`。最终测试退出1，用时1.46秒，负载首轮耗时约0.202秒。远端证据`/tmp/viorka-monitoring.Bmu3rVeg/load-http-50-counts.jsonl`；本地日志`/tmp/viorka-load-http-50-counts.log`。

| 首轮结束快照 | 实测值 | 口径 |
| --- | --- | --- |
| 请求接受/失败 | 26 / 24 | 失败包含任何非预期回执；首错误503 |
| RSS | 40,370,176 bytes | Panel及合成客户端同进程 |
| goroutine | 258 | 同进程瞬时快照，不是稳定峰值 |
| FD / socket FD | 214 / 203 | socket FD不等同TCP连接数 |
| 创建客户端TCP连接 | 100 | 50管理WSS与首轮HTTP；未完成后续轮次，不能证明HTTP复用 |
| 客户端读取/写出字节 | 318,910 / 291,819 | DialContext下层计数，含TLS/WSS/HTTP；不包含直接fixture注册/challenge |
| DB / WAL文件大小 | 421,888 / 4,120,032 bytes | 含资产/身份/会话等；尚未完成指标flush阶段 |
| CPU user/system ticks | 102 / 27 | 累计原始tick，不转换为未知HZ下的百分比 |

失败没有生成complete记录，不能引用尚未生成的p95、全部落库计数或刷盘耗时作为结果。

## 有界等待修复后的同场景复测

保留8个HTTP读体/处理槽位，新增最多56个鉴权后、读体前等待位置，最长250ms，取消/超时均释放。队列满仍503；共享解析及聚合内存限额不变。新增测试覆盖排队释放、上限、取消、超时，以及过载时不读取body。

2026-11-03，同一Ubuntu nobody、同样50节点三轮同步突发通过，未错峰或自动重放以绕开原失败。测试121.68秒；负载窗口120.510秒，150个POST均接收，SQLite逐节点fast_count6/full_count3（总300/150），所有节点management online。第二、第三轮无新增TCP连接，验证该场景HTTP复用。此为约两分钟短测，不证明长时间稳态、50个真实采集器或其他硬件性能。

测试二进制SHA256：`98f950f60d6c589fc783493971b5ed21fc3b11da222dcd63afbce054480fce58`。输出本地`/tmp/viorka-load-http-50-queue.jsonl`、远端同临时目录`load-http-50-queue.jsonl`，日志`/tmp/viorka-load-http-50-queue.log`。网络字节计数位于TLS下层客户端连接，含管理心跳及TLS握手，排除fixture直接注册/challenge。

| 指标 | 实测值 | 限制 |
| --- | --- | --- |
| POST p95 / max | 0.191543 / 0.205002 秒 | 包含客户端至入口回执；不等于落库延迟 |
| 请求 / JSON payload总字节 | 150 / 193950 | 两fast一full的小批次，非1MiB压力 |
| Flush调用 / max耗时 | 24 / 0.321403 秒 | 包含可能无需提交的调用；非事务数量 |
| 采样最大RSS | 47,132,672 bytes | 13次快照；Panel+合成客户端，不保证捕获瞬时峰值 |
| 采样最大goroutine / FD | 278 / 214 | 同进程合计 |
| 累计客户端TCP创建 | 100 | 50管理WSS+50HTTP，无后续新增 |
| 负载期TLS读取 / 写出增量 | 170550 / 377079 bytes | 约1415 / 3129 bytes/s，含HTTP及管理协议开销 |
| CPU user / system tick增量 | 423 / 92 | 原始tick，不推断CPU百分比 |
| 最终DB / WAL | 421888 / 4120032 bytes | 同库身份/资产/会话及指标合计 |

原失败记录仍保留；新增等待不保证任意慢连接或大批次均可通过。50节点W1与故障压力场景仍待测试。

## 50 节点 W=1 实时短测

2026-11-03，新增共享性能夹具的WSS分支`TestTelemetryFiftyNodesRealtime`。在同一Ubuntu nobody环境，50真实TLS/WSS客户端逐秒同步发送，每节点121个fast、3个full，处理真实租约初始ACK和续租、每30秒心跳；该分支没有HTTP指标POST。启用环境变量`VIORKA_TEST_LOAD_WSS=1`，输出必须使用独立新文件。

实际通过121.82秒，负载窗口120.474秒，6050帧全接受，SQLite逐节点计数121/3（总6050fast/150full），管理仍online，未触发每秒调度missed_deadline；仅50个客户端TCP连接，无连接轮换。既有共享解码并发预算保持8，本场景没有据推测调大它。

构建SHA256：`fb8d8d3f487ef7ba6383fe94c49fb14e0cb67e6eea5dc33d74de64664d06d958`。原始结果本地`/tmp/viorka-load-wss-50.jsonl`和`/tmp/viorka-load-wss-50.log`；远端测试临时目录`load-wss-50.jsonl`。同进程Panel+合成客户端、非独占机器等限制与HTTP测试相同，未建立真实浏览器SSE连接。

| 指标 | 实测值 | 口径 |
| --- | --- | --- |
| WSS接收回执 p95 / max | 0.052221 / 0.120611 秒 | 含该帧前可能的心跳/续租处理，不是浏览器显示延迟 |
| 帧数 / envelope JSON总字节 | 6050 / 5351188 | full只每60秒，非每帧重发 |
| Flush调用 / max耗时 | 24 / 0.308467 秒 | 调用数不等于实际事务数 |
| 采样最大RSS / goroutine / FD | 46993408 bytes / 124 / 114 | 13次快照，同进程合计 |
| 负载期TLS读取 / 写出增量 | 2785510 / 5700794 bytes | 约23121 / 47319 bytes/s，含心跳、回执、续租 |
| CPU user / system tick增量 | 1640 / 323 | 原始tick |
| 最终DB / WAL | 577536 / 4214792 bytes | 含身份/会话与指标 |

这是两分钟实时短测，不覆盖频繁开关、数据库暂停、DNS超时、反复断线或长期资源趋势。共享夹具改造后HTTP回归通过121.79秒（/tmp/viorka-load-http-50-shared.log）。



当前新实例`soak-inventory-24h`于2026-11-03T02:10:45Z开始，Panel93755/Agent93769，双方构建摘要见进度第十二、十三批。实际于2026-11-03T02:39:56Z退出1，1750.34秒，soak mode did not settle realtime；不是24h通过。续租pending瞬态与夹具断言竞争是待验证假设，原证据不足以确认原因。此前主动终止的旧构建观测保留诊断用途，不累计为本轮时长。

## 尚缺的性能与环境证据

- 50节点默认HTTP已有两分钟短测，持续资源验证仍需扩展；50节点W1已有两分钟短测；频繁订阅开关、SQLite暂停恢复、DNS超时和反复断线的资源/恢复报告。
- 真实浏览器SSE更新延迟、Agent内部goroutine，以及独立进程和稳态网络字节率。
- 当前版本24h成功终态；Linux arm64、Alpine及低配NAT72h（连接信息待提供）。
- 1024逻辑上限拒绝单测仅证明限制，不证明该规模性能。

2026-11-03T02:55:49Z以当前Panel构建启动`soak-renewal-24h`（SHA256 3a06afb207bf6aa8618dcd4a8851f677922f0ea1699ecb8cad9ad7edd69aa6ef），允许有界续租pending并保留独立Agent日志；Panel95968/Agent95981已确认存活，未达到24h。不将旧失败时长累计。

`soak-renewal-24h`实际于2026-11-03T03:24:59Z退出1（1750.37秒）：控制状态degraded、refs1、lease82秒，采样序号仍增长；Agent日志显示会话重连。尚未24h成功，不能继续描述为运行中。下一步验证正常会话更新后的有界恢复，未启动替代长测。


2026-11-03T03:47:54Z新建`soak-session-24h`，Panel97505/Agent97518（nobody）已确认存活。默认周期主动会话刷新在macOS与Ubuntu分别约31.078/31.035秒恢复；据此夹具仅在真实成功签发下一代凭证后提供R+45秒观测恢复窗口，10秒轮询起点误差另计，重复签发不得续宽限。新构建及摘要见进度第二十五批。前两次失败保留，新实例从零计时，尚未24h通过。
