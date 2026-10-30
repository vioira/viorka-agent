# Viorka Agent

> M0 当前状态：已实现注册、签名会话、WSS、重连/吊销和只读 node.inspect；完整运行说明与未完成验收见 [GETTING_STARTED.md](docs/GETTING_STARTED.md)。
**Viorka** 的独立节点守护进程项目。负责 VPS/NAT 节点本地资源探测、受控任务执行、代理核心生命周期管理和运行状态汇报。

> 当前状态：**M0 本机通信闭环已实现**。已提供注册/WSS、心跳/重连/吊销和只读节点检查；代理核心安装/启动/配置下发仍未实现，真实 NAT VPS 验收待完成。

## 核心约束

- Agent 主动通过 HTTPS / WSS 连接 Panel，不需要 VPS 开放管理端口。
- 正常代理流量由 Xray / sing-box / Realm 等进程处理，Agent 只负责控制。
- 独立版本和发布周期，协议需要向后兼容。
- 运行需尽量适应 Debian、Ubuntu、Alpine、NAT 小鸡和受限 TCP/UDP 网络。
- Agent 不应默认成为可远程任意执行系统命令的高权限 Shell。
- 面板离线时已有代理进程继续运行，任务按可恢复语义处理。

主机监控新增 Linux CPU、内存/Swap、负载、根文件系统和网卡流量；监控范围及限制见 [MONITORING.md](docs/MONITORING.md)。

## 使用当前脚手架

源码构建需要 Go 1.26 或以上版本。

```bash
go test ./...
go run ./cmd/viorka-agent version
go run ./cmd/viorka-agent doctor
# 验证配置（不会实际连接到 Panel）
go run ./cmd/viorka-agent config-check --config ./examples/agent.example.json
```

`run --config PATH` 使用已注册身份运行；配置无效、未注册、身份吊销或证书校验失败会明确退出。

## 目录结构

```text
viorka-agent/
├── cmd/viorka-agent/    # 可执行入口
├── internal/app/        # 命令与生命周期
├── internal/connection/ # 长连接/重连（设计占位）
├── internal/collector/  # 节点信息采集
├── internal/executor/   # 白名单任务执行（设计占位）
├── internal/runtime/    # 引擎接口及 sing-box / Xray / Realm 适配器目录
├── internal/config/     # 配置发布和回滚（设计占位）
├── internal/state/      # 本地任务记录（设计占位）
├── internal/supervisor/ # systemd/OpenRC 运行管理（设计占位）
├── internal/updater/    # 安全升级（设计占位）
├── protocol/v1/         # 当前协议数据模型（M0 改成从 schema 生成）
├── packaging/           # 服务管理草案模板
├── examples/            # 配置示例
└── docs/                # Agent 设计、安装、安全和开发规范
```

## 文档

- [Agent 架构](docs/AGENT_ARCHITECTURE.md)
- [节点注册与身份](docs/ENROLLMENT.md)
- [配置与兼容](docs/CONFIGURATION.md)
- [引擎适配接口](docs/RUNTIMES.md)
- [安全和权限](docs/SECURITY.md)
- [开发及路线图](docs/DEVELOPMENT.md)

Panel 仓库参见 `github.com/vioira/viorka`（占位路径，需确认 GitHub 组织名）；M0 前不要求跨仓库 Go module 依赖。

## 许可证

尚未确定；正式对外发布前补充项目许可证。
