# 监控改造实施记录

更新：2026-11-02，Asia/Shanghai。目标仍 active，整体未完成。

## P0 已落地的第一批契约

Panel 权威源：`../viorka/api/agent/v1/envelope.schema.json`。
详细契约：[Telemetry v3](../../viorka/api/agent/v1/TELEMETRY_V3.md)。

- 新增独立 TelemetrySettings、fast/full/public_ip、批次、租约、回执和 ready 模型。
- 保持外层 1.0 和所有旧定义不变；通过独立 ready 消息避免改变旧严格解码握手。
- 明确 C=1–60/默认30、R=30–180、W=1–5，C>R 合法。
- 300 fast / 8 full / 2 IP，1 MiB POST / 64 KiB WSS，序号/时间/身份/回执数量规则。
- 补充 Schema 边界测试、Go 上下文校验与兼容测试；生成器增加 boolean 支持。
- 两仓库 Schema 和生成 Go 文件完全一致；未手改生成模型。
- 未启用/宣告 node.telemetry.v3；没有新增运行中的 POST 路由或数据库改动。

Schema SHA-256：`47ecd79956fb207e10fb289ebcadde5f46f1abefe9fb53cddfef7e1195a13129`。
源为本地工作区，尚未提交/tag/发布；不是已发布兼容矩阵。

## 实际验证

Agent：`./scripts/validate.sh` 成功，含安装/服务/发布脚本隔离测试、全量 Go test/vet、gofmt、协议生成检查和构建。最后一次协议测试改动后再执行 `go test ./protocol/v1` 成功。

Panel：执行 `PATH=/tmp/viorka-monitoring-venv/bin:$PATH ./scripts/validate.sh`。
Go test/vet、构建、SQLite schema 检查、21 个协议样例、5 组新 Schema 测试、生成模型和 OpenAPI 生成一致性检查通过；最后的 check_openapi.py 因临时环境缺 PyYAML 失败。补装 PyYAML 后单独重跑该步骤成功，未重复已通过的全套检查。最后一次协议测试改动后再执行协议 Go 测试和两个 Python 契约脚本成功。

Python 环境：`/tmp/viorka-monitoring-venv`，安装 jsonschema 和 PyYAML；没有更改系统 Python。

两仓库 `git diff --check` 成功。另用脚本比较 HEAD 所有旧 Schema 定义及 envelope 字段均未改变；跨仓库 Schema 和 models.gen.go 字节一致。

发布测试中的提交/推送只发生在测试脚本创建的临时仓库。业务仓库未提交、推送、发布、部署或重启服务。

未执行：race 全套、Linux 双架构构建/真实运行、Web/UI、TLS 新链路、资源/长稳测试。当前只有契约改动，不将这些列为已验收。

## 后续实施入口

P1 尚未修改。已读取：Panel `internal/monitoring/history.go`、`internal/storage/metrics.go`、`docs/DATABASE.md`。
真实情况：现有 Rollup 使用 stats map；metric_rollups 使用 stats_json；Recorder 的 due/interval 与 R 耦合；已有旧 60 秒恢复分支。需要按计划替换为类型化 30 秒统计、新周表/明细/资产/检查点、同节点一致提交、真实 SQLite 测试，再接 P2。

P0 的 Go `ValidateIdentity` 仅校验上下文，不是完整结构校验器。P2 必须补齐生产接收路径的字段/错误/设备/数值/缺值一致性校验及严格 JSON 解码（含 POST 字节上限），不能把 Schema 测试当作运行时验证。设置 applied_revision 与 revision、月起点、设备唯一性等跨字段约束也需要在入口落地。

P1–P7、T01–T37 的端到端验收仍待完成。继续按实施计划依赖顺序推进，不启动子代理；新能力必须保持关闭直至双端完整。

## P1 第一批：类型化聚合与周表存储基础

2026-11-02 后续进展，整体仍未完成。

已新增 Panel：

- `internal/monitoring/buckets.go`、`bucket_stats.go`：类型化 30 秒桶；CPU/RAM used/Swap used 独立 sum/count；速度与计数 max/count；容量/累计/负载/运行时长按组时间 latest；设备 I/O 不跨设备相加，slow-only 不补 fast。
- `telemetry_recorder.go`：按 run/group/sequence 去重，整批暂存，旧 run 失去上传资格；串行刷盘、版本判断防止刷盘期间新样本被错误清脏；节点快照不可拆分；5 秒检查与 R 无关，失败有界退避；32 MiB 暂存保守预算、4 MiB 单节点、8 MiB 刷盘预算、512 主桶上限及轮转公平选择。
- `internal/storage/telemetry_history.go`、`telemetry_columns.go`：桶/设备/文件系统/资产/检查点同事务保存，真实 SQLite 周轮换、跨周迟到路由、漏跑多周清理、时钟回拨检测；稳定读事务内分段查询，避免一次载入七天设备数组；当前资产单独保存，不提供资产变更历史。
- `migrations/0010_telemetry_history.{up,down}.sql`：新主表/上一周表、明细、当前资产、检查点和分区元数据；NULL/count、30 秒对齐、类型、外键、唯一性约束。
- 更新 schema 检查脚本以执行全部建库定义并检查新表；两处迁移数量测试从 9 更新为 10。

修正 P0：文件系统上限由误写的 64 改为设计规定的 32，同步两仓库 Schema/模型。当前 Schema SHA-256 为 `b08d15df54483e9e8223dd0a0a1e2c51ff8afb8e43ae5c9ae12548df5772e6fc`，替代上文旧摘要。

另发现 jsonschema 的基础安装不会注册 date-time 格式检查。新增 `scripts/protocol_formats.py` 使用标准库显式检查 RFC3339，并增加无效日期/缺时区反例，避免测试静默跳过格式检查。

### 本轮验证证据

- Panel `PATH=/tmp/viorka-monitoring-venv/bin:$PATH ./scripts/validate.sh` 完整成功，日志 `/tmp/viorka-monitoring-p1-validate.log`。最后增加显式日期格式校验后，单独重跑 `check_protocol.py` 和 `test_telemetry_contract.py`，21 个样例及 6 组契约测试成功。
- Panel `go test -race ./internal/monitoring ./internal/storage` 成功。不是全仓库 race 结果。
- 新真实 SQLite 测试覆盖：绝对快照重投、关闭/重开恢复、触发器注入写失败及事务回滚、checkpoint 防倒退、连续两次表重命名及 FK/index、跨周迟到、多周追赶、时钟回拨、删除级联和迟到写入不复活、不同 count 的 AVG、slow-only、512 桶边界和非法 NULL/count。
- Recorder 与真实 SQLite 联测覆盖：缓存 full 去重、重启重投、分组原始时间、输入指针所有权、刷盘期间新样本、写失败退避和恢复、旧 run 拒绝。
- Agent `go test ./protocol/v1`、生成一致性检查成功；两仓库 Schema 摘要一致，`git diff --check` 成功。
- 尚未做内存/RSS压力测量；代码预算是保守计数约束，不是实测资源承诺。平台、24h/72h、UI、新 TLS 双出口仍未测。

### P1 仍需完成，下一轮从这里继续

目前新存储/Recorder 已有真实实现与测试，但尚未接到 app/Gateway/历史 API。旧 `Recorder`、`SaveRollups`、`metric_rollups` 和 legacy 60 秒历史恢复代码仍在使用，这是实施中的临时状态，不符合最终完成条件。

下一步必须替换旧在线历史路径，将新到达的旧协议样本适配为带 legacy 语义的新桶，移除旧历史读写/旧表建库和旧历史迁移测试；按开发测试要求整理监控建库定义，旧监控库不做数据转换。还要接入启动/运行时周检查、节点删除清理、历史 API 的类型化投影并更新文档/测试/OpenAPI。不能以保留两条历史路径宣布 P1 完成。

P2 接收前还需完整结构和跨字段校验，当前 `TelemetryRecorder.Accept` 明确依赖调用者先完成这一步。未宣告新 capability，未实现 POST 或切换采集。没有运行任何真实数据库重建/服务变更，没有提交或发布。

## P1 后续：替换旧历史路径并接线

2026-11-02，本轮为实质实现进展；总目标仍 active。

- App 已装配 `TelemetryRecorder`，启动检查周分区，运行时每分钟维护；刷盘每 5 秒检查、退出有界强制刷盘。检测时钟回拨时保留既有分区并报告，不因此强制回退/删除分区或使 Panel 启动失败。
- Gateway 收到的新旧协议 v1/v2 指标通过 `ObserveLegacy` 进入新类型化桶，旧协议用原始 sampled_at 的纳秒水位去重并标记 legacy；旧协议资产也进入新当前资产表，不制造指标桶。没有读取/导入旧历史。
- 删除 `Recorder`/`SaveRollups`/`RecentRollups`/旧历史读取与 60 秒桶恢复代码。`metricValues` 仅保留作为既有实时 SSE 的数值投影，不再是历史存储。
- 监控初始化按开发测试目标整理：初始建库和 0006 不再建旧历史，`0009_telemetry_history` 替代旧 0009；上一轮临时 0010 已改名。新库共 9 个迁移、29 张业务/辅助表（schema 检查脚本的计数口径）。不存在旧历史表。
- 检测到旧监控表直接拒绝初始化；迁移摘要不匹配仍拒绝。新增测试验证不重置无关账号数据、不开始旧库迁移。只在临时测试数据库执行验证，未操作用户运行中的数据库。
- 历史 API 查询新周表，响应改为 `HistoryResponse/HistoryPoint` 类型化字段，大字节计数以十进制字符串输出；更新生成 OpenAPI，CPU 等统计按字段 sum/count，慢组缺口保留 null。
- 节点删除清理 Recorder 和实时缓存。活跃 WSS 先结束接收循环再清缓存，防止最后一条在途消息重建已删除缓存；历史 DB 外键与延迟刷盘检查仍保证不复活节点。
- 更新 DATABASE/MONITORING/PROTOCOL 文档；删除不再适用的旧历史迁移/兼容测试，保留并更新账号/配置/任务和新历史不被重新注册删除的测试。

### 本轮验证

- `make generate-openapi` 成功。
- `PATH=/tmp/viorka-monitoring-venv/bin:$PATH ./scripts/validate.sh` 全部成功，日志 `/tmp/viorka-monitoring-p1-integration.log`。
- Panel `go test -race ./...` 全仓库成功。
- 最后的节点删除缓存竞态修复后，补跑 `go test -race ./internal/httpapi -run 'TestDeleteNode|TestBatchMetricsPersistedHistory'`、`go test -race ./internal/monitoring` 和相关包 vet 成功；未无理由重跑全部慢检查。
- 新增/更新测试覆盖旧 Agent 批次→WSS→新表→历史 API、旧样本重启去重、资产-only 不补桶、513 主桶拒绝、600 节点跨两次整节点刷盘、1024 节点上限、4 MiB 单节点整批拒绝不推进水位、字节计数字符串精度、NULL、旧监控库拒绝与非监控数据保护、节点删除后缓存释放。
- `git diff --check`、Schema/模型生成一致性通过。Python 模块缓存加入项目忽略规则。

### 当前必须保留的缺口

**历史 Web 消费端仍使用旧 stats map 响应，本轮类型化 API 变更后尚未适配；不能部署或宣称历史页面已可用。** 此缺口由计划 P5 的 API 类型/图表语义更新完成。当前实时 SSE 未切换新协议，仍是原有行为。

P1 的主要运行路径已接入，但最大设备/节点压力、真实 RSS/事务延迟、崩溃进程级与所有 T01–T37 的总验收仍未全部完成；不能仅凭单元/SQLite 测试宣布整项目或全部阶段验收完成。

下一步按依赖进入 P2：检查 `internal/auth/agents.go`、`internal/storage/agents.go`、Gateway 会话/单 writer 和 REST 注册，补齐 v3 的运行时结构/跨字段校验、统一接收服务、受认证 POST、接收回执及能力/会话绑定。当前 v3 仍不宣告，不允许仅凭类型定义启用双出口。之后 P3/P4/P5 继续采集、账本、会话/调度/租约和 Web。

无提交、tag、发布、部署或服务重启。原目标、双仓库范围及实机/长稳验收保持不变。

## P2 第一批：严格传输解码与权威 Schema 运行时校验

本轮已重新核查认证和路由真实实现：文件为 Panel `internal/auth/agent.go`、`internal/storage/agents.go` 和 `internal/httpapi/router.go`。`ConsumeChallenge` 会删除节点旧会话；`AgentSession` 目前将过期、缺失和身份拒绝统一为 Unauthorized。P2/P4 仍必须实现错误分类及单会话协调，不能每次 POST 独立 challenge。

已实现并同步双仓库：

- `protocol/v1/telemetry_decode.go`：HTTP 1 MiB / WSS 64 KiB 独立边界、16 层嵌套限制、UTF-8 验证、严格未知字段/数字解码、拒绝重复 JSON 字段（含转义后同名）和尾随 JSON。旧 `Decode` 的 64 KiB 边界未放宽。
- 生成器将 Panel Schema 中 Telemetry 定义确定性写入模型；新增 Schema 关键字/format 若运行时不支持会在生成检查阶段失败，避免悄悄漏校验。
- `telemetry_schema.go`：从生成规则加载固定校验器并一次编译正则；检查类型、必填/非 null、未知属性、整数精度和范围、数组/字符串边界、枚举、日期/IP/UUID、唯一枚举、if/then/else/not/allOf/anyOf。
- `DecodeTelemetryBatch` 完成严格结构与 node/run/sequence/time 上下文校验。**尚不等于完整业务语义校验或授权**；内存/Swap/容量关系、错误与字段一致性、设备唯一性、月起点及过滤规则等跨字段检查仍待下一步补齐。

本轮验证：两仓库 `go test ./...`、`go vet ./...` 和 `go test -race ./protocol/v1` 全部通过；两边生成文件与运行时校验源码一致；生成检查与 diff 空白检查成功。新测试覆盖大小精确边界、重复 key、深度、分数/溢出整数、缺必填、null、非法错误码、错误日期、301 样本/33 文件系统、跨节点、C>R 和租约/回执条件。

没有新增 POST 路由、没有启用 v3、没有改变旧认证边界。下一步先补跨字段语义，再实现共享接收服务及 authenticated session/capability/run 绑定、POST 和 WSS 回执。历史前端响应适配缺口仍待 P5；整体目标未完成，未部署或发布。

## P2 第二批：跨字段语义与正反例

双仓库新增 `telemetry_semantics.go`，由 `DecodeTelemetryBatch` 在结构/身份上下文检查后执行整批语义检查：错误标记与值一致性、RAM/Swap/Socket/Load 完整性、设备唯一性与聚合整数上限、基线不携带速率、正采样间隔、磁盘 await 与 IOPS 关系、文件系统容量汇总/保留空间/绝对规范路径、OS 控制字符、公网 IP 可用性、UTC 月起点及 filter revision。设置应用结果还必须与 applied revision 一致。示例补齐 Swap 和缺失组错误标记；权威 Schema 不变。

新增双仓库一致的语义测试：完整 fast/full/IP 有效样本，以及 26 个结构合法但跨字段矛盾的变体，另含设置结果校验。每个负例先断言 Schema 合法，保证实际覆盖语义拒收，未被结构检查提前拒绝。Panel 契约文档同步说明这些规则。

验证：双仓库 `go test ./...`、`go vet ./...`、`go test -race ./protocol/v1`、`git diff --check` 全部通过。

尚未新增共享 receiver、POST/WSS v3 入口或会话授权绑定；因此未宣称真实传输下的整批拒收/水位不变已完成集成验证。下一批实现该接收边界及测试，随后继续 P3–P7。v3 未宣告，历史前端旧 stats 响应适配仍待 P5，未部署/发布，整体目标仍进行中。

## P2 第三批：共享接收服务和会话错误分类

Panel 新增 `internal/monitoring/telemetry_ingress.go`：能力/设置交换成功后由管理连接调用 Activate，绑定认证会话哈希、server generation、设置与到期时间；首个完整校验批次绑定 run。相同会话重连保留 run 与速率预算，旧 generation 的 WSS 帧拒绝，HTTP 不依赖管理连接在线。全部语义校验后才调用 Recorder，解析期间被替换的会话/设置再次校验。最多 8 个并发解析、1024 节点，双通道共享每秒 16 请求/1 MiB、突发 32 请求/2 MiB；非法请求也消耗预算。取消和 Forget 有明确拒绝路径。

新增真实 Recorder 状态测试（存储接口使用已有内存测试库）：合法 fast 后出现非法 full 不建立 run、不推进水位；纠正后两个组均接受；同批跨 HTTP/WSS 第二次只计 duplicate；Flush 得到两个检查点；断开管理不删除 HTTP 授权；旧代次、同会话切换 run、过期、取消、共享限流与恢复覆盖。此为公共入口单测，尚非网络端到端证据。

认证：`AgentSession` 对已知过期、吊销、禁用分开分类，均保持 `errors.Is(ErrUnauthorized)`；永久拒绝优先于过期。ConsumeChallenge 不再删除其他节点过期会话，仍替换当前节点所有旧会话；保留每节点最后一份会话用于安全刷新分类。未知/已删除 token 仍是 Unauthorized，不能按过期刷新。真实 SQLite 测试覆盖分类、优先级和另一节点登录不会抹去过期分类。

已通过 Panel `go test ./...`、`go vet ./...`、完整 `scripts/validate.sh`（日志 `/tmp/viorka-monitoring-p2-ingress.log`），monitoring/storage race 与新增认证测试通过。全仓库 race 正在执行，结果下一条记录补充。

下一步仍需接线：POST 路由/错误码/Retry-After/OpenAPI，Gateway ready/settings-result 与单 writer 回执，删除/重置时 Forget，最新观测/新鲜度与租约状态。当前 ingress 尚未装配到运行服务；不宣告 v3。P3–P7 与前端历史响应适配继续保留，未发布或部署。

补充：Panel 全仓库 `go test -race ./...` 已成功退出（0），日志 `/tmp/viorka-monitoring-p2-race.log`。新增跨节点过期分类测试在该轮启动后加入，已单独通过 race 检查。初次该测试复用唯一公钥触发预期数据库 conflict，修正为不同节点唯一 key 后通过，未为测试放宽约束。

## P2 第四批：受认证 POST 路由与 TLS 验证

新增 `POST /api/v1/agents/metrics` 和独立 handler，使用共享 ingress：只接受单一 Bearer，拒绝 Cookie/Origin/查询参数，严格 application/json 未压缩格式，1 MiB MaxBytesReader 和最多 8 个并发读体。读取后再次验证凭证；过期/无效/禁用/吊销分别映射安全 401 错误。成功返回接收回执，不表示已耐久保存；已解码批次拒绝返回对应回执，其余返回普通 API error。429 和过载 503 提供 Retry-After 秒数。

路由在 Dependencies 中显式注入 TelemetryIngress。缺失依赖返回 unsupported_protocol，未协商绑定返回 stale_session；生产 app/Gateway 尚未装配启用 v3。OpenAPI 经统一路由注册/Go DTO 重新生成，记录特殊大小限制、错误分类和错误体联合 Schema；没有手改生成 YAML。

真实 httptest TLS + SQLite 测试覆盖未协商拒绝、管理 WSS 未建立时上传、重复回执、实际刷盘后 CPU count=1、不改变节点管理 online、Cookie/缺凭证/URL/Origin/错误媒体/超大请求/未知字段/跨节点/限流/Retry-After/过期分类/吊销。协商在此测试中显式调用 Activate；尚不能替代实际 ready/settings-result 或 Agent 调度端到端验收。

Panel 全量 go test、go vet、scripts/validate.sh 通过，日志 `/tmp/viorka-monitoring-p2-post-validate.log`；HTTP/monitoring race 仍在运行，结果补记。下一步接入 Gateway 协商与回执、v3 设置存储/应用、删除清理和共享服务装配，再继续 P3–P7。能力未宣告，未部署/发布。

补充：HTTP/monitoring race 已成功退出，日志 `/tmp/viorka-monitoring-p2-post-race.log`。最后加入的真实历史计数/online 状态断言已单独通过 race 测试。`git diff --check` 通过。

## P2 第五批：独立 v3 设置与 Gateway 协商/回执

Panel 新增 `0010_telemetry_settings`：节点级 v3 设置、独立 revision/结果、C/R/W 数据库范围约束、删除级联。缺行代表现有节点默认值，不复制或重解释旧设置。保存使用预期 revision、事务审计，清除旧结果；迟到结果不能标记新 revision，也不能将成功倒退。真实 SQLite 测试覆盖 C>R、旧协议不变、审计失败回滚、冲突、迟到/失败回执、删除不复活、重开数据库恢复、down SQL。

Gateway 新增启动期显式 ConfigureTelemetry 门槛和 `telemetrySession`：hello_ack 后独立 ready，等待关联的 v3 settings applied 回执后激活 ingress；30 秒待确认超时；设置唤醒发送新版设置；指标回执使用既有单 writer，关联上传 message_id。每次 v3 上传重新认证，控制与指标速率预算分开；已启用 v3 的会话拒绝继续写旧指标。协商激活与 revoke/reset/delete 清理采用同一 Gateway 锁，避免迟到设置回执恢复失效绑定。断开管理不清除已协商 HTTP 绑定。

真实 TLS/WSS/POST + SQLite 集成测试覆盖 ready 前置、应用前 POST 拒绝、WSS 接收回执、POST 同组 duplicate、设置动态推送/确认、数据库只累计一次、WSS 关闭后 POST 可用。该测试显式启用 Gateway gate，生产 app 尚未开启；当前 Agent 未宣告/发送 v3。

已通过 Panel 全量 go test/vet/scripts/validate.sh（日志 `/tmp/viorka-monitoring-p2-gateway-validate.log`）；后续锁修复单独通过 HTTP 删除/POST/WSS race；全仓库 race 和新增重开数据库测试仍在执行，结果补记。

剩余：共享最新观测/新鲜度 DTO 与 SSE，v3 REST 设置表单/API，租约需求及控制优先级，生产 gate/依赖装配，失败回执/背压等完整端到端覆盖；继续 P3 采集/月账本、P4 调度和 P5 前端。历史旧 stats 消费端仍待适配。整体未完成，未发布/部署。

补充：全仓库 race 已成功退出，日志 `/tmp/viorka-monitoring-p2-gateway-race.log`。最后追加的设置重开恢复与迁移检查已单独通过 storage race；diff 空白检查通过。全仓库 race 启动后的 Gateway 锁修复由前述 targeted HTTP race 覆盖，未宣称先前运行覆盖后续源码。

## P2 第六批：v3 设置 REST 与应用状态

新增 viewer GET / admin PUT `/api/v1/nodes/{id}/telemetry-settings` 和 viewer GET `/status`。沿用 Web 身份、Origin/CSRF 与审计，严格权威 TelemetrySettings Schema 解码（含重复键、缺字段、null 和未知字段），拒绝查询参数；独立 C/R/W、默认值、C>R、预期 revision、pending/applied/failed 状态。保存仅代表 Desired，不宣称 Agent 已应用或 v3 已开启。OpenAPI 通过生成链更新。

Gateway 增加已协商 v3 的连接索引和 NotifyTelemetrySettings，准备新设置不会唤醒/关闭 legacy-only 连接；连接退出删除索引。真实 TLS/WSS 集成测试改为通过 REST PUT 保存，并验证单 writer 下发与 Agent 结果确认。

新增 API 测试覆盖默认值、C>R、范围两端拒绝、未知模式、null/重复/缺失/额外字段、revision 冲突、401/403/404、跨节点不变、旧设置不变，以及结果确认/更新后重新 pending。Panel `go test ./...`、`go vet ./...`、完整 validate 通过（`/tmp/viorka-monitoring-p2-settings-validate.log`），新增 REST/WSS 路径通过 targeted race；diff 空白检查通过。

下一步推进 Agent P3 分组采集/接口过滤/月账本，随后 P4 接线。P2 的最新快照/新鲜度、拒绝回执与背压细节仍需随 P4/P5 总集成补齐；v3 生产开关关闭，前端历史响应适配和全部实机/长稳验收未完成。没有部署或发布，整体目标保持进行中。

## P3 第一批：本地接口规则与独立网络计数基线

Agent 新增受约束 `network_filter.include/exclude`，每列表最多 64 项、每项最多 64 字节，字面名称/末尾前缀星号；默认排除→显式包含→显式排除。拒绝重复、不支持字符/Shell/正则；规范排序生成含默认规则版本的 SHA-256 filter_revision，等价列表重排保持 revision。配置说明明确字段尚未接入旧运行调度，不宣称已启用。

新增 `collector.TelemetryNetwork`，单采样所有者、只读 procfs 与 net.Interfaces，注入接口枚举以便复现：boot_id/ifindex/name 基线、无损 uint64、实际 elapsed、600 秒区间上限、首次/身份变化/计数倒退/异常恢复只报告质量标记。总计 signed64 溢出与速率协议边界拒绝；失败清空基线、不返回部分快照当成功。返回独立快照，不由下次采样改写。先应用接口过滤，再检查 64 个选中接口；原始枚举最多 4096、procfs 读取 256 KiB，旧 parser 保留 64 个接口和 loopback 排除行为。

测试覆盖过滤优先级、revision 稳定/变化、配置所有权、实际 30 秒→2 秒间隔、ifindex/boot 更换、计数倒退、601 秒/时钟回拨、读取失败恢复、显式包含 loopback、缺身份拒绝，以及 70 个默认排除 veth 不占选中上限/显式纳入后拒绝。collector/config race 与 diff 检查通过；完整 validate 成功（`/tmp/viorka-monitoring-p3-network-validate.log`）。Linux amd64/arm64 交叉编译结果待本轮最终补记。

尚未连接 fast/full/IP 分组调度，也未实现 traffic.dat；当前网络采集器尚未被运行客户端启用。下一批继续 P3 CPU/内存/Socket fast、full 磁盘 IOPS/await/util 与独立 IP，再做月账本和 P4。没有 Linux 实机采集证据，不能以交叉编译代替运行验收；P2/P5 剩余集成缺口不变。

补充：`CGO_ENABLED=0 GOOS=linux GOARCH=amd64/arm64 go build -trimpath` 均成功，输出分别为 `/tmp/viorka-agent-linux-amd64`、`/tmp/viorka-agent-linux-arm64`。最后追加过滤前后数量上限测试通过 targeted race；未声称 Linux 运行测试完成。

## P3 第二批：fast 分组采集

新增 `collector.TelemetryFast`，为单个 Agent run 持有独立 CPU/network 基线和单调组 sequence；仅读取 stat、meminfo、uptime、sockstat/sockstat6、net/dev、boot_id 与接口身份。不调用旧 Sample，不读 loadavg、不枚举进程/文件系统/磁盘。采样时间显式传入，实际 elapsed 最多 600 秒；首次/重启/计数倒退无 CPU 假零，模式改变不重建对象。socket 提取为新旧共用小函数，保持 inuse、IPv4/IPv6 合并、缺 sockstat6 可接受及失败未知语义。

新增内存整数解析：kB 精确转 uint64 bytes，校验 signed64 溢出、字段重复/缺失、整数和容量关系；RAM/Swap 成组提供。采集失败使用固定 unavailable 错误。traffic.dat 尚未实现，fast 暂时明确 traffic_unavailable，不将内核累计当月累计。取消返回错误，不发送假成功观测；非 Linux 默认 proc 路径返回 unsupported_platform。

procfs 夹具测试直接将每次真实采集结果装入 TelemetryBatch，经 Schema+身份+语义解码验收；覆盖初始基线、真实 CPU/net 差值、boot 更换、IPv6 缺失/损坏、memory 精度/溢出/重复、取消，以及 disk 回调若被 fast 调用立即失败。collector race 通过。完整 validate/交叉编译正在执行，结果补记。

尚未接入实际客户端 v3 调度；full 组（设备 IOPS/await/util）、IP 独立刷新、月账本和 P4 仍未实现。下一批继续 full 的磁盘计数与容量采集，不缩减原始交付/实机/长稳范围。没有部署或发布。

补充：完整 `scripts/validate.sh` 成功（`/tmp/viorka-monitoring-p3-fast-validate.log`），包含全量 Go test/vet、协议生成与安装/发布脚本检查。Linux amd64/arm64 CGO=0 交叉编译均通过，diff 空白检查通过；仍未进行 Linux 实机采集验收。

## P3 第三批：磁盘 I/O 明细与独立公网 IP

新增 `TelemetryDisk`，full 周期单一基线所有者，解析 Linux diskstats 的扇区、完成次数、耗时和忙碌毫秒。无损转换 512-byte 扇区并校验 signed64 溢出/设备数/身份。逐设备计算 bytes/s、IOPS、read/write/combined await 和 util；不求全机 I/O 总量。分母为零 await 为 nil；非法 util 省略且标记 partial，不静默钳制。首次、boot 变化、计数倒退、长间隔、解析失败后恢复均重新立基线。设备名称/major:minor 仍仅是可见身份，不宣称永久硬件 ID。

磁盘 fixture 测试覆盖完整公式、idle await 未知、超范围 util、reset/reboot/601 秒间隔/失败恢复、整数/设备数/重复身份限制。每个正常/降级采样都经过完整 v3 Schema 与业务语义解码。

新增 `TelemetryPublicIP` 独立 600 秒缓存，使用既有固定 HTTPS 探测源，不随 full OS 采集刷新；最多 12 秒整个刷新 deadline。缓存复用保持原 sampled_at/sequence，返回深拷贝错误切片；探测失败保留新 unknown 观测，父 context 取消返回错误。内部超时不导致紧循环重试，未知结果也按刷新周期缓存。测试只使用可控 lookup，不向公网发送探测请求。

collector race 通过；完整 validate 与 Linux 双架构交叉编译正在执行，结果补记。新组件尚未接入实际 Agent 客户端；full 容量/OS/load/process 组合、traffic.dat 月账本、P4/P5/P6/P7 及 P2 剩余集成仍未完成。下一轮继续 full 无损容量与完整组装。未部署/发布。

补充：完整 `scripts/validate.sh` 成功（`/tmp/viorka-monitoring-p3-disk-ip-validate.log`）。最后 IP 内部超时处理调整后 collector race 再次通过，Linux amd64/arm64 CGO=0 交叉编译与 diff 检查成功；无 Linux 实机/长稳或真实公网探测验收。

## P3 第四批：full 组装与无损文件系统容量

新增 TelemetryFull：独立 sequence/elapsed、load、proc 进程计数、OS release、每设备磁盘 I/O 与文件系统容量，不读取 fast stat/meminfo/uptime/net，不调用公网 IP。OS 读取保留受限文件大小和既有解析器；取消返回错误，非 Linux 默认源明确 unsupported_platform。full 的磁盘基线独立于 fast 周期。

新增容量采集：Linux 使用 O_DIRECTORY/O_NOFOLLOW 打开目录后 Fstatfs，拒绝最后一级符号链接；block 数全程 uint64，转换和汇总限制 signed64。total=blocks×size、used=(blocks−bfree)×size、available=bavail×size，保留 reserved 差额。复用本地文件系统白名单、mountinfo 路径/候选数量限制，按 device/fsid/btrfs source 去重后汇总，最多32个；选中资源失败不返回部分总量。此只读路径不修改任何挂载/服务。

测试：完整 procfs 夹具删除 fast 文件后 full 仍成功；每次完整/失败观测通过权威 Schema+语义校验。覆盖容量整数精度、reserved、别名去重先于求和、溢出拒绝、单挂载失败全组未知、OS 失败、真实 full 磁盘间隔、快照不变。collector race 已通过。新增 Linux 专用真实临时目录 Fstatfs/最终 symlink/普通文件拒绝测试，但当前 macOS 主机未运行该测试；随后进行 Linux 双架构测试二进制编译。

当前完整 validate、Linux 构建和测试编译正在执行，结果补记。剩余重点为 traffic.dat 月账本、P4 会话/调度与 P5 集成/前端；P2最新快照/新鲜度/控制背压缺口仍保留。当前实现没有硬中断阻塞内核 filesystem syscall 的能力；只对白名单本地挂载读元数据，真实慢挂载/压力与有界退出仍属后续实机验证，不宣称已验证全部 I/O deadline。未启用 v3，未部署/发布。

补充：完整 validate 成功（`/tmp/viorka-monitoring-p3-full-validate.log`），包含全量 Go test/vet。Linux amd64/arm64 collector 测试二进制 `go test -c` 和 Agent CGO=0 build 均通过，diff 检查通过。测试二进制仅交叉编译，未在 Linux 执行；不等同实机通过。

## P3 第五批：月流量账本状态机

新增 state.TrafficLedger/TrafficSnapshot，独立于瞬时速率：绑定 Panel/Node、boot、filter_revision、UTC month、逐接口名称/ifindex/uint64 counters、累计和采样/保存时间。单所有者处理，快照/恢复均复制切片。初次缺基线报告 partial；同月增量逐接口累加；重启/身份变化/计数倒退不累加可疑差值；规则变化保留已有累计，继续相同接口，新接口仅立基线，标 mixed_filter。

相邻月将边界差值归入当前月并标 boundary_estimated；跨多月重建基线并标 period_gap。旧时间/未知字段语义/重复接口/计数溢出拒绝；月累计溢出或恢复身份/结构不合法进入阻断状态，输出 unknown 而不是新建零账本。恢复的累计和接口基线来自同一快照，避免 Agent 重启重复累计。

state race 测试覆盖恢复后补增量/重复计数、快照所有权、boot/计数 reset、时间倒退不改状态、相邻/多月、filter 新接口、累计溢出、跨身份恢复拒绝且后续不能假装空账本。完整 validate 正在执行，最后快照时间验证调整已通过 state race/vet。

**尚未实现 traffic.dat 文件读写、完整性封装、60 秒保存重试、退出保存或 fast 集成。** 现有 Store.Read 的 Lstat→Open 存在可避免的最终文件跟随窗口；下一批为固定 traffic.dat 使用 O_NOFOLLOW + fd.Stat 验证并保留损坏/不安全文件，不直接用现有通用读函数假称安全。已检查 Store.Write 的临时0600文件、fsync、rename和目录sync；下一步复用这些耐久性步骤并验证失败路径。运行链路仍未启用 v3，后续 P4–P7 与实机验收不变。

补充：完整 scripts/validate.sh 成功，日志 `/tmp/viorka-monitoring-p3-traffic-core-validate.log`，diff 空白检查通过。本批为平台无关账本算法，未进行 Linux 实机/文件崩溃恢复验证；文件恢复将在下一批使用真实临时目录测试。

## P3 第六批：traffic.dat 安全文件与保存重试

新增固定 traffic.dat 64 KiB 完整性封装（版本化快照+canonical SHA256）。读取使用 O_NOFOLLOW/O_NONBLOCK 打开后 fd.Stat，检查目录0700、文件0600/普通文件/当前用户/单硬链接，严格 JSON（重复键/未知字段/尾随内容）和绑定/语义校验。校验检测损坏，不是抵御文件所有者恶意修改的认证。

OpenTrafficLedger 对损坏/不安全/绑定不匹配返回 blocked ledger+error，允许上层保留其他监控；不可将其替换成空账本。保存检查已有文件内容摘要，外部替换或删除不被静默覆盖。复用 Store.Write 的临时0600、fsync、rename、目录sync，成功验证实际落盘内容后清除dirty。失败保留内存累计/persist_failed并60秒限频重试；识别rename后目录sync失败时已经写入的精确内容，避免下轮误判为外部修改。force仅用于正常关闭的一次最终尝试；context取消和磁盘syscall不能硬中断的限制已记录。

真实临时目录测试通过：保存关闭重开后恢复增量不重复、0600、损坏JSON/校验/权限/符号链接/跨Node原文件保留、有效外部替换保留、写失败限频、rename后失败重试、最终保存、取消、状态质量恢复。最初测试直接使用测试框架的非0700目录，被现有权限检查正确拒绝；改为Store.Open创建专属子目录，未放宽权限。

state race/vet通过；完整validate和Linux双架构state测试二进制编译正在执行，结果补记。仍未接入fast账本更新、异步保存/退出生命周期和P4调度；慢磁盘/进程崩溃/真实Linux运行验收尚待P6。能力未启用，未部署或发布。

补充：完整validate成功（`/tmp/viorka-monitoring-p3-traffic-file-validate.log`），包含全量Go test/vet；Linux amd64/arm64的state测试二进制编译通过。最后外部替换测试通过state race/vet；diff检查通过。Linux测试二进制未执行，不等同实机或断电恢复验收。

## P3 第七批：fast 账本接入与不阻塞采样的保存

TelemetryFast 增加启动期 SetTrafficLedger，网络采集完整成功后逐接口提交 boot/filter/name/ifindex/counters；network 失败不以空列表更新账本，恢复后仍可补同月增量。账本错误返回 unknown Traffic，并通过 TrafficError 提供诊断，不使 CPU/RAM 等整个 fast 观测失败。月累计恢复与瞬时速率基线分离：新 sampler 首次无速率，但能从持久化账本恢复月增量。

TrafficLedger 现使用短 mutex 保护累计与快照，Save 在独立 TryLock 下复制一致快照后释放采样锁再写磁盘；不创建内部后台 goroutine。并发 Save 明确 ErrTrafficSaveInProgress，不排无界队列；写盘期间的新样本保持 dirty，旧写入不能覆盖新基线或误标已保存。后续 P4 生命周期仍需拥有单个保存 worker、等待/取消与最后一次尝试。

受控阻塞 writer + 实际文件保存测试验证新 Observe 不被磁盘写入阻塞、旧快照一致、新样本保留且下次真正刷盘。procfs→fast→账本→真实文件→重新加载完整集成测试通过 Schema/语义检查，覆盖 rate baseline 与月恢复分离、网络错误保留基线、损坏账本 unknown 而其他指标继续。state/collector race 正在本轮完整检查中执行，结果补记。

当前尚未装配客户端 v3 调度/保存/退出生命周期，不能宣称运行中的 Agent 已启用新采集。下一步进入 P4 会话协调与调度，仍需 P2 最新状态/SSE 和 P5 UI、全矩阵/Linux实机/长稳验证。没有发布或部署。

补充：state/collector race、完整 scripts/validate.sh（全量 Go test/vet、协议生成及安装/发布脚本检查）全部通过，日志 `/tmp/viorka-monitoring-p3-traffic-integration-validate.log`，diff 检查通过。未新增 Linux 实机、慢fsync或进程级断电验收证据。

## P4 第一批：进程内共享会话协调

新增 sessionCoordinator，按 Panel/Node/本地身份摘要绑定，单个有界 challenge/sign/session 请求，等待者各自可取消，不创建裸后台 goroutine。缓存 token/expiry/local generation；到期前30秒统一刷新，短暂失败至少1秒共享等待，永久身份/TLS错误阻断后续签发。Reject按generation检查，旧请求/刷新期间旧token拒绝不能撤销新凭证；只有明确expired分类允许刷新。

现有 WSS connect 已改用共享凭证，临近expiry有界退出重连，协调器刷新通知通过原writer循环select处理，不新增writer。会话被永久401/403拒绝时阻断身份，延迟旧代次拒绝转为受控重连。仍使用严格TLS/hostname和已绑定的签名challenge；未改旧hello能力声明，没有启用v3。

测试：32并发Get只签发一次、expiry换代/旧代次拒绝/身份隔离/永久阻断、取消等待、刷新期间旧拒绝、临时失败预算。真实TLS服务验证Ed25519签名，16并发credentials只产生一次challenge/session，后续复用。相关race测试通过；完整validate及connection race正在运行，结果补记。

尚未实现Agent v3 POST上传、ready设置应用、lease/C/R/W调度、统一熔断、保存worker与退出接线。当前改变只消除管理重连重复签发，为双通道共享准备基础；不会把契约/协调器等同完整P4。P2最新状态/SSE、P5前端与所有实机/长稳门槛保持未完成。

补充：完整scripts/validate.sh和全connection race均成功退出，日志分别为 `/tmp/viorka-monitoring-p4-session-validate.log`、`/tmp/viorka-monitoring-p4-session-race.log`。diff检查通过；ENROLLMENT文档已说明当前内存共享/临期刷新行为和v3尚未启用的界限。未部署、发布或执行真实节点操作。

## P4 第二批：Agent v3 设置与独立周期计算

新增独立 telemetrySettings（Panel/Node绑定、revision/conflict/persist_failed/applied），使用固定 node-telemetry-settings.json，完整写入后才改变内存设置；重复revision重写以验证耐久性。不读写旧node-settings.json。新增安全读取方法，0700目录、O_NOFOLLOW/O_NONBLOCK打开后fd.Stat检查0600/owner/单链接/普通文件，16KiB/严格JSON；跨节点和symlink拒绝。

新增单owner telemetryCadence，只管理deadline、不重建任何采集基线：初始fast/full采样，POST按R；C/R独立且C>R有效；实时模式W覆盖fast并暂停POST；R修改只影响full/HTTP POST，实时C只保存，W修改只改变实时fast/send；模式退出保留full相位；逾期只执行一次、不追赶补跑。模式切换函数明确要求等待在途POST结束后调用，实际传输状态机尚未接入。

测试覆盖持久化重载、旧文件不变、写失败不假applied、低/冲突/非法revision设置、同revision幂等、绑定/符号链接安全，及默认C30/R60、C2/R60、C60/R30、动态重排、实时切换和无catch-up。targeted connection race已通过；全量validate和Linux安全读取编译正在执行，结果补记。

下一步仍需Agent v3 POST与错误分类/退避、ready接线、租约、采样/保存worker生命周期及整个C/R/W状态机集成；以上类尚未改变现有生产模式，不宣告v3。P2最新快照/SSE/P5历史和设置UI/总验收仍未完成，未发布部署。

### 最终交付要求更新

用户将目标更新为：完成交接/实施计划后，commit & push 两个仓库。该操作已明确授权，作为最终交付步骤执行；不能以当前阶段进度代替整体完成后提前交付。提交前核查两仓库git状态/分支/远端/用户改动归属及完整验收证据，保留无关改动，不发布部署。

补充：完整scripts/validate.sh成功（`/tmp/viorka-monitoring-p4-settings-validate.log`），最后目录权限检查后targeted connection race通过；Linux amd64/arm64 state测试二进制交叉编译和diff检查成功，未声称Linux运行通过。

## P4 第三批：Agent 单次指标 POST 与响应分类

新增 uploadTelemetry：接收已经完成协商的共享 credential，不自行 challenge、不自行重试/补传；15秒 context、固定 /api/v1/agents/metrics、Bearer header、application/json，发送前完整v3批次校验/1MiB上限，响应16KiB。200仅接受严格Schema/重复键检查、正确batch_id、accepted且计数守恒的回执。过期仅401明确session_expired允许协调器刷新，永久身份拒绝阻断；429解析秒数或HTTP日期Retry-After并防duration溢出；5xx归overloaded，404/405归unsupported_protocol，其他格式/状态矛盾拒绝。TLS错误保留原始错误分类并阻断当前代次，旧代次结果不会撤销新凭证；迟到成功也返回stale_session，不得作为新会话确认。

真实TLS响应测试覆盖成功/duplicate、错误batch/count、重复JSON键、expiry/revoked/invalid/模糊401/伪403 expiry、rate/overload/proxy failure/缺端点、非法Retry-After，以及每次只发送一次。受控在途请求期间更换会话，旧成功回执不影响新generation。targeted race通过；完整validate仍在执行，结果补记。

尚未接入运行scheduler，也未实现统一拒绝阶梯/120秒冷却/半开、v3 ready/lease和worker生命周期；不会因上传方法存在宣告新版已可用。下一步接统一重试状态与有界发送窗口，再组装P4。全部完成验收后commit/push两个仓库要求保持不变。

补充：完整scripts/validate.sh成功（`/tmp/viorka-monitoring-p4-post-validate.log`），共享会话/POST/Retry-After相关race通过；最后迟到回执测试清理路径改为先释放受控请求再关server，防失败时测试自身阻塞，并再次通过race。diff检查通过。未运行真实节点或宣告双出口链路完成。

## P4 第四批：跨出口重试预算、窗口所有权与临时租约

共享 telemetryRetry 由调度器单 owner 管理 HTTP/WSS 尝试：POST 起点至少间隔 R；429/过载前两次按 R，随后2/3/5/7/10分钟；Retry-After可长于10分钟；熔断到期只允许一个HTTP探测。普通网络失败不计拒绝阶梯，WSS失败允许受R约束的首次HTTP兜底。协议错误相关出口冷却120秒；过期/旧会话冷却双方；永久身份/TLS阻断，旧generation完成不能影响新代次。成功清除拒绝阶梯但保留尚未结束的协议冷却；协商解除unsupported也检查generation。非法Retry-After的429仍归rate_limited。

上批完整validate（`/tmp/viorka-monitoring-p4-retry-validate.log`）成功；最后generation fencing改动后的定向race成功。

新增 telemetryWindow：采样时编码并取得不可变数据所有权，300 fast/8 full/2 IP及保留2048字节元数据的1MiB上限，溢出删除最旧并计数。pending与单个inflight分离，release在所有发送结果后统一丢弃，不回放失败窗口。每R冷却边界清空pending，最新三组快照保留原sequence/time；最新兜底本身也受1MiB限制。fast-only窗口可附带慢组缓存。WSS只编码当前一帧，按完整Envelope 64KiB约束，单组超大明确拒绝；中途失败必须release剩余整窗口。内存设计上限明确包含pending、inflight、三个最新缓存和一次编码；这还不是进程RSS实测结果。

新增 telemetryLease：启动/重连新对象不恢复租约；绑定服务端generation，严格递增revision，同revision相同内容只返回实际状态、不延长90秒期限，冲突/迟到/跨generation拒绝。接收启用仅记录pending，settle必须等当前sender空闲才切换模式并产生applied回执；更新命令可取代pending意图。过期回退HTTP、断线立即失效，所有期限使用进程时间保留monotonic信息。

确定性测试覆盖窗口300/301、实际字节截断、collector复用不修改在途数据、在途新采样保留、冷却最新兜底、逐帧完整协议解码、超大单组拒绝，以及租约重复/续期/等待发送/覆盖/到期/断线/换代。window/lease及retry定向race通过；完整validate正在执行，随后补记。

仍未接入实际Client调度器，不能将以上辅助状态机称为P4完成。下一步需要跨WSS连接生命周期的scheduler、独立fast/full/IP与ledger保存worker、唯一WSS writer控制优先、ready/settings/demand/receipt接线和实际TLS双通道集成；生产v3仍未启用。P2最新快照/SSE、P5 UI（历史DTO适配尤其必要）、P6/P7完整验收及最终双仓库commit/push保持未完成。

补充：完整scripts/validate.sh成功（`/tmp/viorka-monitoring-p4-window-lease-validate.log`）；随后将WSS拆帧从保守元数据预留改为测量实际空Envelope、按已编码样本和分组逗号精确累计，增加恰好65536字节通过/多1字节拒绝测试。最终window/lease定向race、connection vet及git diff --check均通过。完整validate先于最后精确拆帧改动，最终改动由上述定向检查覆盖。未执行Linux实机/资源长稳测试，也未启用生产v3或提交推送。

## P4 第五批：独立采集/保存 worker 与有界退出

新增 telemetrySources 生产装配，fast/full/IP collector 在单次运行内各实例化一次，fast接入同一个本地traffic ledger；不因重连/周期切换重建基线。损坏或不安全账本作为独立startup diagnostic保留，其他采集继续；blocked ledger仍连接fast和save，不创建新零账本。真实临时目录集成测试确认损坏文件未覆盖、fast仍返回观测、最终save明确失败。

新增四个固定worker（fast/full/IP/traffic save），每组最多一个未完成操作；request每组1槽、results总计4槽，scheduler拥有busy标记并消费结果后才允许同组再次提交。fast/full最多5秒context，IP12秒，保存5秒；全程不为单次采样新增goroutine。受控full/IP/save同时阻塞时fast仍可连续采样并进入telemetryWindow，重复慢任务不能排队。

退出取消普通操作，等待三个采集worker结束，再由唯一保存worker以独立5秒context执行一次force最终保存。stop由调用方deadline限制等待，超时和最终保存失败返回明确错误；重复stop可读取同一结果，停止后拒绝新提交。不能硬中断不响应context的内核fsync/statfs：测试模拟这种情况，stop明确超时，释放阻塞后worker正常结束，没有宣称强制中断系统调用。

完整scripts/validate.sh成功（`/tmp/viorka-monitoring-p4-workers-validate.log`）；worker/window/lease race通过；最后生产source损坏账本装配测试及worker race、connection vet、diff检查通过。尚未完成Client事件循环的实际接线，因此不能称为v3运行生命周期已交付或启用。下一步必须把这些worker、cadence/window/retry/lease与Client ready/settings/demand/receipt、POST异步发送、唯一WSS writer合为实际scheduler，随后继续Panel SSE/UI和P6/P7。最终双仓库commit/push未执行，完整目标保持不变。

## P4 第六批：统一调度与异步发送运行循环

新增 telemetrySchedule，把cadence、四个采集worker、pending/inflight窗口、共享retry、临时lease接成一个owner。HTTP到R取已完成样本，不等待full/IP也不额外采fast；实时W截止先启动fast，完成后才取窗口。pending启用在当前发送释放前不进入实时；退出/失败保留R尝试预算。WSS失败释放整窗口并使旧lease不可复活，兜底只取最新；冷却R边界清理积压，半开恢复前也清空积压。新会话保留在途所有权直到旧sender退出，迟到旧结果不能解除新代次故障。R/W修改同时重排deadline和未执行发送意图，避免旧意图绕过新周期。

新增 telemetryRunner：一个owner循环、一个固定sender、8槽本地事件/控制回执队列、1槽发送/结果队列，sender统一15秒deadline。收集结果、控制命令、发送完成与动态最近deadline唤醒，不用高频轮询。设置先持久化再改周期；pending demand保存correlation并在真正切换后出回执。控制消费堵塞明确退出并返回错误，不静默丢设置/需求确认。采集/保存诊断以最新完整快照合并，保留每组错误与完成次数。退出取消sender和collector，合并有界关闭/最终账本保存错误。

新增Client.telemetryTransport连接已有真实TLS POST上传与WSS逐帧交付回调；POST新增uploadTelemetryBody直接消费window编码字节，复用完整验证/鉴权/响应分类，避免decode再marshal请求。WSS每帧等待交付回调确认，失败立即终止剩余窗口，没有内部重试。管理socket的唯一writer/回执路由尚未实现该回调。

组合测试通过：C2/R60的31个fast窗口；在途POST期间仍采样；启用等POST完成后转移pending到实时，新fast完成前不发WSS；失败后R限制兜底且不补发；C60/R30缓存原sequence；管理断线不取消HTTP；冷却清积压、半开最新、旧generation迟到成功隔离；R/W更新重排未执行意图。

真实TLS运行循环集成测试通过：共享凭证POST被server受控阻塞，期间设置回执仍返回、启用不提前确认；server释放后才确认realtime并交出新fast frame。这里WSS交付端仍为回调测试，不能声称真实管理WSS端到端完成。

完整scripts/validate.sh成功（`/tmp/viorka-monitoring-p4-scheduler-validate.log`）；最后周期意图重排和startup诊断补充后，定向runner/schedule race、全connection race（`/tmp/viorka-monitoring-p4-scheduler-race.log`）、connection vet、git diff --check均通过。下一步继续Client生产生命周期接线、能力ready与独立设置、管理writer控制优先/metrics回执路由/超时/重连，再补真实TLS+WSS+POST组合验收。仍未启用v3广告，没有部署或commit/push；Panel SSE/UI/P6/P7全部后续范围不变。

## P4 第七批：WSS writer 桥接与回执隔离

新增 telemetryWSS/telemetryWSSPort：桥接不拥有socket，唯一管理writer取1槽frame request后写入；写成功只留下一个pending receipt，sender等待匹配batch_id/correlation/count和完整Schema校验的回执。写失败、等待超时/取消、断线均返回错误；错误/迟到correlation不影响新pending。端口仅writer访问pending，attach关闭旧端口；每个逻辑窗口开始时固定一次delivery快照，拆帧剩余部分不能跨重连迁入新管理代次。

telemetryTransport改用delivery factory以固定整窗口的连接归属。WSS明确session_expired/永久身份拒绝也作用于共享sessionCoordinator，关闭Changed通知并按代次拒绝；永久撤销不能从POST或新challenge绕过，旧代次拒绝归stale_session。

真实TLS WebSocket测试通过：服务端先接收metrics，再刻意等待node.heartbeat后才回receipt，证明等待指标接受不占住writer；socket.Write成功不能提前确认指标。其他race测试覆盖新旧port隔离、取消等待、错误计数和rate_limited回执、300样本拆帧中途重连不跨代次、identity_revoked同时阻断共享会话/不重新challenge。现有runner TLS POST测试仍通过。

完整scripts/validate.sh成功（`/tmp/viorka-monitoring-p4-wss-bridge-validate.log`）；最后补充拆帧换代/身份拒绝后定向WSS/runner race、connection vet及diff检查通过。当前桥接已在真实socket测试中使用，但尚未接入Client.connect的生产select/ready/settings/demand处理。下一批需要直接修改Client生命周期和writer分支，处理旧collector切换、控制优先与跨连接回执generation；不把桥接测试替代完整Client/Panel端到端验收。生产v3能力仍未启用，双仓库commit/push仍待整体完成。

## P4 第八批：Client 生命周期与真实双连接接线

Client.connect现在在启动gate启用时声明node.telemetry.v3，处理独立ready/settings/demand/receipt消息；新telemetryClientSession验证Schema/关联字段、应用并持久化设置、生成真实UUIDv4 run_id、装配生产sources/runner/共享credential/WSS bridge。gate目前仅内部测试启用，生产默认仍关闭，待Panel SSE/UI与总验收完成后统一配置。

首次ready成功后取消旧sampling并停止旧metrics ticker；runner归Run而不是connect所有，管理连接断开只失效lease/port，HTTP及采集基线继续。重连ready经runner串行重新应用设置/credential与server generation，控制回执携带内部generation，旧连接排队回执不能写到新连接。失败ready后的新设置可重新完成协商，不能因applied=false永远拒绝恢复。

管理select接入runner控制、诊断、完成、WSS frame request；控制回执/心跳优先，已有incoming任务先于待发metric frame；仍只有原writer调用socket.Write。框架限制每次metric写5秒，匹配receipt由桥接释放；不在writer等待指标ACK。settings/demand进入有界队列，堵塞明确错误，不默默丢确认。采集诊断按错误变化记录，避免逐样本刷日志。

Run/direct connect现在合并退出错误；原旧collector等待改为总体10秒有界，runner停止及最终ledger保存错误返回，不把超时或写失败作为成功。停止不操作代理业务进程。

新增真实Client TLS/WSS/POST测试（实际connect函数，生产装配路径的sources以本地fixture注入避免外部IP网络）：声明能力→ready落盘→实时enable回执→v3帧/匹配receipt→disable→POST复用run_id；同一Run作用域连续两次connect，重连要求新demand，run_id不变、fast sequence递增，最终save只执行一次。设置失败后重试协商和旧generation控制回执丢弃亦有定向测试。

完整scripts/validate.sh通过（`/tmp/viorka-monitoring-p4-client-validate.log`），全connection race通过（`/tmp/viorka-monitoring-p4-client-race.log`）。随后增强双连接测试/设置重试路径，最终Client/runner定向race、connection vet和diff检查通过。仍需完整Panel实际Gateway与此Client共同运行验收、过载/过期/撤销/ACK丢失全矩阵、旧Panel回退和能力gate配置，以及P2最新状态/SSE、P5 UI、P6/P7实机/时长验证。没有部署，双仓库commit/push待整体目标完成。

## P5 第一批：Panel SSE 需求引用与 Gateway 租约控制

本批修改Panel仓库。真实代码核查发现旧SSE已有授权范围/会话复核，但v3观测尚未进入其快照。先将服务端scope接到实时需求：monitoring.Demands最多128个订阅/1024个节点记录，按节点引用计数；scope Update原子校验，Close幂等；最后引用释放15秒宽限，新引用取消宽限；无后台goroutine/持久化。过期零引用记录可回收；回调在锁外，避免Gateway锁反转。

metricEvents在鉴权和节点范围解析后Acquire；成员变化Update，断开/访问失效/节点删除/写失败统一defer释放。REST快照/历史与全局events不创建指标需求。Gateway独立demandWake只通知v3连接，不为SSE变化反复读取旧settings。原唯一writer于首次引用或ready设置应用后发送node.metrics.demand，90秒TTL，30秒递增revision续期；无viewer宽限内不续期，到期停用。匹配generation/lease/revision/correlation的回执才记录实际模式；15秒未确认记degraded，停用丢ACK按30秒预算重试，不宣称send等于applied。

测试覆盖多订阅共享、重复关闭、scope替换、取消宽限、节点/订阅上限、失败更新不改引用、callback锁安全；租约启用/续期/宽限/超时/停用重试/旧回执隔离。实际HTTPS SSE测试验证未授权不Acquire、两个页面计数2、各自关闭只释放自己、最后关闭产生宽限、REST不提频。真实SSE+Gateway+WSS集成验证两订阅在ready applied后触发同一个初始lease，Agent需求回执被接受且后续设置仍能通过管理连接。

修正一次测试期望计数（实际8次合法引用变化，而非测试误写7次），实现没有放宽。定向monitoring/gateway/httpapi race通过。OpenAPI从Go重新生成，docs/PROTOCOL.md注明当前gate与未完成范围。Panel完整validate通过（`/tmp/viorka-panel-demand-validate.log`）；全相关包race仍运行，随后补记。

尚未完成：v3独立fast/full/IP最新快照、组freshness、SSE typed DTO/通知；需求状态对Web可见；WSS入站必须再接lease有效性校验（现有P2测试在无需求时直接发metrics，后续需改成先建立租约）；真实Agent Client与Panel Gateway共同端到端全矩阵；前端历史旧DTO适配、模式/设置UI；P6/P7实机/长稳/开关验收。此批不等于P2/P5完成，生产v3仍未启用，最终双仓库commit/push未执行。

补充：Panel monitoring/gateway/httpapi全包race成功（`/tmp/viorka-panel-demand-race.log`，httpapi约83秒），diff检查通过。没有将以上本地测试视为Linux/NAT实机或24/72小时长稳验收。

## P2/P5 补充：WSS 租约准入与新版快照视图基础

Panel telemetrySession.receive在身份校验后、Ingress/Recorder之前检查当前WSS需求。无租约/停用返回stale_demand；从最近有效enable/renew发送起满90秒返回lease_expired，拒绝批次不绑定run或写历史。无效租约的metrics计入120/min管理消息预算，避免绕过Ingress限速。HTTP路径不受浏览器需求限制。

既有真实WSS/HTTP去重测试改为先验证无需求拒绝，再建立并确认lease，重新发送同一批次得到accepted_count=2，随后POST只计duplicate；证明拒绝未累计。补充90秒精确到期边界、停用和不存在租约测试。定向gateway/httpapi race通过，Panel完整validate通过（`/tmp/viorka-panel-lease-admission-validate.log`）。

新增monitoring/telemetry_view.go作为后续REST/SSE的类型化投影：fast/full/IP及接口/月流量/磁盘/文件系统所有uint64字段投影为十进制string，保留未知nil与真实零，float速率不变；复制指针和数组，外部修改不能污染快照。TelemetrySnapshot分开保存各组sampled_at/received_at/stale和最近metrics_received_at；fast按max(90,3C,3发送周期)，实时用W，full按max(90,3R)，IP按1800秒（600秒周期的3倍）。新HTTP receipt不能刷新旧sample freshness，management_online留给Gateway独立输出。

投影测试覆盖2^53+1/int64最大值无舍入、零/未知区分、快照ownership、各组独立TTL、缓存重复receipt和缺失组stale。最后view定向race和monitoring/gateway/httpapi vet、diff检查通过；完整validate先于新view文件，未将前一完整检查冒称覆盖后续新增投影。

这些新视图尚未接入REST/SSE；下一步需完成有界latest/live缓存、共享Ingress原子接收后的通知、SSE DTO与generation/cursor、管理在线状态和各组freshness的解耦，然后进行前端历史/设置/实时模式适配。生产gate、实机/长稳、最终双仓库commit/push仍未完成。

## P2 第三批：共享Ingress的有界v3实时缓存

新增telemetryLiveCache，由Ingress同一mutex拥有，无额外goroutine。每节点保留fast/full/IP各自最新视图、原group received_at和sequence，fast曲线最多600点/10分钟。缓存总体32MiB预算，按编码snapshot+每点1024字节保守开销计，与Recorder 32MiB预算分开；最多1024节点。这里是逻辑预算，不等于实测RSS承诺。最新快照编码存储，读取时解析为独立对象；曲线map复制，summary去除设备数组和点窗口。

Ingress在Recorder BindRun/Accept之前prepare完整候选并检查缓存预算；Recorder成功接受后commit live数据，随后在锁外调用可配置通知器。失败不会产生部分live更新或发布通知。重复跨通道批次仅更新metrics_received_at，不增加曲线点/改变组received_at。Forget同步删除协商与live缓存，回收预算；管理断线不Forget。组样本序号与本地SSE cursor分开，换run重新生成cache generation并reset，过旧cursor恢复窗口。

加入Recorder水位读取：重启后低于持久化水位的观测不能进入live；等于水位的缓存观测可以恢复latest，但不重放曲线点；更高序号只追加一次。最初重启单测误用了不实现Restore的通用fake，被测试正确发现；改为实际恢复已flush checkpoint的专用内存repo。该测试证明水位恢复语义，不冒称新增SQLite进程重启实测。

测试覆盖605次采样截断到600点、10分钟窗口、reset/delta、视图/map ownership、重复批次/组provenance、run切换、删除释放预算、summary减载、超预算在历史绑定前拒绝、通知在两份状态提交后且锁外可重入读取、重启水位防重放。定向monitoring race/vet通过。Panel完整validate通过（`/tmp/viorka-panel-live-cache-validate.log`）；最后summary减载新增后定向live/ingress race及diff检查通过。

缓存已实际接到HTTP/WSS共享Ingress，但Gateway尚未设置通知器，REST/SSE也尚未读取新Live接口。下一步直接接Gateway/HTTP DTO和SSE cursor、独立management_online及需求mode，使现有页面可取得v3数据，再做前端类型/历史/设置/模式适配。生产开关、完整跨仓端到端/实机/长稳与最终双仓库commit/push保持未完成。

## P2/P5 第四批：v3 REST/SSE 读取与控制状态发布

Panel ConfigureTelemetry现在为共享Ingress安装metrics通知；Accept和applied settings可唤醒已有SSE，通知在Ingress锁外且数据可见后发布。Gateway新增ReadMetrics，保留legacy路径并返回独立telemetry/telemetry_control；缓存解析错误向HTTP传播或结束SSE，不能假成功/静默退回旧快照。v3与legacy游标generation使用独立命名空间，协议切换不会混入旧点；list summary去掉设备数组和曲线。

节点列表/详情、metrics REST及metricEvents已读取新API，输出management_online，只有legacy仍保持offline强制stale。v3根据fast/full/IP自身时刻计算freshness，HTTP成功不修改节点在线状态。telemetry包含实际applied settings（ready尚无样本时也可显示），而非pending Desired。Gateway按唯一writer拥有的需求状态发布pending/realtime/http/degraded、viewer refs和租约余量；匹配ACK之后才显示realtime，断线清除控制状态并通知读取HTTP模式。

真实HTTPS POST→共享Ingress→Event Hub→授权SSE→REST测试通过：无管理连接时最新HTTP数据仍fresh、management_online=false；计数9007199254740993无损string；重复POST无新点且不更新group received_at；设置applied更新SSE但不造点；列表summary不含设备数组；数据库节点仍offline。真实SSE+WSS测试增加先观察pending，再发送Agent ACK并观察realtime，验证引用/发送不冒充确认。

首次全相关包测试仅因OpenAPI新类型未生成失败；已按Go注册重新make generate-openapi，未手改Schema。定向monitoring/gateway/httpapi telemetry/metrics/OpenAPI race通过（`/tmp/viorka-panel-live-api-race.log`）；最后SSE applied设置/模式测试race及相关vet通过。Panel完整validate通过（`/tmp/viorka-panel-live-api-validate.log`），diff检查通过。docs/PROTOCOL.md更新响应和freshness语义。

前端尚未消费telemetry字段；此前历史API旧stats DTO不匹配也仍需修复。下一步读取Panel Arc skill并实施P5前端类型/历史/设置/实时模式与时间状态，之后继续联合端到端失败矩阵、P6/P7实机/长稳/生产开关与最终双仓库commit/push。未发布部署，目标未完成。

## P5 第五批：前端 typed history 适配与统计语义

读取Panel真实history DTO、bucket聚合代码和现有Arc图表实现后，新增web/src/api/history-wire.ts，将bucket_start秒/last_sample_at毫秒转换到现有图表时间；校验实际消费的均值/峰值计数、nullable值、十进制计数、设备唯一性/上限、时间范围/对齐/顺序和最多169桶。getMetricHistory现在消费typed DTO，旧stats wire拒绝。内部图表投影保留kind(mean/peak/latest)，原bucket保留精确十进制计数；Number仅用于绘图比例/坐标。未知均值和缺失峰值保持缺口，真实零保留，零容量不生成使用率。

历史页typed模式取消无意义的全局平均/峰值选择，按实际字段语义显示：CPU/内存均值，网络/连接/进程采样峰值，负载/容量最新值。数值列表和Arc LineChart可访问标签同步语义。typed磁盘历史按device绘制独立读写峰值，移除主机IO合计；legacy实时路径尚未替换，仍待v3前端接入统一处理。复用现有Arc LineChart/SegmentedControl/Button/Toast，未增加依赖或重写组件。

验证：pnpm run typecheck、pnpm test（23文件179测试）、pnpm run build、改动文件Biome、git diff --check通过。测试覆盖2^53+1字符串、加权均值/峰值/latest、秒/毫秒、未知与零、设备分离/图表选择、无效计数/溢出时间/乱序/超限，以及typed图表标签。构建仍有>500kB chunk警告；没有为本次修复做无关打包改造。首次验证发现测试中可空值TS错误与旧标签断言，修正后重跑通过。

Arc代码检查已完成对应文案/语义token/现有组件/无新增动效范围；实际浏览器浅深色、390/768/1024/1440、键盘和移动端视觉验收尚未执行，不能称Arc全清单完成。其余待办不变：v3 live字段与SSE客户端、独立设置与组freshness/模式UI、真实双仓端到端失败矩阵、生产gate/旧Panel回退、P6/P7实机长稳。未commit/push，目标保持active。

## P5 第六批：v3 Web 实时快照、分组新鲜度与设备视图

新增web/src/api/telemetry.ts：按真实Go REST/SSE投影定义fast/full/public_ip/settings/control，校验decimal计数、数组上限、时间/游标、nullable/optional组和C/R/W范围；C>R合法。MonitoringSnapshot消费management_online/telemetry/control，isStale对v3使用各组自己的sampled_at和received_at，HTTP整体receipt不能刷新旧fast；与管理online无耦合。计数显示使用BigInt运算，保留2^53+1原始字符串。

SSE解析验证nested/outer sequence/reset一致；mergeMetricFrame合并telemetry.points而非legacy空points，按run/reset隔离、去重，600点/10分钟按观测时间截断；只有模式/设置变化时更新metadata而不造点。legacy继续120点。NodeInventory改用v3 CPU/内存、OS和IP，管理离线不再隐藏新鲜HTTP指标。

新增TelemetryMonitoring与telemetry-chart：NodeMonitoring在v3快照下使用独立页面区域，展示管理连接、HTTP/pending/realtime/degraded、三组采样/接收时刻及freshness、月流量精确字节与quality、设备IO吞吐/IOPS/await/util、去重文件系统和网卡。NodeMetricHistory接收类型化当前图表/采样字段映射；暂停保存当前图表与点。full最新值与fast实时曲线明确区分，不伪造full每秒曲线；磁盘设备无主机IO合计。现有本地Arc gap-aware LineChart现在保留缺失样本，避免过滤后跨未知值连线；没有任何有效series时恢复真正空图，点数按有效值计算。

验证：pnpm run typecheck、pnpm test（24文件186测试）、pnpm run build、12个改动文件Biome与git diff --check通过。新增测试验证actual-shaped DTO、600点重连窗口、按sample过期、嵌套游标一致、跨run重置、control-only不造点、C>R、2^53+1、各组TTL边界/管理离线、缺失值和图表字段映射。构建仍提示>500kB chunk。

实际浏览器使用临时纯fixture预览（无真实凭证/API/节点），复用生产组件与样式：390/768/1024/1440 iframe视口，body clientWidth与scrollWidth分别379/757/1013/1429且相等；检查浅色/深色、长挂载路径、未知/零/大整数、键盘Enter暂停。390窄屏发现Arc表格折叠后数值无名称，已通过column render补移动端标签，复查深色窄屏正常；没有改Arc内部样式。1440截图受外层浏览器宽度裁切，但内部DOM宽度/不溢出已检查。不是全量页面/实际SSE网络联合浏览器验收，也未证明所有Arc清单项（例如两accent、错误Toast和所有键盘操作）。临时预览文件已删除，Vite会话84648已停止；首次服务器默认TLS被换为独立纯fixture localhost HTTP配置，没有修改项目TLS配置或绕过证书警告。

下一步仍需独立telemetry settings编辑/Desired与Applied状态、完整实际API/SSE前端联调及语义审计（错误组/quality字段、live gap时长、陈旧数据/时钟）、完整Agent Client+Panel Gateway联合失败矩阵、生产gate/旧Panel回退、P6/P7实机/长稳。生产v3尚未启用，双仓库未commit/push，goal保持active。

## P5 第七批：独立监控设置与期望/确认/已应用状态

新增web/api/telemetry-settings.ts和TelemetrySettings表单，接入真实独立GET/PUT/status API，使用CSRF、revision乐观并发、严格整数范围（C1–60/R30–180/W1–5，C>R合法）和状态结构校验。Applied回执必须revision=applied_revision，无错误码；failed要求固定错误码。旧回执与当前Desired版本不一致显示pending，未知读取错误不冒充成功。保存只宣称期望配置已保存；最近已应用字段来自telemetry.settings独立显示。

表单支持管理员写、viewer/operator只读、AbortController取消/读请求代次隔离、保存时停用重复提交；事件在有未保存输入时只刷新确认状态并提示核对，409保留输入与原基准版本。复用现有Arc Input/Button/ToastStack，无inline操作错误。详情设置默认新版，未协商节点保留旧版折叠区域；未协商v3不宣称配置会立即生效。修复useNode和详情离线提示残留旧语义，v3不再因管理离线被额外标记stale。

上次用户暂停发生于测试刚完成，测试204项通过但session fixture role类型被推断string导致tsc失败；恢复后显式标注Session，当前typecheck通过。最终pnpm test为25文件204项通过，pnpm build通过（仍有>500kB chunk提示），改动Biome和git diff --check通过。实际go test ./internal/httpapi ./internal/storage -run TelemetrySettings -count=1通过。

浏览器使用临时纯fixture响应测试生产表单：0秒输入显示全局Toast且PUT计数0；C60/R30保存PUT计数1，Desired变2、pending、Applied仍1；保存期间按钮/输入禁用；409时45秒输入保留；persist_failed显示应用失败/目录与磁盘建议；viewer输入禁用且无保存按钮。该浏览器测试验证前端交互，不冒称实际后台/Agent联调。临时preview文件已删除，Vite会话15565停止。docs/MONITORING.md同步当前UI行为及未发布边界。

后续仍须实际跨仓Agent Client+Panel Gateway联合失败矩阵、完整HTTP/SSE/UI集成、语义审计与生产gate/旧Panel回退、P6/P7实机和长稳。代码尚未commit/push，goal active。没有部署或修改用户代理服务。

## P6 第一批：显式启动 gate 与真实跨仓 Agent 进程

核查发现原v3仅测试可启用，正常启动未接线。本批Agent Local新增telemetry_v3布尔（默认false），New读取该gate声明能力；Panel Config新增VIORKA_TELEMETRY_V3（仅true/false/空值），Run显式开启时创建同一TelemetryIngress，分别传入Gateway.ConfigureTelemetry和HTTP Dependencies。默认关闭，未修改实际运行配置/部署。配置测试覆盖默认、显式启停和无效类型/字符串。双方文档说明启动开关、重启和未完成兼容矩阵。

Panel新增opt-in TestTelemetryAgentProcess，使用实际Agent CLI子进程（/tmp/viorka-monitoring-agent由Agent源码go build生成），Panel测试进程中是真实Gateway/auth/HTTP路由和临时SQLite。显式CA验证TLS、stdin传入一次性注册token、私有临时state；未导入另一仓内部包、没有替换Agent采集器。真实平台采集与可失败的公网IP探测运行，IP结果不作为通过条件。

测试观察无人查看时HTTP接受v3；订阅后Agent ACK确认realtime并取得新点；实时期间创建node.inspect，等待真实执行和持久化succeeded，重复幂等键为同任务；释放最后需求，15秒宽限后HTTP模式恢复并取得新观测；全程run_id不变，最后SIGINT退出成功。初次普通运行46.21秒序号15<16<31；补任务后Panel测试race运行48.26秒序号15<17<33。race覆盖Panel测试进程，子Agent二进制本次未加-race；不能宣称子进程race已测。当前订阅是Gateway真实引用入口，浏览器SSE入口此前已有单独集成证据，此测试尚未合为浏览器端到端。

验证：Agent config/connection测试，Panel config/app测试及相关vet通过；Agent ./scripts/validate.sh（/tmp/viorka-agent-gate-validate.log）与Panel PATH=/tmp/viorka-monitoring-venv/bin:$PATH ./scripts/validate.sh（/tmp/viorka-panel-gate-validate.log）均成功；diff检查通过。所有本批会话均完成，无后台试点进程留存。go test未设置VIORKA_TEST_AGENT_BINARY时明确skip此跨仓测试，不能当作联合证据。

仍待：真实两端更多失败/重连/吊销/丢ACK/过载矩阵，首次旧Panel及已协商v3后回退旧Panel的完整行为，公共IP网络的可重复控制方案，UI+SSE总联调和T01–T37审计，P6/P7资源基线、Linux/NAT与24/72小时长稳。默认gate未开启，未发布、未commit/push，目标active。

## P6 第二批：协商回退、再次开启及双进程 race

2026-11-03。发现已协商v3后连接未开启v3的Panel时，旧采样器已取消且metrics ticker被runner存在条件禁止，导致旧指标无法恢复。Client现在在此类重连上等待10秒ready协商窗口，期间保留HTTP；窗口结束仍无ready时有界停止runner并检查最终账本保存，重新启动legacy采样器与按旧设置报告的ticker。收到ready后取消回退计时，设置应用失败不伪装为成功回退。身份、持久化设置和账本保留；正常v3重连维持run_id，停止后再次开启建立新run_id。CONFIGURATION同步行为。

真实TLS/WSS fixture新增TestTelemetryClientFallbackToLegacy和TestTelemetryClientReenableAfterFallback：验证回退后的样本距当前不超过5秒、runner清理、再次ready和需求确认、恢复v3新run身份，以及每次runner停止各保存一次账本。普通重连测试仍验证run与序号连续。三项一起执行go test -race ./internal/connection -run '^TestTelemetryClient(ReadyLiveAndHTTP|FallbackToLegacy|ReenableAfterFallback)$' -count=1 -v通过（23.245秒）。这些使用fixture Panel，不代表旧发布tag的完整兼容矩阵。

Agent ./scripts/validate.sh通过（/tmp/viorka-agent-fallback-validate.log），go test -race ./internal/connection通过（/tmp/viorka-agent-fallback-race.log，41.487秒）。两者在最后新增再次开启测试前执行；新增测试后定向race全部通过。

构建go build -race -o /tmp/viorka-monitoring-agent-race ./cmd/viorka-agent成功。在Panel执行VIORKA_TEST_AGENT_BINARY=/tmp/viorka-monitoring-agent-race go test -race ./internal/httpapi -run '^TestTelemetryAgentProcess$' -count=1 -v成功（51.364秒）。本次Panel测试进程和真实Agent子进程均启用race：实际TLS注册/认证、平台采集、HTTP→WSS→HTTP、node.inspect任务与幂等、run保持以及序号15<17<33通过，正常退出。不是Linux实机或浏览器全链路结果。

剩余完整失败矩阵、首次旧Panel/tag版本兼容、UI/API/SSE联合验收、逐项T01–T37证据审计、Linux/NAT资源基线及24/72小时长稳均未完成。默认gate仍关闭；无部署、生产数据库清理或代理操作，尚未commit/push两仓库，目标保持active。

## P6 第三批：管理断线的真实双端测试与 Linux 测试环境

2026-11-03。新增Panel TestTelemetryAgentProcessManagementOutage，通过fixture HTTP中间件取消当前WSS请求、临时拒绝新WSS，同时保留真实HTTPS POST入口。检查数据库offline且出现被拒绝的重连后，HTTP指标继续增长；恢复入口后原viewer需求恢复realtime，run_id不变，新的node.inspect实际完成。中间件只用于测试，无生产故障注入接口。

收紧原进程测试：关闭最后需求并经过15秒宽限、确认HTTP后，必须再次看到更高序号；此前以最早realtime序号作基线会错误接受宽限期内的WSS样本，不能单独证明恢复HTTP。本次双进程race运行两个场景通过：普通场景63.32秒序号15<17<39；断线场景93.29秒，离线HTTP序号32、恢复WSS序号37，拒绝4次WSS重连，最后HTTP序号55；总157.649秒。Panel httpapi全包race也通过（85.653秒，/tmp/viorka-panel-outage-httpapi-race.log）；该命令没有外部Agent环境变量，跨仓测试明确skip。MONITORING修正旧SSE状态陈述并记录新测试运行方式。

随后进一步收紧断线基线：先等待旧管理handler完成offline处理及至少一次拒绝重连，再取序号基线，避免在途WSS更新被当成新HTTP。此最终断言改动已vet/diff-check及Linux测试二进制编译通过，运行验证见后续记录，不能沿用之前race结果宣称已执行。

用户提供测试主机并明确允许普通用户最长72小时隔离测试；用户确认不是NAT主机。只读检查为Ubuntu24.04.2/x86_64/Linux6.8、约1613MiB内存，现有Realm PID64716等服务，未更改服务/防火墙。当前主机可覆盖Linux amd64和普通用户，不覆盖arm64/Alpine/NAT。远端专用目录/tmp/viorka-monitoring.Bmu3rVeg，已上传静态Agent与Panel httpapi测试二进制；Agent SHA256 932ff3127d8b18acd987793ff4a2227050596c6605690aaaf63acda4527b8ee0，测试二进制174075489827a26d08301897ab39a54682a4f9bc86e2c4b1965dbb3e6c67799d。无源码/现有凭证上传。

Linux短测已启动，会话80261：以nobody运行上述两个跨仓进程场景，临时SQLite、显式CA、回环TLS；独立sleep哨兵检查存活并由测试命令收尾，前后只读检查原Realm PID及启动时间。结果尚待收取；不要重启仍在运行的会话。24/72小时持久在线资源测试尚未启动，不能把反复短测代替长稳。其余验收与双仓commit/push仍未完成。

## P6 第四批：Ubuntu 普通用户实测及连续观测夹具

2026-11-03。上一批远端会话80261已完成，退出0：普通场景60.34秒序号15<17<39；断线场景120.28秒，离线HTTP18、恢复WSS25、最终HTTP42。断线最终版断言在旧WSS handler结束后取基线，避免在途消息误判。独立哨兵存活；原Realm PID64716和Thu Oct29 14:12:20 2026启动时间未变。此为Ubuntu24.04 amd64真实运行，两个静态二进制未启用Linux race。

新增Agent TestTelemetryLiveLinux显式VIORKA_TEST_LIVE_TELEMETRY=1启用，真实两次fast/full采样检查首CPU无基线、下一次有效CPU/内存/socket/网卡/流量/文件系统/磁盘/OS与完整协议验证。交叉编译整个collector包测试并以nobody执行全包通过（非Linux测试按平台skip）；实际uid65534、1网卡、2文件系统、12磁盘设备、Ubuntu。没有通过fake覆盖真实采集器。测试二进制SHA2563402722ad80671150babe5b1b1bd4741d3f53e76b0c0e72dd202b2d291388797。

Panel新增TestTelemetryAgentSoak，持续同一Agent/Panel、默认C30/R60/W2、Recorder真实运行、5分钟订阅切换、10秒procfs及DB/WAL资源记录；序号/run/模式有断言，结束验证历史。仅显式启用Linux，1m–72h时长限制、0600独占新输出文件、16MiB/26000行证据预算、8MiB子Agent日志限制。记录CPU原始tick、RSS、FD/socket FD、磁盘读写字节和Panel goroutine/heap；未声称覆盖网络字节率/Agent内部goroutine/刷盘逐次延迟/50节点或完整Panel启动流程。短测复用helper并增加日志上限，已定向race检查日志边界，procfs本地macOS明确skip，vet/diff-check通过。

Soak Linux测试二进制SHA25613a32205ca6dc15830022d31a7e72b769ab80ba4a362fd4724c3d5eab4f50f5f，远端上传后sha256sum -c验证通过。1分钟夹具smoke会话90595运行中，输出/tmp/viorka-monitoring.Bmu3rVeg/smoke.jsonl；已观察到10秒记录，仍须检查终态。24小时观测尚未启动。最新helper普通+断线双进程race会话68253（/tmp/viorka-panel-soak-helper-race.log）运行中，待收取。不能重复启动这些现有会话。

第四批终态补充：会话90595完成退出0，1m soak实测180.35秒含前置协议切换，7条资源记录/7个真实历史桶；日志上限和真实procfs检查均通过。会话68253完成，最新双进程race两个场景通过（187.889秒），普通63.53秒、断线123.32秒，断线HTTP32→恢复WSS39→最终HTTP56。以上取代本节“运行中”状态。

已启动授权的24小时观测：远端同目录viorka-run-soak-24h.sh，以nobody运行，timeout外层87000秒、Go24h8m、业务context24h5m，默认C30/R60/W2。输出soak-24h.{pid,started,log,jsonl,exit,finished}，exit/finished仅终止时产生。启动返回launcher_pid91114；需按远端pid文件和实际ps/JSONL检查持续状态，不能凭启动命令认定通过。无新主机服务/防火墙改动；运行二进制固定为上文SHA，后续源码变动不自动进入本次观测。此观测尚未完成，不满足24小时验收，更不满足NAT72小时。

## P6 第五批：实际旧版本二进制兼容矩阵

2026-11-03。扩展Panel scripts/e2e_agent_connection.py支持预构建绝对二进制路径、双方独立v3启动开关及显式预期模式；原默认构建方式保留。打印二进制SHA256，配置0600。v3额外验证Panel kill/restart后原run恢复、Agent停止/restart后新run出现；沿用真实任务、幂等、吊销和哨兵检查。不是fixture替代旧版本。

旧Panel来自无tag的提交fedad58072b291eed69dce4a19ac0620ce1451bc；旧Agent来自v0.1.0-bate.4/a64d0c95cfcf4b214a0bfc61d6986a0464d4663e。git archive到隔离/tmp目录编译，未改原工作区。新二进制由当前未提交工作区构建，各组合独立临时SQLite/TLS身份。四个实际进程组合均退出0：old-old（91564）、old-new（34139，新Agent显式v3）、new-old（93317，新Panel显式v3）、new-new（19446，双方v3并预期v3）。新新测试覆盖完整Panel可执行文件启动装配、崩溃后新表恢复和HTTP继续采样，未把进程内Panel测试当完整CLI证据。

详细提交/tag、四个二进制摘要、命令参数及范围限制已写入MONITORING_COMPATIBILITY.md；本次日志/tmp/viorka-compat.LH8x1Fok/*.log。py_compile、--help与diff-check通过，未改生产Go行为，因此未重复全套Go验证。旧Panel接替新Panel时不能直接复用新历史库；此类实际二进制降级仍未证明，已有同版本开关/fixture回退证据不能替代。

远端24h观测当前ps确认Panel91120/Agent91133均nobody存活，JSONL持续更新且fast_stale=false；启动总时刻2026-11-03T01:13:42Z，采样窗口在前置功能测试后开始。未结束，勿重启。arm64/Alpine/NAT72h、50节点资源、UI总联调及剩余逐项验收仍待完成；双仓未commit/push。

## P5 第八批：容量百分比与历史口径审计修复

2026-11-03。对照交接4.4发现新版telemetry-chart与typed history都错误使用used/total，且历史直接把legacy根容量与v3全本地合计投到同一disk系列。新增filesystemUsagePercent，以BigInt精确实现ceil(100*used/(used+available))，零分母/缺值为未知；当前卡片不再显示“未知%”。history-wire消费并验证已有DTO的disk_available，v3投到disk、legacy保留used/total投到disk_legacy，mixed桶不绘制容量百分比。历史图明确标识两种口径，旧根系列虚线，不拼接两类数值。

复用原Arc图表/布局，读取现有Arc skill/checklist；本批只改数据投影/文案，无新控件/样式/依赖。测试覆盖保留空间差异250/(250+500)→34%、2^53+1精确ceil、0/满容量/未知、legacy与mixed分离以及系列标签键。首次测试新增用例传反historyMetricCharts参数被tsc/test发现，已修正；最终pnpm typecheck、25文件206测试、build、六文件Biome及diff-check通过。构建仍提示>500kB chunk；本批未重新执行浏览器全量视觉QA，不把数据单测冒称浏览器验收。MONITORING文档同步口径。

远端24h观测实际进程91120/91133继续存活，2026-11-03T01:20:53Z记录elapsed310秒/mode=realtime/fast_stale=false，证明第一次5分钟HTTP→实时需求切换已执行，尚不证明完整24h。二进制未变，当前前端修复不影响该后台观测。其余总验收、未测平台与双仓commit/push仍未完成。

## P5 第九批：浏览器指标流迟到事件隔离

2026-11-03。审查useLiveMetrics发现metrics/onerror有source身份检查，但closed事件缺少；旧连接在隐藏恢复或scope切换后迟到closed，会污染新连接状态并显示错误Toast。现在closed同样只接受当前stream；格式错误、服务端closed和EventSource终态error都会先解除stream归属，再关闭，避免终态后的排队事件恢复假live或重复报错。可重连的临时网络错误仍保持浏览器自动重连。

新增effect生命周期测试，显式替代React hooks和EventSource/document：验证隐藏关闭、恢复新连接、旧closed/error/metrics不影响新连接、三种终态忽略后续事件、scope替换/卸载清理visibility监听、disabled/空scope/初始隐藏不发起连接。此测试调用生产effect逻辑，验证资源所有权；不声称模拟了真实React调度或浏览器/SSE联调。

最终26文件212测试通过，typecheck、两文件Biome和diff-check通过。build在补终态onerror前一次通过，终态补充后重跑测试/typecheck通过，未将前一次build描述为最终代码重构建。测试初版Biome提示测试夹具调用hook位置/void联合类型，已按夹具边界添加单行说明并修正undefined类型；没有抑制生产规则。没有新增UI控件/样式/依赖，Arc全浏览器验收仍单独待办。

长稳新发现（待处理）：2026-11-03T01:27:03Z远端采样elapsed680秒，mode=http/fast_stale=false、进程存活，但Panel日志多次出现expired unpersisted telemetry bucket。已定位Flush按node级n.dirty判断每个过期bucket是否未持久化（telemetry_recorder.go约303行）；持续新样本会让整节点dirty，已刷盘旧桶也可能被误报。尚需通过真实SQLite与并发flush测试确认有无实际丢数据，并按桶跟踪脏版本/持久化成功，不能仅去掉警告。当前观测不认定稳定性通过；不要擅自重启远端91120/91133，先保存证据并修复。下一轮优先处理该问题。

## P6 第六批：长稳发现的过期桶误报修复

2026-11-03。确认原Flush使用整节点dirty判断每个过期桶，持续新数据使早已落库的旧桶被误报。新增node.persistedVersion与bucketVersions：成功接受fast/full或legacy样本时标记受影响桶的版本；完整快照成功提交后推进已提交版本，即便期间又收到新样本。过期桶仅在自身修改版本仍未提交时告警/计数，并回收对应版本记录。节点级dirty继续用于检查点/资产和整体刷盘，身份删除重建仍用owner指针隔离。

八个定向race场景覆盖v3/legacy各自的已保存桶+新桶、失败写入、刷盘期间同桶更新和不同桶更新；真正未提交内容的过期告警保留，没有通过删除告警隐藏丢失。首版v3测试fixture缺filter_revision被协议拒绝，补齐后通过。真实SQLite新增连续24桶跨10分钟内存保留期测试，检查24桶及每桶CPU count均保留，恢复后末次重投仍被去重。monitoring/storage全包race通过（/tmp/viorka-panel-expiry-race.log），相关vet/diff检查通过；Panel完整validate通过（/tmp/viorka-panel-expiry-validate.log）。

远端旧观测仍运行时只读查询真实SQLite：37个已保存桶，被告警的早期bucket_start1793668440实际存在且CPU count=1，证实该条未保存告警错误。历史有一个60秒间隔，未填补或据此宣称全时间无缺口；需结合前置模式切换与发送窗口进一步评估。保留原soak-24h.jsonl/log作为诊断证据。

随后显式向仅测试Agent PID91133发送SIGINT，结束旧构建观测；测试父进程最终因模式不再收敛退出1，日志标FAIL，不能当作自然长稳失败或成功。ps确认91120/91133已退出，sentinel_alive=true，finished2026-11-03T01:33:13Z，总1170.35秒。用户Realm未修改。

修复后的Linux测试二进制viorka-monitoring-soak-expiry-linux-amd64.test摘要b73ebbec5531ee40b82e8e68d3c591c57582c5185eaa432f9e642f01f5bf6e55，上传后sha256sum -c通过。已用新的soak-expiry-24h文件前缀重新启动24h观测，原记录不覆盖；其结果与准确pid/时间待后续收取。Agent二进制未变。重新计时前的运行时长不累计为修复后24h证明。总体目标仍active，未commit/push。

## P6 第七批：正常 HTTP 限频抖动错误清空采样窗口

2026-11-03。继续审计60秒历史间隔时发现telemetrySchedule.advance在R定时器到期且now<retry.earliest(true)时无条件collapse；earliest包含lastPOST+R，发送较定时器晚数毫秒即可导致正常上报窗口被当作故障冷却丢弃。新增TestTelemetryHealthyPOSTJitterPreservesWindow，以默认C30/R60、首次POST晚10ms、正常成功构造下一边界。修复前测试真实失败，droppedCooldown=1；修复后保留90s/120s两条原sequence样本，120s时仍拒绝越过限频，120s+10ms正常发送，无补采/重编号。

修复仅使正常限频等待不collapse；blocked/unsupported和degraded故障退避仍按既有规则清窗口，take仍执行lastPOST+R和单在途限制。定向Schedule/Retry/抖动测试race通过，相关vet/diff通过。原远端60秒缺口与此错误机制相符，但只凭旧聚合记录不能逐样本还原那次原因，不能将推断写成已证实的单次根因。

Agent完整validate会话5024（/tmp/viorka-agent-jitter-validate.log）和全连接race会话11182（/tmp/viorka-agent-jitter-race.log）已启动，待收取。新Linux Agent已构建viorka-monitoring-agent-jitter-linux-amd64，准备独立真实进程联调。当前远端soak-expiry-24h仍是旧Agent，不计作修复后长稳；必须等新构建联调通过后保存记录、仅停止测试进程，再使用新文件前缀重新计时，不能静默替换运行二进制或累计旧时长。当前测试父/Agent为91962/91975，启动2026-11-03T01:34:54Z；尚未停止。

第七批运行补充：全连接race会话11182已退出0。Linux新Agent SHA256为4b2a0d7c87824b7a789cbb423b2d658cf0a08802e42c8df72495422e55724483，上传远端校验通过。会话24250正在以nobody运行普通+断线真实进程测试，Panel测试二进制为上批expiry修复版；请继续轮询，不重复启动。完整validate5024仍在运行。

第七批终态补充：validate会话5024完成退出0，日志末尾Viorka Agent M0 check OK。Linux联调24250完成退出0：普通60.33秒序号15<17<39；管理断线90.29秒，HTTP31→恢复WSS36→最终HTTP54，拒绝4次WSS重连，重连任务成功。实际使用新Agent jitter摘要与修复后Panel expiry摘要，非旧构建结果。

已准备新soak-jitter-24h脚本并向旧测试Agent91975发送SIGINT，旧soak-expiry-24h记录保留且不能作为24h通过结果。等待旧父进程退出后启动新的24h单节点观测。此前工作区暂存状态被外部更新；保留已有index，不reset或重写暂存内容。最近jitter修复与本记录属于未暂存增量，最终提交时需核对两边diff/cached diff。

长稳切换确认：旧soak-expiry-24h在主动停止测试Agent后退出1，finished2026-11-03T01:41:44Z，410.34秒，末尾/proc/91975/stat不存在为主动停止后的预期终止证据；sentinel_alive=true，旧父/子ps均不存在。新soak-jitter-24h于2026-11-03T01:42:21Z启动，脚本PID92535、Panel测试PID92539、Agent PID92555，均由nobody运行并已ps确认存活。输出前缀/tmp/viorka-monitoring.Bmu3rVeg/soak-jitter-24h；外层timeout PID92538。Realm64716启动时间不变。需要持续读取此新实例，不再轮询旧PID作为当前状态。用户表示稍后提供arm64/Alpine/NAT环境连接信息，尚未取得，不对其声明测试通过。

## P6 第八批：逐项验收索引与真实订阅关闭断言

2026-11-03。新增MONITORING_ACCEPTANCE_EVIDENCE.md，覆盖T01–T37定位、证据层次和缺口。此索引刻意不将独立单测组合宣称为双端故障注入，也不将文件重开宣称为跨进程恢复；真实UI、精确崩溃窗口、同时刷新和持续拒绝下任务共存等仍列待验收。

Panel TestTelemetrySSEScopeActivatesGatewayLease新增关闭第一条真实SSE后引用1且无grace、关闭最后一条后引用0且grace在未来15秒内的断言；定向race通过（2.431s），httpapi vet及diff-check通过。该测试只证明真实SSE与gateway引用状态，15秒到期由独立gateway时钟测试覆盖，不冒称两个真实浏览器标签联调。

Agent新增TestTelemetryRealtimeCadenceIgnoresCollectionInterval，C60、W1/W2分别逐秒检查180秒内fast与WSS严格按W、无POST、full严格每60秒。定向cadence race通过（1.033s），gofmt及diff-check通过。仅新增测试，不改变当前远端运行二进制行为。

只读复查远端当前Panel92539/Agent92555均nobody存活；2026-11-03T01:46:51Z采样窗口elapsed150秒、mode=http、fast_stale=false，Agent RSS12652544字节/FD9，Panel RSS30932992字节/FD19/goroutine14。这些是单次快照，不是稳定性预算结论。Realm64716仍存活。用户表示其他环境稍后提供连接信息，不重复询问，目标仍active；双仓未commit/push。

## P6 第九批：接收成功后丢失 HTTP 回执的真实进程验证

2026-11-03。前轮已新增验收索引及断言，属于实际进展。本轮在Panel既有跨仓夹具新增TestTelemetryAgentProcessLostPOSTResponse：测试中间件仅截获前两次指标POST，经过真实认证/ingress接收后，第一请求以net/http ErrAbortHandler中断连接，不向Agent发送receipt；第二请求正常返回。使用真实Agent CLI与显式测试CA，无生产故障开关，不改运行中的远端观测。

测试断言第一次批次包含多个fast，失败后下一批只有更新的一个fast且run不变、sampled_at前进；实际请求间隔不早于R（为本机请求调度允许1秒裕量），真实SQLite所有桶fast_count总和等于两批已接收点数。之后沿用实时租约切换、node.inspect及幂等、最后需求grace后HTTP恢复和有界退出。不是HTTP mock直接返回错误，也未以fixture代替Agent调度。

本机macOS命令VIORKA_TEST_AGENT_BINARY=/tmp/viorka-agent-response-loss go test -race ./internal/httpapi -run '^TestTelemetryAgentProcessLostPOSTResponse$' -count=1 -v通过，测试121.25秒/总122.286秒；首批15点，下一次30.009956333秒后只发序号30，落库count16；之后HTTP31→WSS33→HTTP55，run不变。日志/tmp/viorka-response-loss-race.log。Panel定向WSSReadyAndHTTPDedup/SSEScopeActivatesGatewayLease race通过3.823秒，httpapi vet与diff-check通过。此轮只改测试/文档，未将结果声称Linux同场景或完整故障矩阵通过。

远端只读复核92539/92555存活，2026-11-03T01:51:31Z观测elapsed430秒、mode=realtime、fast_stale=false；24h未完成。诊断字段审计确认现有Agent diagnostics主要为内部channel，尚需完成交接§12可观测性要求；该缺口未因故障测试通过而关闭。arm64/Alpine/NAT连接信息继续等待，目标active，双仓未commit/push。

## P6 第十批：Agent 独立于管理连接的有界运行诊断

2026-11-03。前轮真实回执丢失测试完成并更新验收索引，属于实际进展。本轮检查发现诊断虽有内部channel，生产仅由管理连接消费并输出任意error.Error文本；断线期间缺少持续运维状态，错误反复变化也未按时间限频。

新增telemetry_diagnostics.go，由原调度owner维护、每分钟最多一条Telemetry status，不新增后台goroutine/无界队列。记录模式、C/R/W、管理连接与需求开关/租约剩余、fast/full/IP/账本最后完成时间和错误标志、最后发送/成功回执时间、pending/inflight点数和编码样本字节、限额/冷却/失败窗口丢弃、拒绝次数和两通道等待秒数。有效采样对象内的局部Errors也标记对应组错误。移除管理reader的任意错误文本日志；日志只含固定字段及数值/时间/布尔状态，不含凭证、样本、会话标识、原始路径或错误。退出额外固定字段报告worker/final-save及整体关闭成功状态。

新增TestTelemetryOperationalLogBoundAndRedaction：600次检查60秒内只一条，后一分钟再一条；带secret错误/样本/会话标识不进入日志，检查周期/租约/冷却/窗口/丢弃数值及恢复后错误标记清除。与Runner定向race通过1.038秒；完整connection race通过53.696秒（/tmp/viorka-diagnostics-race.log）；完整validate通过（/tmp/viorka-diagnostics-validate.log），vet/gofmt/diff-check通过。CONFIGURATION明确零时间、完成并非成功、入口ACK并非落库、编码字节并非总内存、失败窗口放弃并非服务端必然丢点等口径。

此为生产代码变更；当前远端soak-jitter-24h仍使用此前固定摘要，不覆盖本轮诊断。暂不打断既有观测；待Panel诊断补齐并完成联调后再安排最终构建验证，不能静默累计旧构建时长。Panel需求引用数/last accept/flush/初始化与轮换诊断仍需补齐，整个§12未完成；真实浏览器与其余实机门槛继续待办。双仓未commit/push，goal保持active。

## P6 第十一批：Panel 接收/持久化/轮换有界诊断

2026-11-03。上一轮Agent诊断代码与验证完成，属于实际进展。本轮新增Panel telemetry_diagnostics.go，Recorder原mutex保护聚合诊断状态，每分钟最多输出一条结构化Telemetry history status，释放接收锁后才调用日志sink。记录初始化、最后新观测接收、刷盘尝试/成功、失败次数/retryAt、维护循环错误、轮换尝试/成功/错误、节点/dirty节点/保守记账字节、累计未持久化过期桶。既有鉴权快照telemetry_control已包含引用数/模式/租约，无需重复新增调试API。

保留实际Save失败与dirty/backoff语义；逐桶过期警告改为新过期计数触发的限频汇总WARN，累计数始终保留。维护循环错误（含在调用存储前context截止）也标记，不把跳过重试当成功写入。日志不含原始SQL错误、节点载荷或查询。MONITORING解释last_flush_success不等于所有节点已同步、INFO不等于找回过去过期数据、字节记账不等于RSS等边界。

新增TestTelemetryHistoryDiagnosticsFailureRecoveryAndBounds覆盖写失败、退避不提交、限频、敏感文本隔离、成功恢复和1000个过期桶仅汇总告警；InitializationDiagnostics覆盖初始化失败无实例、已存在分区时clock rollback可启动但WARN/无rotation success、正常初始化成功。monitoring/storage全包race通过1.484s/22.506s（/tmp/viorka-panel-diagnostics-race.log）；最终新增初始化测试定向race通过1.026s。Panel完整validate通过（/tmp/viorka-panel-diagnostics-validate.log）、vet/gofmt/diff-check通过。最终测试新增不改变生产行为。

远端只读复核Panel92539/Agent92555与Realm64716仍存活；2026-11-03T01:59:51Z elapsed930秒、mode=realtime、fast_stale=false。发现日志2026/11/03 09:52:23出现legacy monitoring history rejected，尚待确认触发路径及影响，不能据当前数据正常而忽略。远端仍为expiry/jitter旧固定构建，不含双端新增诊断；保持观测不擅自替换。下一步优先查该告警，并验证最终双端构建日志/联调，再处理浏览器与资源等剩余验收。goal active，双仓未commit/push。

## P6 第十二批：v3 周期资产被错误送入旧监控历史

2026-11-03。上轮Panel诊断和测试完成，属于实际进展。本轮定位远端legacy monitoring history rejected来源：Agent独立资产定时器继续发送已协商node.inventory；Gateway正常SaveInventory后无条件ObserveLegacyInventory，而Recorder已有v3 activeRun会返回ErrTelemetryRun。保留独立资产更新是正确行为，错误在额外进入旧历史适配；未发现该路径覆写v3指标，原拒绝保护了其身份。

Gateway现在只对未协商v3的连接执行旧资产→监控历史适配，所有连接继续按原契约SaveInventory。真实TLS/WSS TestTelemetryWSSReadyAndHTTPDedup增加inventory能力并在v3激活后发周期资产，再通过同socket下一指标回执作为处理屏障；断言无旧历史警告、资产确实落库，原去重/统计断言继续执行。负向回测恢复旧条件时明确失败v3 inventory incorrectly entered legacy history（0.151s）；修复条件已恢复。

初版日志捕获包装原default slog handler引起日志递归，负向回测及误重叠启动的全包race被主动终止，不计作结果；改用独立JSON discard handler后完成上述真实负向复现。当前测试没有Parallel，恢复global logger。最初修复版定向inventory/v3 race通过3.407s；最终gateway/httpapi全包race已退出0，日志/tmp/viorka-inventory-gateway-httpapi-race.log。完整Panel validate通过/tmp/viorka-inventory-validate.log，vet/gofmt/diff-check通过。

新Linux静态构建已上传隔离目录并sha256sum -c验证：Agent viorka-monitoring-agent-diagnostics-linux-amd64 SHA256 6b84e239336ee19d3d45d7632bbcfacc93a5695c46128835d39d4c83ea1430f5；Panel测试viorka-monitoring-inventory-linux-amd64.test SHA256 3d4761873380b51bcae03354224ad82743ed40d524f3d3afa3536381bee68987。已以nobody启动真实AgentLostPOSTResponse及WSSReadyAndHTTPDedup短测，会话85581，日志/tmp/viorka-linux-inventory-response.log，8分钟deadline，需收取终态，不重复启动。旧soak-jitter-24h保持运行，没有替换其二进制或将旧观测当最终构建证明。双仓尚未commit/push，goal active。

## P6 第十三批：Linux 最终修复短测与账本跨进程恢复

2026-11-03。上一轮修复v3资产误入旧历史并启动Linux联调，属于实际进展。本轮会话85581已结束：真实AgentLostPOSTResponse通过120.35秒，首15点/下一次29.998869426秒后序号30/总count16，随后HTTP31→WSS32→HTTP54与任务/关闭通过。第二个WSS fixture测试因从/root启动而读不到../../api示例失败，非业务断言失败；补齐临时目录下静态公开协议示例、在internal/httpapi工作目录以nobody单独重跑，通过0.26秒。未将混合命令退出1改称整体通过。

新增Agent TestTrafficLedgerAcrossProcesses及显式helper：三个独立子进程seed/recover/duplicate，在真实traffic.dat显式Save后os.Exit，不靠同进程对象或defer恢复；基线1000/2000→1100/2200，第二进程1150/2300累计150/300，第三进程相同计数不再增加，父进程读取最终文件验证累计/基线和0600。计数与UTC时间为fixture，不冒称真实网卡重启。macOS定向race通过4.177秒，全state race也退出0（/tmp/viorka-traffic-process-race.log），vet/gofmt/diff-check通过。交叉编译state测试SHA256 df59dbbd10b9df6f86eca8433e0099ec51a4546cd3b89aba2eeb83814c805d91，上传sha256sum -c后nobody Linux跨进程测试通过0.03秒。

以最新已验证双端生产构建开始重新计时：先确认旧92539/92555实际仍在运行，仅向旧测试Agent92555发SIGINT；父实例因模式不能收敛退出1，1690.35秒、finished2026-11-03T02:10:31Z、sentinel_alive=true。旧soak-jitter-24h日志保存，不计作自然失败或24h通过。新soak-inventory-24h于2026-11-03T02:10:45Z启动，脚本93751、timeout93754、Panel测试93755、Agent93769，以nobody运行，已实际ps确认。使用第十二批Agent6b84e239…/Panel3d476187…摘要，不覆盖旧二进制或旧记录。

新Agent日志实际出现Telemetry status，默认C30/R60/W2；新Panel日志出现Telemetry history status及初始化/轮换成功字段，确认不是仅内部单测。当前刚开始，尚不能评价持续资源或24h稳定性。Realm64716及Thu Oct29 14:12:20 2026启动时间不变。接下来读取新前缀/PID，不再把旧实例当当前观测；arm64/Alpine/NAT、50节点、真实浏览器及其他矩阵缺口仍待完成，双仓未commit/push，goal active。

## P6 第十四批：50 节点 HTTP 突发实测暴露拒绝

2026-11-03。上轮Linux真实进程/账本测试与新24h观测启动属于实际进展。本轮新增Panel TestTelemetryFiftyNodesHTTP显式opt-in性能夹具：50独立身份、真实TLS管理WSS协商/心跳、默认C30/R60的三轮真实时间HTTP、临时SQLite、5秒Flush/10秒资源记录；HTTP客户端底层net.Conn统计真实TLS字节与TCP连接创建数。记录同进程负载生成器+Panel资源，不能冒称50个真实Agent或专用Panel RSS。

首次Linux运行首轮收到503而失败（1.36s）。为避免只保留首错误，补齐整轮接受/失败计数后重编译上传，SHA256 0fe8f92449b8cdc738cafa610bb54c3dd417bf4447baeeaedf5724616d9d95cb，经sha256sum -c验证。计数版同样首轮失败：26接受/24失败，首错误503，1.46s；首轮快照RSS40370176、goroutine258、FD214/socketFD203、客户端连接创建100、TLS读取318910/写出291819字节。未完成后续轮次、落库计数和复用检查，不能将其报告为50节点通过。生产HTTP上传及共享解析有界并发保护是定位方向，未为测试调大限额或补发失败窗口。

证据本地/tmp/viorka-load-http-50-counts.log、远端load-http-50-counts.jsonl，原失败load-http-50.jsonl保留。新增MONITORING_RESOURCE_REPORT.md准确列出范围、失败和缺口。未启用环境变量的本地test明确skip；编译/vet/gofmt/diff-check通过，Linux实际性能测试失败如上。当前无此负载测试仍运行的session，下一步应分析稳态/同步突发与有界恢复，不重复把failed视为通过。

并行新soak仍在继续，2026-11-03T02:18:46Z观测elapsed360秒、mode=realtime、fast_stale=false；尚未24h。该主机同时跑独立soak，不是独占性能环境。无生产服务/防火墙改动。目标active，双仓未commit/push。

## P6 第十五批：有界上传等待消除50节点短突发拒绝

2026-11-03。上轮新增性能夹具并得到26/50接收的真实失败证据，属于实际进展。本轮保留8个HTTP body处理槽位，新增56个鉴权后读体前等待位置、最长250ms；满队列/超时仍503/Retry-After，取消释放，不新增goroutine，不增加同时分配1MiB body的数量。正式入库前既有二次鉴权仍保留；共享HTTP/WSS解析并发与历史预算未改。对应MONITORING补充边界说明。

TestMetricUploadBoundedWaiting验证队列满、释放后继续、250ms超时、取消及恢复；OverloadDoesNotReadBody通过真实认证对象断言64个位置占满时503且body.Read零次。定向race通过2.435s，TLS admission定向亦通过；完整httpapi race88.469s（/tmp/viorka-upload-queue-race.log），完整Panel validate（/tmp/viorka-upload-queue-validate.log）、vet/gofmt/diff-check通过。

Linux编译SHA256 98f950f60d6c589fc783493971b5ed21fc3b11da222dcd63afbce054480fce58，上传校验后nobody运行同一50节点同步突发夹具，不错峰、不补发原失败数据。最终退出0，TestTelemetryFiftyNodesHTTP121.68秒；三轮150POST全接受，每节点6fast/3full正确落库、management online。客户端TCP总创建100，后续两轮无新增；p95 0.191542548s/max0.205001681s，24次Flush调用max0.321403161s。13快照采样maxRSS47132672/goroutine278/FD214，负载期TLS read170550/write377079字节；Panel+合成客户端同进程且同主机另有独立soak，不能宣称专用硬件或50个真实采集器资源。细节写入MONITORING_RESOURCE_REPORT，原失败证据保留。

当前单节点soak-inventory-24h保持运行，其Panel构建不含本次上传队列变更；不能默认为最终构建长稳。暂不为每一批短测重启长测，待剩余实时性能与故障验证后安排最终版本观测。50节点W1、浏览器、其他平台/NAT72h及余下矩阵继续待完成，goal active，双仓未commit/push。

## P6 第十六批：50节点W1实时吞吐与租约续期短测

2026-11-03。上轮HTTP有界等待修复与同场景150请求成功属于实际进展。本轮将性能夹具共享到HTTP/WSS两种显式模式，WSS设置W1、121轮真实每秒同步50帧，每节点每60秒full，真实demand初始/续租ACK与心跳走同一writer循环；不启用HTTP心跳发送goroutine，不制造并行writer。若不能赶在下一W截止前结束本轮则失败，不用catch-up掩盖。最终核对每节点SQLite121fast/3full及管理online。

编译与vet/diff通过，本地未启用环境变量明确skip，不冒称已运行负载。Linux测试SHA256 fb8d8d3f487ef7ba6383fe94c49fb14e0cb67e6eea5dc33d74de64664d06d958，经远端校验后nobody TestTelemetryFiftyNodesRealtime通过121.82s；6050帧全接受，120.474s窗口，p95回执0.052221195s/max0.120610642s，50TCP连接稳定，原共享解析并发8未改。24次Flush调用max0.308467104s；13快照maxRSS46993408/goroutine124/FD114。实际JSONL已取回/tmp/viorka-load-wss-50.jsonl，资源报告记录全部口径，明确不含浏览器SSE或50个真实Agent采集器。

共享测试代码也影响HTTP分支，因此在WSS终止后单独启动同构建HTTP回归，避免两组负载互相争抢；当前会话65058，日志/tmp/viorka-load-http-50-shared.log，远端新输出load-http-50-shared.jsonl。需收取终态，不重复启动。先前HTTP成功报告仍属于上批夹具版本。当前无WSS负载进程继续运行。

原soak-inventory-24h Panel93755/Agent93769已ps确认存活约20分钟，仍是不含上传排队修改的构建；不将新短测并入其24h证明。实时性能短测只关闭对应吞吐检查点，故障压力/真实浏览器/其他平台与最终长稳仍未完成，双仓未commit/push，goal active。

## P6 第十七批：真实浏览器、Linux Agent 联调与长测失败

2026-11-03。共享HTTP负载回归终态已收取：TestTelemetryFiftyNodesHTTP通过121.79秒，日志/tmp/viorka-load-http-50-shared.log。没有继续运行的50节点负载。

新增Panel scripts/monitoring_browser_fixture.py提供真实构建、私有临时SQLite/凭证、回环TLS反向代理、最长120分钟生命周期。当前实例https://127.0.0.1:53792，工作目录/var/folders/5w/k87gsn6n5tg819mcdv8lb7b80000gn/T/viorka-browser-3mrixavo。Ubuntu普通用户Agent独立运行在/tmp/viorka-browser.s9M4bC，通过SSH回环反向转发访问Panel且验证专用CA；与soak和生产Realm分离。首页仍为明确标记的旧演示页面，不计监控证据。

浏览器真实节点列表在线并显示Ubuntu amd64；详情指标进入WSS realtime，原始时间序列先30秒后2秒，配置热更新后1秒；1小时历史实际返回30秒桶并保留采集前缺口。磁盘设备、文件系统、网卡与月流量显示真实值；公网探测失败显示部分不可用。设置C=0通过Toast拒绝；有效C60/R30/W1保存后期望及已应用均版本2。只读检查真实任务a4f56d3802fb4ba162bc510cd769e6bd34fe653745f60c3cfd681c0584110060显示已完成、执行1次。

最初31节点未跨50条分页；夹具修正为60个待注册节点，当前环境追加30个无指标节点，浏览器确认首屏50→加载更多后61。追加节点时独立API管理员登录使原浏览器会话失效并跳回登录页，重新登录后真实监控恢复；这是观察到的会话替换恢复，不能代替全部权限/吊销矩阵。390px指标及设置、768px设置、1024px指标、1440px浅色指标已查看；390px文档scrollWidth379≤innerWidth390。未完成各尺寸所有状态、完整键盘/对比度验证。截图/tmp/viorka-monitoring-browser-evidence/{settings-390-dark,metrics-1024-dark,metrics-1440-light}.jpg。

长测只读检查发现soak-inventory-24h已于2026-11-03T02:39:56Z退出1，1750.34秒，失败soak mode did not settle realtime，最后JSONL elapsed1620秒/sequence416/fresh realtime；sentinel_alive=true，Realm64716仍在。不是24h通过。源码发现每次续租发送会短暂置pending，而夹具在阶段开始45秒后对任一非realtime瞬时快照立即失败，可能是续租ACK竞争；原失败没有输出control且临时Agent日志已清理，尚不能断言原因。现仅增强测试诊断：失败打印control/序号/阶段时长，soak Agent有界8MiB日志独立保存到输出路径.agent.log（0600/exclusive）；尚未放宽断言或重启长测。

后续优先重现并区分续租瞬态与真实降级，再完成其余浏览器/故障门槛和最终长测。arm64/Alpine/NAT等待已承诺连接信息，不重复询问。双仓未commit/push，goal active。

## P6 第十八批：续租过渡断言与当前构建重新观测

2026-11-03。上一轮真实浏览器证据和长测失败诊断属于进展。复核Gateway pushDemand每次续租置pending、demandResult后恢复realtime，既有TestTelemetryDemandRenewalGraceAndReceipt明确验证该状态；交接§6也要求ACK前不得假称已确认。旧soak在切换45秒后任何pending瞬态直接失败，确实不符合该状态语义，但旧失败未保存control，无法追认其具体原因。

只修改测试夹具：阶段切换45秒宽限不变，稳定实时期间pending从首次观测起最多15秒（采样10秒，不能声称精确测到发送时刻），一旦realtime复位；http/degraded/缺控制状态仍失败，禁用未收敛不享受续租宽限。新增TestSoakModeCheckRenewalBound验证合法过渡、16秒拒绝、ACK清理以及错误模式拒绝。macOS httpapi/gateway定向race通过1.048/1.029秒，vet和diff-check通过；Linux nobody同一断言测试通过。生产逻辑未为长测放宽。

新Panel测试构建包含当前HTTP上传等待修复，SHA256 3a06afb207bf6aa8618dcd4a8851f677922f0ea1699ecb8cad9ad7edd69aa6ef；远端sha256sum -c成功，Agent仍为6b84e239…诊断构建。soak-renewal-24h于2026-11-03T02:55:49Z启动，脚本95964/timeout95967/Panel95968/Agent95981，nobody，已ps确认存活；独立Agent日志soak-renewal-24h.jsonl.agent.log实际0600，限制8MiB且不随TempDir清理。输出/退出/结束时间使用新前缀，不复用旧失败数据。尚未形成24h证据。

浏览器独立Agent95519仍存活，Realm64716仍存活；没有修改生产服务。下一步继续浏览器后台/多标签与余下故障矩阵，同时检查新长测的续租记录及终态。其他实机连接信息仍待提供。双仓未commit/push，goal active。

## P6 第十九批：真实连续拒绝与任务共存、迁移失败恢复

2026-11-03。上一轮有界续租断言与新长测启动属于进展。当前soak-renewal-24h Panel95968/Agent95981已实际ps确认存活；2026-11-03T03:00:19Z JSONL elapsed150秒、HTTP、fast_stale=false、sequence17，未接近24h。首查文件尚未生成是前置切换阶段，进程存活，未因此重启。

新增Panel TestTelemetryAgentProcessRefusalsAndTasks复用真实Agent CLI/TLS/SQLite/管理链路：首批指标成功后，在隔离HTTP中间件拒绝全部指标POST为503/Retry-After30；连续三次，每次要求真实node.inspect成功且接收序号不增长。拒绝间隔不得短于29秒，第三次后恢复入口仍须等待至少119秒；收到新指标时要求恰好三次拒绝、run不变。随后继续原真实WSS切换、任务、宽限退出与Agent关闭。拒绝由测试中间件在路由前注入，非数据库过载；没有验证全部七级阶梯或WSS拒绝，不将macOS采集结果称Linux资源证据。

当前本地构建/tmp/viorka-agent-refusal-current的真实进程race通过272.325秒（场景271.28秒）；三任务均成功，第三次后120.05906225秒恢复，序号15→16，后续HTTP16→WSS17→HTTP39；日志/tmp/viorka-refusals-tasks-race.log。Panel完整validate通过/tmp/viorka-refusals-validate.log，vet/diff-check通过。

新增storage TestTelemetryMigrationReceiptFailureRollsBackDDL：真实SQLite新库回退监控两项迁移，通过schema_migrations触发器在第9项全部DDL执行后拒绝写迁移记录。Open必须失败、sqlite_master全部对象与迁移前一致、记录数仍8；移除测试触发器后Open成功，全部表/索引SQL与原完整库一致，记录数10。最终定向race通过1.730秒；是事务故障恢复，不冒称kill/断电。

MONITORING文档已纠正开头旧版描述混用：标明旧采集/设置边界、新版HTTP/WSS、600点窗口、类型化周表与独立刷盘、各组时间口径，删除历史前端尚未改造及旧分钟混读的过时陈述。

Linux同一拒绝场景已启动，会话94831，日志/tmp/viorka-refusals-tasks-linux.log，最多9分钟；Panel测试SHA256 ce0bbca2b5e398f5657d2fcceca7763b81a23a7bf52f5d384e80b7aa0b4ac8c5，上传后校验且nobody执行。需收取终态，不重复启动。长测使用原固定二进制，未替换。双仓未commit/push，goal active，剩余实机/故障/浏览器验收继续。

## P6 第二十批：30点真实批次归桶及Ubuntu拒绝复验

2026-11-03。上一轮真实拒绝联调、迁移故障测试和文档修正属于进展。Ubuntu会话94831已退出0：TestTelemetryAgentProcessRefusalsAndTasks通过270.31秒；三拒绝期间三任务成功，第三次后120.055080801秒恢复，HTTP16→WSS17→HTTP39，run稳定；日志/tmp/viorka-refusals-tasks-linux.log。macOS与Ubuntu两环境均已有该完整场景证据，不重复启动。

新增TestTelemetryAgentProcessThirtyPointBatch使用真实CLI C2/R60，捕获真实TLS鉴权入口接受的首批，验证30fast原始身份递增、相邻采样1.5–2.5秒，并按各sampled_at计算UTC30秒桶期望值；Recorder Flush后真实SQLite每桶fast_count严格相符且总30。允许自然UTC边界覆盖2或3桶，不为凑2桶移动时间戳。随后继续真实WSS/任务/宽限后HTTP与退出。本地真实Agent race通过122.341秒（场景121.30秒），实际3桶，序号HTTP30→WSS32→HTTP69，日志/tmp/viorka-thirty-points-race.log；属于macOS传输/存储证据，非Linux采集资源。首次编译误把int64桶起点当time.Time，已改正后执行上述测试。

T20逐断言审计确认：301条加入淘汰最旧且计数，pending/inflight所有权隔离，1MiB字节预算触发提前丢弃，300fast+full分帧无缺失，64KiB恰好接受/+1字节拒绝，重连不得继续旧窗口；诊断测试验证pending/inflight编码字节与丢弃计数日志有界。定向connection race通过2.458秒。构造编码边界不冒称真实采集压力。证据索引和Panel复验说明已更新，vet/diff-check通过。

soak-renewal-24h保持原实例，Panel95968/Agent95981已ps复核存活约692秒；2026-11-03T03:07:19Z JSONL elapsed570秒、realtime/fresh、sequence158，仍未24h。生产代码未改，不替换长测构建。剩余WSS丢回执/身份刷新等组合、浏览器后台多标签、其他平台与长稳继续待办；双仓未commit/push，goal active。

## P6 第二十一批：真实WSS成功回执丢失与HTTP兜底

2026-11-03。上一轮真实批次归桶与Ubuntu拒绝测试完成，属于进展。本轮新增_test.go专用TLS WebSocket转发器，前后均真实连接到现有Gateway；只丢一条已接受的node.metrics.receipt，通过MessageID/BatchID关联校验，其他管理/设置/需求/任务帧照常转发。输入和输出均设1MiB读上限，有context及双向关闭等待，不加入生产故障开关。

新增TestTelemetryAgentProcessLostWSSReceipt使用真实Agent CLI，在已进入realtime后武装一次丢回执，捕获基线POST和超时后的实际POST。断言兜底仅一条fast、时间/序号比被丢回执的窗口更新、run不变、POST间隔至少29秒（R30并容纳入口调度差），随后观察更高原始序号的WSS恢复，再走最后viewer宽限HTTP及关闭。macOS race通过92.300秒（场景91.26秒）：丢WSS序号18回执、HTTP最新序号38、两POST间30.000663708秒；之后HTTP15→WSS17→HTTP42正常结束，日志/tmp/viorka-lost-wss-receipt-race.log。首次编译发现DTO sequence是十进制字符串，已改用ParseUint后运行。测试证明真实入口接收/恢复，不把成功回执称为SQLite耐久提交。

Panel完整validate通过/tmp/viorka-lost-wss-validate.log，vet/diff-check通过。另审计T13跨UTC月份测试：跨一个月增量计新月并标boundary_estimated，跨三个月不计旧增量且period_gap，月起点校验；定向race1.016秒。

Linux测试构建SHA256 8a8eaf731eb1f9ef1d439b06bd8ab5a853081eb756cd935661c20c9a1c7b4c41，上传校验后nobody复验会话2387，日志/tmp/viorka-lost-wss-linux.log；已观测丢18、HTTP38、间隔29.993162967秒与实时恢复，随后已收取退出0，场景通过90.31秒，HTTP15→WSS17→HTTP42及有界关闭正常。当前无该短测继续运行。长测Panel95968/Agent95981存活约1227秒，2026-11-03T03:16:09Z JSONL elapsed1100秒/realtime/fresh/sequence289；Realm64716仍在，不累计为24h通过。

上述证据已同步验收索引。其余身份刷新/崩溃持久化组合、浏览器后台多标签、目标平台与长稳仍需完成；双仓未commit/push，goal保持active。

## P6 第二十二批：独立进程接收/提交边界强制终止

2026-11-03。上一轮WSS丢回执双环境验证完成，属于进展。新增Panel storage/telemetry_crash_test.go：每个子场景使用私有真实SQLite，父进程先提交序号1并关闭Store；独立测试可执行子进程重开、恢复Recorder、接收序号2并确认AcceptedCount=1。分别在接受后但未Flush、Flush成功后通过stdout握手，随后阻塞stdin；父进程观察明确边界后Process.Kill并等待非正常退出，没有正常Close或defer刷盘。子进程只访问该测试数据库，20秒总deadline，不触及服务器或生产进程。

重开数据库，未提交场景只保留fast_count/CPU count1与sum20；已提交场景保留2与sum40。恢复Recorder后重投对应已提交样本，AcceptedCount0/DuplicateCount1，验证统计和检查点一致。这里明确展示已接收但未刷盘数据可以在崩溃后丢失，不假称ACK耐久；不冒称物理断电或HTTP传输同时kill。

macOS定向race通过2.547秒，完整storage race通过24.697秒（/tmp/viorka-crash-storage-race.log），vet/diff-check通过。Linux静态测试构建SHA256 1d3c04c8431e82e2a58cc506816d98824ad7758db761543c48f2d1d8122602aa，上传校验后Ubuntu nobody实际两个kill子场景通过0.23秒；没有尚未收取的该测试会话。验收T28和Panel运行文档已更新。

原soak-renewal-24h继续运行，Panel95968/Agent95981实际存活1459秒；2026-11-03T03:19:59Z JSONL elapsed1330秒、HTTP/fresh/sequence348，不是24h通过。生产代码未修改，不替换其构建。其他平台、浏览器后台多标签、身份刷新及剩余验收继续待办；双仓未commit/push，goal active。

## P6 第二十三批：HTTP时间窗口联合验证与长测第二次失败

2026-11-03。上一轮真实进程kill边界双环境验证属于进展。新增Panel TestTelemetryHTTPTimestampAdmissionAndAtomicity：真实TLS、身份、共享入口与SQLite，R180时-480秒fast接受/-510拒绝，未来+290接受/+310拒绝；第二条fast非法或IP过期时整批无fast持久化且不建立live run；-900秒IP缓存可接受且sampled_at保持原值。每例独立数据库/身份，用有效公开协议fixture及合成IP（不发请求）。初版minimal fast及IP遗漏必需字段/不可用标记导致语义拒绝，已纠正，不计为时间验证；最终7例race通过9.267秒。双方TestTelemetryContextAdmission固定时钟精确-495/+300及超1ns复验通过Agent1.020秒/Panel1.022秒。vet/diff-check通过，T25和Panel文档已更新。

长测再检查发现Panel95968/Agent95981均已终止。soak-renewal-24h于2026-11-03T03:24:59Z退出1，1750.37秒，sentinel_alive=true；本次明确失败control={Mode:degraded References:1 LeaseRemainingSeconds:82}，sequence417仍增长，last_advance_ago0，phase_age2m10s。最后JSONL elapsed1620秒仍realtime/fresh。持久化Agent日志在03:24:50Z报连接断开重试，03:24:52Z重新连接。不是pending误判，旧失败不能改称通过。

源码事实：会话连接到expires_at−30秒结束，凭证最长约15分钟；第二次更新约29分钟与两次固定失败时点相符。telemetryRetry.sessionChanged明确保留限频并置degraded，新租约ACK可以诚实报告degraded，成功HTTP探测后再恢复；当前soak只容许续租pending短暂态，仍会对任何degraded即时失败。这提示要验证正常会话刷新后的有界恢复，尚未证明它已恢复或持续失败。本轮不再直接放宽并重新启动24h；下一步应建立可复现短会话刷新场景，确认恢复边界再修正适当层次。新日志已保留，不丢弃失败证据。

当前没有活跃24h实例。其他平台连接信息待提供，浏览器与其他验收继续待办。双仓未commit/push，goal active。

## P6 第二十四批：短会话主动刷新恢复证据

2026-11-03。上一轮时间窗口联调并定位第二次长测失败属于进展。本轮新增TestTelemetryAgentProcessScheduledSessionRefresh：只在测试中间件缩短首个真实签名会话响应给Agent的expires_at；服务端凭证本身仍用真实TTL，认证/挑战/新凭证签发/旧凭证替换均真实执行。通过真实CLI主动到期重连触发刷新，检查恰好两次成功签发（初始+替换）、HTTP latest-only探测保持run并遵守R、原viewer恢复更高原始序号实时采样，恢复时长≤R+45秒，恢复后只读任务成功及最终HTTP退出。不是服务端同时过期的全部故障矩阵。

首次R30/W1版本已观察正常恢复，但测试错误地要求每个POST AcceptedCount>0；实际刷新探测回执accepted0/duplicate3，是已通过WSS接收的最新缓存组去重，符合无新fast时保留原身份的语义。该运行退出1，保留/tmp/viorka-scheduled-refresh-race.log，不改称通过。改为短会话场景允许合法全重复accepted回执，并校验完整总数；其他需要新样本的测试仍要求AcceptedCount>0。修复时一次编译发现Validate参数需uint64，已转换。

纠正版R30完整race通过122.295秒（场景121.25秒），恰好两会话，HTTP探测序号46、距前次POST29.999351209秒，替换凭证后31.076646秒恢复原viewer实时，后续任务成功/HTTP15→WSS17→HTTP72及关闭。日志/tmp/viorka-scheduled-refresh-corrected-race.log。vet/diff-check通过。此证据说明正常主动刷新可有短暂degraded，不能由任一瞬态判为长期失效；尚未据此改soak断言。

当前源代码进一步采用默认C30/R60/W2，并将首响应有效期140秒（Agent约110秒主动更新），错开上报边界，避免只验证恰好R到期的情形。该默认周期真实race已启动会话79179，日志/tmp/viorka-scheduled-refresh-default-race.log，最多9分钟；必须收取终态，不重复启动。R30纠正版会话69781已经退出0。

长测未重启；需默认周期与Linux短测证实边界后，为已观察到的会话更新单独提供有界恢复窗口，而非无限容许degraded。生产逻辑未修改。剩余其他平台/浏览器与验收继续待办；双仓未commit/push，goal active。


## P6 第二十五批：默认周期刷新双环境通过与重新长测

2026-11-03。收取默认C30/R60/W2真实刷新race退出0：场景241.27秒/package242.310秒，恰好两次会话签发，HTTP探测序号28、间隔1m0.002474s，替换后31.078202秒恢复原viewer实时，任务成功，最终HTTP2→WSS4→HTTP50和关闭通过。日志/tmp/viorka-scheduled-refresh-default-race.log。Ubuntu nobody复验退出0、240.36秒，间隔1m0.003687973s，恢复31.034757996秒，序号与任务/退出检查均通过；日志/tmp/viorka-scheduled-refresh-linux.log。测试仅缩短首响应有效期，非服务端双通道同时过期验证。

soak夹具现在记录真实成功鉴权签发的session epoch；初次签发不授予宽限，后续必须恰好加1，观察到更新后给予R+45秒恢复窗口，窗口内再次签发不能延长。10秒轮询使窗口起点最多晚10秒。JSONL新增session_epoch和session_recovery_remaining_seconds；原3分钟无新序号、run、资源限制和普通续租pending上限保留。TestSoakModeCheckAuthenticatedSessionRecovery验证初始拒绝降级、104秒允许/105秒拒绝、重复刷新不得延长、后续正常刷新和代次回退拒绝；两项mode测试本地race1.047秒，Ubuntu通过。此改动仅测试，不改变生产恢复策略。

Panel完整validate首次因系统Python缺jsonschema中止，使用已有/tmp/viorka-monitoring-venv重跑通过，日志/tmp/viorka-session-refresh-validate-venv.log。文档同步恢复窗口和实际8分钟夹具deadline。

新24h实例soak-session-24h于2026-11-03T03:47:54Z启动，脚本97501/Panel97505/Agent97518，实际ps确认全部nobody。Panel测试二进制viorka-monitoring-refresh-linux-amd64.test SHA256 b0486a9b9ca934b52faf96b64c0e91e182fca6d36d0791460444da2bec010e37，上传后sha256sum -c通过；Agent保持6b84e239336ee19d3d45d7632bbcfacc93a5695c46128835d39d4c83ea1430f5。独立新前缀位于/tmp/viorka-monitoring.Bmu3rVeg，旧失败记录保留不累计时长；当前仅启动，不是24h通过。生产Realm64716启动时间仍Thu Oct29 14:12:20 2026。其他平台用户稍后提供连接信息，不重复询问。

后续需收取长测终态、继续T24/T37及浏览器等未闭合验收，再做最终构建兼容矩阵和双仓交付。双仓未commit/push，goal active。


## P6 第二十六批：WSS兜底真实落库计数及双周查询边界

2026-11-03。上一轮默认刷新双环境通过并重启长测属于进展。本轮扩展_test.go的真实WSS回执代理：对每条accepted回执检查批次/关联ID及总数，记录对应fast原始sequence/sample时间；HTTP同样记录所有真实接受批次，最初两次仍用于失败兜底判断。测试观测map有4096条上限，同sequence不得改变sampled_at。Agent正常退出后Flush真实Recorder，再查询SQLite逐UTC桶对比接受身份的去重数量，整段总数也必须一致，不能只检查兜底请求成功。

TestTelemetryAgentProcessLostWSSReceipt当前macOS真实CLI race通过92.327秒（场景91.28秒）：丢序号18的成功回执，HTTP最新序号38、距前次POST29.9991765秒，原viewer恢复；全程42个不同fast身份与SQLite逐桶计数完全相符，最终HTTP15→WSS17→HTTP42及有界退出。日志/tmp/viorka-lost-wss-sqlite-race.log。本轮新增落库断言尚未Ubuntu复验；既有Ubuntu丢回执链路结果仍有效但不冒称包含新断言。

新增storage TestTelemetryHistoryTwoPartitionsRangeAndGaps：真实两张周表都有数据，含允许范围之前的物理行；固定now查询恰好168h只得4个合法30秒桶、不补空桶；按小时查询得到3桶，最后两个原始桶合并的fast_count/CPU count/sum严格2/2/40。超过168h边界1ns、短区间但开始时间早于保留边界均拒绝。定向race3.451秒，日志/tmp/viorka-history-partitions-race.log；vet和diff-check通过。属于可控时间SQLite验证，不称跨周长稳。

长测Panel97505/Agent97518实际ps存活182秒；2026-11-03T03:50:55Z JSONL elapsed60秒、HTTP/fresh、sequence15、session_epoch1、恢复剩余0秒。Agent RSS13762560字节/9FD，Panel RSS30244864字节/19FD只是单点，不作稳定资源结论。当前长测构建未替换。其他平台和剩余浏览器、T24等验收继续，双仓未commit/push，goal active。


## P6 第二十七批：Ubuntu兜底落库复验及切换/设置边界审计

2026-11-03。上一轮补齐WSS兜底落库与双周查询证据属于进展。当前Linux测试构建viorka-monitoring-fallback-count-linux-amd64.test SHA256 d829127c91fe528f355803e3397808a04a7362892ca166b30349b6479e654866，上传后sha256sum -c通过，Ubuntu nobody TestTelemetryAgentProcessLostWSSReceipt退出0、90.39秒。丢成功回执序号18，HTTP最新38、距前POST29.99420214秒，实时恢复，全程42个不同fast与真实SQLite逐桶计数一致，HTTP15→WSS17→HTTP42及关闭正常。日志/tmp/viorka-fallback-count-linux.log；短测已结束，不替换现有长测构建。

T17逐断言审计：Agent TestTelemetryScheduleHTTPRealtimeFallback在POST占有31条样本时接收需求，不提前ACK或进入realtime，继续采集到pending；15秒POST完成后ACK才applied。新增精确身份检查POST为1..31，WSS为期间新采32及即时采33，防止只验数量却混入重复旧样本。所有权测试验证释放flight不丢pending；租约测试覆盖重复pending无ACK、重复需求不延长、旧代次/断线/90秒过期。Panel TestTelemetryDemandRenewalGraceAndReceipt验证发送仅pending、匹配回执才realtime、旧回执不覆盖新需求、ACK超时degraded。Agent四项定向race1.103秒、Panel两项1.027秒；仍是两端分层时序证据，不称在真实网络中强制悬挂POST实验。

T36设置边界审计：协议已有默认30、0/61拒绝；Agent已有1/60应用及采集相位检查，前端已有1.5拒绝且不发请求。补充真实REST JSON collection_seconds=1.5返回400，并在所有非法写后GET确认值/Revision完全不变；TestTelemetrySettingsREST race2.302秒，web telemetry-settings18项通过185ms。此前真实UI已验证0拒绝、C60/R30/W1 applied revision2，不等同完整浏览器输入矩阵。双方vet和diff-check通过。

长测Panel97505/Agent97518实际存活373秒，2026-11-03T03:54:05Z JSONL elapsed250秒、HTTP/fresh、sequence21、epoch1，尚未24h。其余平台用户稍后提供；下一步继续真实浏览器后台/多标签和T24等缺口、最终兼容构建检查。双仓未commit/push，goal active。
