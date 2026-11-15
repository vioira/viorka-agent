# 指标断线补传验证记录

日期：2026-11-15。此次修改只增加有界内存补传、断线原因日志和 Panel 迟到图表点合并，不操作代理服务、不开放管理入站端口、不引入磁盘采样队列。

## 实现边界

- 默认本地 `telemetry_buffer_bytes=524288`，512 条；可选 1048576，1024 条。待发、发送中和最新快照共同计入预算，共享编码内容只计一次。
- 待发采样按原采样时间保留最多五分钟，容量不足淘汰最旧待发点。单条/单批采样预算 56 KiB，外层元数据预留 8 KiB；上传单并发。过期、超大及容量淘汰分别计数。
- HTTP 保留 R 最小尝试间隔，WSS 保留 W 节奏和现有控制优先级；补传不绕过服务端限流。大量网卡、较高采样频率、较长 R、持续拒收可能导致未传完就过期；五分钟不是保证可补齐的断线时长。
- 保持原始 run、组序号和采样时间，先补旧数据，未收到有效回执的部分重排队。Panel 用水位去重；普通回执表示接收入内存，不代表 SQLite 刷盘，Panel 崩溃仍有丢失已确认但未刷盘数据的可能。
- IP 最新快照有独立 900 秒上限（每 600 秒刷新）。进程重启不恢复普通指标缓存；日流量继续使用原有独立耐久账本。

## 本机资源实测

环境：macOS / Darwin ARM64，Apple M4，Go 1.27.3；无 cgroup 限额，未运行代理引擎。以下 RSS 由操作系统的 Agent 子进程 `Rusage.Maxrss` 返回，不包含测试中的 Panel 进程。

| 真实 Agent 与本地 TLS Panel 场景 | 运行约时长 | Agent 峰值 RSS | 用户 CPU 时间 | 系统 CPU 时间 |
| --- | ---: | ---: | ---: | ---: |
| HTTP 已接收后丢失响应，原序号重传 | 90.18 秒 | 24,854,528 B（23.70 MiB） | 37.780 ms | 23.428 ms |
| WSS 成功回执丢失，HTTP 按序补传，再恢复 WSS | 90.49 秒 | 24,477,696 B（23.34 MiB） | 35.392 ms | 21.984 ms |
| 管理连接中断、四次重连拒绝、HTTP 继续上报后恢复 WSS | 101.10 秒 | 24,641,536 B（23.50 MiB） | 34.736 ms | 24.004 ms |
| 会话轮换、立即补报、原 viewer 恢复并完成只读任务 | 171.30 秒 | 26,263,552 B（25.05 MiB） | 45.646 ms | 32.387 ms |

这些数值是本次短时测试观测，不是 Linux 资源承诺。macOS 的采集组会报告部分不支持项，不能代替 Linux `/proc`、`/sys`、真实网卡和代理引擎共存时的测量。

持续满缓存微基准（每 30 次采样模拟失败重排队，含 JSON 编码/Schema 校验，默认 512 KiB 档）：

| 网卡数量 | 每次采样处理 | 每次累计分配 | 分配次数 | 编码缓存峰值 |
| --- | ---: | ---: | ---: | ---: |
| 1 | 16.575 µs | 10,694 B | 165 | 228,864 B |
| 64 | 179.216 µs | 96,571 B | 2,883 | 518,800 B |

每次累计分配包含临时对象，不是保留堆内存或 RSS。缓存峰值均未超过 524,288 B；较小单点先触发 512 条数量限制。测试同时覆盖 1 MiB 档以及发送中继续采集、失败回队时的总预算。

## 已执行的检查

- Agent 与 Panel 的 `scripts/validate.sh` 均通过，包含 Go 全量测试、vet、构建和协议检查；Agent 脚本还编译 Linux amd64/arm64 的所有测试包。
- 两仓库 `go test -race ./...` 通过；最后新增的 Agent 超大采样测试另经连接/配置包 race 验证。
- `CGO_ENABLED=0` 的 Linux amd64/arm64 Agent 构建通过。
- 真实 Agent 子进程 HTTP 丢响应、WSS 丢回执、管理断线恢复、计划会话轮换测试通过，包含只读任务、回退/恢复及正常退出；HTTP/WSS 丢回执测试还逐点比较重传前缀的序号/时间戳，并验证真实 Panel 回执的 duplicate_count。计划会话轮换测试验证协商后立即补报及日志 `reason=session_rotation`，不再要求等待旧 POST 周期。
- 三分钟断线测试使用可控时钟和真实队列/调度器，没有等待三分钟真实网络中断；TLS HTTP 丢回执和 WebSocket 错误传递使用本地真实连接。
- 普通全量测试未设置 `VIORKA_TEST_AGENT_BINARY` 时会跳过可选进程测试；上面列出的进程场景另以实际构建二进制单独执行。未执行全部可选长稳/负载测试。
- 本机默认 Python 缺少 jsonschema，已在 `/tmp/viorka-replay-venv` 按 Panel 固定开发依赖创建隔离环境；Go 缓存使用 `/tmp/viorka-go-cache`。测试需要临时回环监听，使用已获准的沙箱外本地测试权限。

## 复现

在 Agent 仓库执行：

```sh
go test ./internal/connection ./internal/config
go test -race ./...
go test ./internal/connection -run '^$' -bench BenchmarkTelemetryReplayBuffer -benchmem -benchtime=2s -count=1
./scripts/validate.sh
go build -o /tmp/viorka-agent-replay ./cmd/viorka-agent
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /tmp/viorka-agent-replay-linux-amd64 ./cmd/viorka-agent
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o /tmp/viorka-agent-replay-linux-arm64 ./cmd/viorka-agent
```

在 Panel 仓库执行（Python 校验依赖见 `scripts/requirements-dev.txt`）：

```sh
VIORKA_TEST_AGENT_BINARY=/tmp/viorka-agent-replay go test ./internal/httpapi -run '^TestTelemetryAgentProcess(LostPOSTResponse|LostWSSReceipt|ManagementOutage|ScheduledSessionRefresh)$' -v -count=1
go test ./internal/monitoring -run TestMetricTrends -count=1
go test -race ./...
./scripts/validate.sh
```

新增覆盖：三分钟断线积压按序恢复、原始身份和时间保留、缓存容量/数量限制、部分帧已确认与剩余帧重传、过期淘汰、HTTP 丢响应重传、旧连接迟到结果隔离、限流预算、配置拒绝，以及真实 TLS WebSocket 远端关闭/二进制帧/损坏消息/会话轮换原因和日志脱敏。图表覆盖全部补齐及部分补齐的断点重算。

## 未验证项

- Debian/Ubuntu、Alpine 的实际 Linux 运行与 ARM64/AMD64 实机性能；交叉编译只能证明可编译。
- cgroup 64 MiB / 128 MiB 硬限额、整机低内存压力、swap/OOM 行为，以及与 sing-box/Xray/Realm 共存。
- 真实 NAT/反向代理超时、持续数小时以上的断线重连长稳和磁盘慢写场景。
- 浏览器视觉验收；本次不修改 Web 组件，图表修复通过服务端趋势增量数据测试验证。

这些环境未在当前主机提供。默认小缓存不能代替整机预算验收，不宣称 64 MiB 节点一定不会 OOM。
