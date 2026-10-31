# Agent 开发、测试与里程碑

> M0 当前状态：已实现注册、签名会话、WSS、重连/吊销和只读 node.inspect；完整运行说明与未完成验收见 [GETTING_STARTED.md](GETTING_STARTED.md)。
## 当前可运行命令

开发和构建需要 Go 1.26 或以上版本；CI 从 `go.mod` 读取 Go 版本。

```bash
cd viorka-agent
go test ./...
make build
./bin/viorka-agent version
./bin/viorka-agent doctor
./bin/viorka-agent config-check --config examples/agent.example.json
```

`run --config PATH` 使用已注册身份连接 Panel，未注册或配置错误时返回非零退出码。服务安装文件暂不提供，详见 `packaging/README.md`。

## M0–M3 计划

| 阶段 | Agent 交付 | 验收 |
|---|---|---|
| M0 | Keypair、Enrollment、Session 签名、WSS、协议生成、任务 receipt | TLS 严格验证，断线重连，旧消息/重复任务安全处理 |
| M1 | 真实资源采集/聚合、NAT 端口 inventory、状态上报 | 无 root 能报告只读资产，低内存、低连接数稳定运行 |
| M2 | 引擎探测/安装、stage/validate/apply/rollback、进程监督 | Debian/Ubuntu/Alpine 测试，错误配置不覆盖正在运行的配置 |
| M3 | Realm、TCP relay、拓扑/能力反馈 | Relay→NAT→出口代理联通和故障回滚测试 |

## 跨架构编译

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/viorka-agent-linux-amd64 ./cmd/viorka-agent
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o dist/viorka-agent-linux-arm64 ./cmd/viorka-agent
```

静态交叉编译降低发行版 GLIBC 兼容问题；但还应在 Debian 12/13、Alpine 容器和真实低配 NAT VPS 做运行测试。

## 交互式发布

项目版本唯一声明在 `internal/version/version.go` 的 `Version` 常量，CLI 和 WSS 握手共用该版本。
需要 Git、Python 3.9+、项目要求的 Go 工具链，以及远端推送权限；先提交当前开发改动，保持工作区干净，然后运行：

```bash
make release
# 或 python3 scripts/release.py
```

运行 `python3 scripts/release.py --help` 查看帮助。仅允许在 `main` 分支发布。
交互终端中版本号使用青色高亮，通过状态为绿色，预发布提示为黄色，错误为红色；重定向输出、`TERM=dumb` 或设置 `NO_COLOR` 时自动使用纯文本。可用 `NO_COLOR=1 make release` 关闭颜色。
仅有一个远端时自动使用；多个远端时才提示选择（优先默认 `origin`，否则默认第一个），未配置远端则明确报错。脚本获取分支与标签，拒绝本地落后或分叉；远端分支不存在时会提示创建。
远端必须只有一个 push URL，且与 fetch URL 一致，确保检查的目标就是推送目标。
随后通过菜单选择补丁（默认）、次版本、主版本、自定义、当前版本、预发布递增或转正式版，也可以选择 `0` 取消。
预发布递增保留原标识，例如 `0.1.0-bate.1 → 0.1.0-bate.2`；正式版默认生成下一补丁的 `bate.1`，也支持自定义 `alpha`、`beta`、`rc` 等 SemVer 标识。
脚本拒绝脏工作区、游离 HEAD、非法版本、版本倒退及本地/远端重复标签；所选版本必须高于已有有效版本标签（忽略构建元数据）。
更新版本后运行 `scripts/validate.sh`，并静态交叉编译 Linux amd64/arm64。终端按三个阶段显示进度、结果和耗时，成功时不展示测试内部的模拟 Git 操作；失败或中断时停止发布，展示日志末尾并保留临时目录中的完整日志，成功阶段的日志自动删除。
最终确认前展示版本变化、发布类型和待推送提交，确认后再次同步远端检查。
检查通过后，输入 `y` 或 `Y` 确认才会提交版本变更、创建附注标签，并原子推送当前分支和标签；当前分支尚未推送的提交也会一并推送。
确认提示为 `[y/N]`，回车或其他输入默认取消。取消或检查失败会保留版本文件改动，供检查处理。推送失败会保留本地提交和标签，并显示重试命令；不强制推送、不删除标签、不自动回滚。

GitHub Actions 校验标签与代码版本一致，构建 Linux amd64/arm64 静态二进制及各自的 SHA-256 文件，所有检查和构建成功后**自动公开发布 Release**，无需手动发布草稿。
带预发布标识的版本（如 `bate.1`）会自动公开发布并标记为 prerelease，不替换 Latest；正式版本可供默认安装流程发现。
默认安装流程只解析最新正式 Release，安装预发布版本需显式指定 `--version=v0.1.0-bate.1`。
交叉编译通过不代表已完成目标 Linux 发行版运行验证。

## PR 约定

- 更新协议必须在 Panel 主仓库先变更 Schema，并包含新旧样例及兼容性测试。
- 新 Runtime 适配器必须做模拟进程管理/端口冲突/回滚/权限不足测试。
- 任何下载/升级机制都必须固定来源且校验签名/哈希。
- 禁止“空实现返回 nil”掩盖未支持的功能。

## 其他说明

- `go.mod` module path 暂按 `github.com/vioira/viorka-agent` 编写；请按实际 GitHub Owner 调整。
- 独立 Release、安装文档、卸载流程应在 M0 收口，不要把 Agent 代码塞回 Panel 仓库。

## 主机计数扩展

已增加 Linux `node.host_metrics.v1` 只读采集，并由 Panel 的
`host_metrics_version: 1` 确认后在指标快照的 `host` 中上报。旧 Panel
不确认时不发送新字段；旧 Agent 对新 Panel 仍可上报原有指标。

- `disk_io`：`/proc/diskstats` 每设备累计读写 bytes 和 bytes/s，设备标识为
  `major:minor:name`。内核扇区固定换算为 512 bytes；首次、回退、失败恢复、
  超过 600 秒的采样间隔不计算速率。分区和堆叠设备分别报告，不能直接求和。
- `tcp_sockets` / `udp_sockets`：`/proc/net/sockstat{,6}` 的 IPv4+IPv6 `inuse`
  合计。包含监听套接字，TCP 不包含 TIME_WAIT；UDP 是套接字数而非会话数。
  无 IPv6 内核缺少 sockstat6 时使用 IPv4 数据。
- `processes`：分批枚举 `/proc` 顶层数字名称，统计当前 PID 命名空间可见进程，
  不把线程数或累计 fork 数当进程数。进程创建/退出期间为近似瞬时值，hidepid
  等权限设置可能限制可见范围。

每文件最多 256 KiB、最多 64 个磁盘设备、最多枚举 65536 个 proc 项；超限、
读取失败或取消时对应组报告 unavailable，不能把部分结果当完整值。
无需 root 或外部命令。VPS/cgroup/用户级最大进程限制尚未采集，不能把
`pid_max` 或 Agent 自身的 ulimit 当成整台 VPS 的进程配额。

Panel 已支持接收、校验和通过实时指标 API 返回此扩展；新增指标尚未接入
前端展示和历史聚合。需分别发布匹配的 Panel/Agent 后才能启用上报。
