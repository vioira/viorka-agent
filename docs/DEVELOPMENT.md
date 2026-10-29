# Agent 开发、测试与里程碑

## 当前可运行命令

```bash
cd viorka-agent
go test ./...
make build
./bin/viorka-agent version
./bin/viorka-agent doctor
./bin/viorka-agent config-check --config examples/agent.example.json
```

注意：`run` 命令当前明确返回非零退出码，**不会悄悄驻留并假称已连接 Panel**。服务安装文件暂不提供，详见 `packaging/README.md`。

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

## PR 约定

- 更新协议必须在 Panel 主仓库先变更 Schema，并包含新旧样例及兼容性测试。
- 新 Runtime 适配器必须做模拟进程管理/端口冲突/回滚/权限不足测试。
- 任何下载/升级机制都必须固定来源且校验签名/哈希。
- 禁止“空实现返回 nil”掩盖未支持的功能。

## 其他说明

- `go.mod` module path 暂按 `github.com/vioira/viorka-agent` 编写；请按实际 GitHub Owner 调整。
- 独立 Release、安装文档、卸载流程应在 M0 收口，不要把 Agent 代码塞回 Panel 仓库。
