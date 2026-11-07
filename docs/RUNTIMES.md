# 托管引擎运行时

## 当前实现

`internal/runtime/managed.go` 实现类型化任务运行时，`render.go` 生成 sing-box/Xray 的 VLESS REALITY 配置和 Realm TCP 配置，`download.go` 下载校验固定发行资源，`preflight.go` 进行端口和目标预检。旧 `Engine` 接口仅为接口定义，实际任务通过 `Managed.Execute` 路径执行。

支持范围为 Linux + systemd/OpenRC + root，架构 amd64/arm64；其他环境不声明 `engine.manage.v3`。运行目录 `/var/lib/viorka-engines/<name>`，systemd unit 为 `viorka-engine-<name>.service`；OpenRC 服务脚本为 `/etc/init.d/viorka-engine-<name>`（0755）。第三方同类进程、常见安装和 unit 会阻止安装，不自动接管。尚无持续第三方资产发现。

安装不启动。配置以结构化任务为输入，不接受任意 Shell、URL 或原生配置。sing-box 执行 `check -C`，Xray 执行 `run -test -c`；Realm 2.9.6 没有原生配置检查命令，使用严格结构化渲染校验和启动后的本地检查。修改运行配置可能重启整个引擎。停止意愿会 disable/stop，停止时应用配置不启动。

预检包含绑定端口、目标 DNS/TCP 和 REALITY TLS 校验。Realm 的明确允许目标暂不可达仅豁免网络失败，不能绕过自循环和格式检查。失败尝试有界恢复，回滚失败返回 degraded。进程/本地监听正常不代表外部客户端可用。

## 任务与文件安全

先持久化执行意图再执行副作用。终态重投返回本地结果；中断记录返回 unknown，禁止自动重复执行。任务 journal 有数量限制，长期运行到上限需要受控维护，目前不自动删除执行凭据。

发行清单同步自 Panel 的 `api/agent/v1/engine-releases.json`，固定 HTTPS 官方资源、版本、架构及 SHA-256。归档限制大小并拒绝链接和重复可执行条目。systemctl 采用固定参数，不经过 Shell；命令和回滚有超时。机密配置和状态保存在私有目录，不输出完整配置和进程输出。

## 验收边界

本地 Go、race、vet、协议和 Linux 双架构编译检查覆盖实现。运行时测试采用临时文件、本地监听及模拟 supervisor；没有在真实 Linux/systemd 和外部客户端上执行安装、VLESS REALITY、Realm、升级、故障回滚及重启恢复。因此清单为开发适配清单，不能宣称生产兼容。

当前只有每种引擎一个固定版本；升级路径存在但尚无第二个可选版本及真实升级证据。受控运行日志查看、被动第三方实例发现仍未实现。用户确认本次记录实机验收缺口，不以模拟测试替代实机验收。

## sing-box 分文件配置

托管 sing-box 1.14.4 使用 `/var/lib/viorka-engines/sing-box/conf.d/`，校验与启动分别为 `sing-box check -C <目录>` 和 `sing-box run -C <目录>`。

- `00-base.json`：warn 级别日志、时间戳及 direct 出站。
- `00-egress.json`：本地 DNS、带域名解析器的 `sb-egress-direct` 出站，以及所有启用节点的 300ms sniff 规则；`route.final` 指向该出站，确保域名解析器实际用于默认出口。
- `01-<service_id>.json`：每个启用的节点（协议中的 EngineService）一个 VLESS REALITY 配置文件，使用稳定 ID 命名，不采用可修改的显示名称。停用或删除节点后，新配置集不再包含其文件与 sniff 匹配项。凭据仍来自结构化任务，不使用示例中的固定密钥。

配置目录权限为 0700，文件为 0600。`conf.d` 是指向同一私有引擎目录下 `.conf-*` 版本目录的受控相对符号链接；先生成完整配置集并执行原生校验，再原子替换链接，避免逐文件覆盖造成新旧混合。仅接受 Agent 格式的私有版本目录，拒绝已有普通 conf.d 目录、外部链接和悬空链接，避免接管手工配置。此目录专供 Agent 管理，手工添加文件会在 inspect 时报告不一致。

旧安装在下一次 `engine.apply` 时迁移并更新 unit；单纯升级 Agent 不改写正在运行的配置。迁移失败恢复原链接与 unit，旧 `config.json` 保留作为迁移前备份，但新 unit 不再读取它。已迁移安装支持整组回滚，成功后清理被替换的版本目录；回滚失败保留现场并返回 degraded。进程中断可能留下 staging 版本目录，现有 journal 将中断任务标为 unknown，不自动重放或清理这些目录，需人工核查。Observed Revision 仍只在部署操作和必要健康检查成功后保存。

文件系统测试覆盖分片、停用/删除、旧配置迁移、校验拒绝、回滚、权限与链接边界。当前未在实际 sing-box 1.14.4 / Linux systemd 环境验证目录合并和业务连通性；本地模拟 supervisor 的测试不代表实机验收通过。

引擎任务使用 Panel 权威 Schema 的 `EngineService.short_ids` 数组，VLESS 为 1 至 8 个不重复（忽略大小写）的偶数位十六进制值，每项 2 至 16 位，Realm 为空数组。sing-box 的 `reality.short_id` 与 Xray 的 `realitySettings.shortIds` 原样接收完整列表。旧单值 `short_id` 不再接受；仅声明 `engine.manage.v2`，需要匹配的新 Panel，不回退 `engine.manage.v1`。本次在本地同步协议与渲染，未发布 Agent 制品。

## 2026-11-05 共享出口与分流

当前引擎能力为 `engine.manage.v3`，需配套的新 Panel；上文 v2 为此前交付记录。仅 sing-box apply 接收结构化出口与分流：SOCKS5、HTTP CONNECT（可选 TLS）及 VLESS TCP REALITY。Panel 解析全局出口，节点不支持覆盖；Agent 严格验证引用、范围、字段及协议约束。

`00-egress.json` 渲染出口、顺序规则及默认动作。不同条件使用 logical AND，HTTP 仅用于明确 TCP 规则，默认拒绝以末尾 reject 动作实现；代理不可用不会自动直连。域名嗅探可关闭，有 IP 条件时通过本地 DNS 解析。DNS 查询和出口服务器域名使用本地解析器。整组文件复用原生检查、原子切换和失败回滚。

已在 macOS arm64 上对官方 sing-box 1.14.4 发行包核验 SHA-256，并实际通过 `check -C`；`VIORKA_TEST_SINGBOX=/path/to/sing-box go test ./internal/runtime -run TestSingboxNative -v` 验证配置及本地 HTTP/SOCKS 夹具流量（规则、直连、代理、代理失效不降级、默认拒绝）。这不是 Linux/systemd、远端 TLS 或 UDP 实机验收；未发布或部署到真实节点。未设置环境变量时原生测试明确 skip，普通渲染和文件回滚测试仍执行。

2026-11-06：EngineOutbound 支持普通 VLESS TCP：type=vless、tls=false（或省略），仅使用服务器、端口和 UUID，禁止 REALITY/TLS 字段和 Flow。tls=true 仍要求完整 REALITY 参数。字段及 engine.manage.v3 不变；旧实现明确拒绝普通 TCP，需配套更新 Agent 校验。本次仅本地实现，未发布或部署。

## 2026-11-07 Alpine / OpenRC 引擎管理

OpenRC 使用固定路径的 rc-service、rc-update 和 supervise-daemon；能力探测要求 /run/openrc 存在且上述工具及 /sbin/openrc-run 可执行。服务通过 supervise-daemon 托管，崩溃后间隔 10 秒恢复，120 秒内最多重启 3 次。输出沿用 systemd 的丢弃策略。安装只写入引擎和服务脚本；启动加入 default runlevel，停止和卸载移除实际存在的 runlevel 成员关系。Agent 退出不停止引擎。

sing-box 分文件配置切换及失败回滚保留 OpenRC 脚本的 0755 权限。探测到第三方 /etc/init.d/<引擎名> 时拒绝接管。任务协议和 engine.manage.v3 不变，更新 Agent 并重连后上报能力，Panel 按该能力放行安装。

回归测试覆盖探测依赖、控制命令及错误、安装/卸载、配置回滚、权限和第三方脚本拒绝。CI 新增 Alpine 3.21 amd64/arm64 的真实 OpenRC 生命周期测试，使用本地进程夹具验证启停、崩溃恢复与开机自启链接；该测试不验证真实 sing-box 的代理流量。

### 引擎任务诊断日志

引擎任务沿用节点动态 log_level，关联 task_id、resource_id、operation、attempt、engine、version、revision。info 记录任务开始和完成；error 记录失败、执行阶段及耗时；debug 记录 Journal 重投、意图/结果持久化、下载响应状态与大小、校验/解压、二进制和服务写入、配置准备及命令执行。中断记录重投时明确记录拒绝重复副作用。

服务管理命令的诊断输出上限为 8 KiB，超出标记截断；失败时为 error，正常结果及未启动状态探测为 debug。引擎配置校验器只记录命令名、退出错误和耗时，不输出原始 stdout/stderr、参数或配置内容。下载错误剥离 URL 包装，避免记录签名下载链接。代理进程的持续业务日志仍按服务模板输出到 /dev/null。

卸载托管引擎允许存在已应用服务：停止并禁用系统服务后移除服务文件和二进制。Panel 决定保留或删除服务草稿及凭证；该选择不改变 Agent 命令，卸载成功后才清理 Panel 数据。重新安装不会自动恢复业务，需由 Panel 再次应用配置。

### 低内存节点安装与升级

下载与解压采用磁盘暂存：压缩包写入同一私有引擎目录的 `.archive-*` 文件，同时计算 SHA-256；校验通过后，以 32 KiB 拷贝缓冲区解压到 `.binary-*` 文件，检查 ELF 标识、大小和归档边界，设置 0700 并同步后原子替换目标二进制。暂存文件不用系统 /tmp，避免常见 tmpfs 占用节点内存；正常完成、失败或取消时清理本次暂存文件。SIGKILL/断电仍可能留下暂存文件，任务按现有 Journal 语义报告 unknown。

升级的旧二进制备份和失败恢复也改为流式原子写入，不将旧、新二进制读入 Go 堆。磁盘须容纳压缩包、解压后的新二进制及升级备份；原有校验、下载大小限制、服务启停和健康检查语义保持不变。

2026-11-07 在 macOS arm64 上运行 `go test ./internal/runtime -run '^$' -bench BenchmarkExtractBinaryStream -benchmem -benchtime=1x`：4 MiB 与 96 MiB 的 tar.gz 二进制解压落盘均分配 121,616 字节（约 119 KiB），40 次分配。该数据为解压函数的 Go 分配量，不是整个 Agent 的 RSS。回归覆盖 tar.gz/zip、校验拒绝、截断归档、下载中断、任务取消、写入失败、临时文件清理、备份恢复和链接拒绝。
