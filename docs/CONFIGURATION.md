# Agent 配置文件与配置部署

## 1. 当前可执行的校验

```bash
cd viorka-agent
go run ./cmd/viorka-agent config-check --config examples/agent.example.json
```

初始配置 JSON：

```json
{
  "panel_url": "https://panel.example.com",
  "node_name": "nat-tokyo-01",
  "state_dir": "/var/lib/viorka-agent",
  "log_level": "info"
}
```

当前 `config-check` 只检验 JSON 结构与 HTTPS base URL 等基础字段；**不会连接服务器或启动 Agent**。文件内故意不放持久注册凭证。

可选 `ca_file` 指向私有 CA PEM，仍验证主机名；`enroll/run` 用法见 [GETTING_STARTED.md](GETTING_STARTED.md)。

## 2. Agent 配置分层

| 层 | 来源 | 内容 |
|---|---|---|
| Bootstrap | 本地 `agent.json` / CLI env | Panel Origin、节点名称、本地状态路径、日志级别 |
| Identity | 本地受保护存储 | node_id、密钥、会话状态，不应复制到源码仓库 |
| Desired Runtime | Panel immutable Revision | sing-box/Xray/Realm 具体目标配置 |
| Observed Runtime | Agent state store | 最后成功 Revision、配置 hash、引擎进程和健康 |

Panel 的 Desired 不直接覆盖 Bootstrap 或 Identity；Agent 也不把自己的临时运行状态当作中央 Desired。

## 3. 配置发布流程

```text
Receive Revision → Validate metadata/permission/capabilities
 → Fetch securely / receive config payload → SHA-256 check
 → Staging temp file → engine validate
 → Backup currently active config → Atomic switch
 → Restart / reload (capability-dependent)
 → Health-check → Persist observed revision → Ack success
                ↘ on failure → Try rollback → Report actual state
```

- 原子文件替换必须考虑系统文件权限、磁盘写入完成、跨文件系统 rename 等条件。
- 静态校验成功不意味着端口能监听或 TLS/REALITY 能成功完成握手；需要进程与网络健康检查。
- 失败回滚不能被描述成绝对保证；可能遇到旧文件损坏/运行时缺失，必须显式上报 degraded。

## 4. Revision 保护

Agent 收到旧于当前 Revision 的普通 apply 指令时应拒绝；如果操作者确实要恢复旧版本，应由 Panel 创建一条新的 Revision，内容引用历史快照，保证版本仍单调递增并可审计。

## 5. 本地配置与机密

- `agent.json` 不存长期 Authorization Token 或代理私钥明文。
- 文件目录权限需要 0700、敏感文件 0600（根据运行账户调整）。
- 完整代理配置可能仍包含业务 Secret，必须限定读权限和备份策略。
- 日志只保留配置 Revision、Hash、操作结果，异常堆栈须脱敏。
