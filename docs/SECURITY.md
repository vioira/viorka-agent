# Agent 安全与运行权限

> 当前不包含远端任务运行能力。以下条款用于指导实现，不等于已有认证或安全执行器。

## 威胁和控制措施

| 威胁 | 必须的措施 |
|---|---|
| 伪造 Panel / 中间人 | TLS CA 与 hostname 校验，验证 challenge 的服务器上下文 |
| 窃取 Agent 身份 | Ed25519 私钥本地保存（0600）；定期轮换与撤销 |
| 任务重放 | Idempotency Key + 本地持久化 Task Journal + Revision 检查 |
| 注入 shell | 禁止任意命令字符串；操作白名单与固定 argv、超时 |
| 目录穿越 | 目录白名单、绝对路径、拒绝 symlink 逃逸、atomic replace |
| 错误接管本地服务 | 发现已有进程默认只读，明确绑定管理范围 |
| 恶意自更新 | 验证版本、平台、哈希和签名，失败恢复旧二进制 |
| 配置/私钥泄漏 | 日志脱敏、最小文件权限、不把 Secret 上报给 metrics |

## 权限分层建议

- 主守护程序原则上尽量非 root，单独的受控特权 helper 执行需要的系统写操作。
- 如果第一版为了部署复杂度使用 root，必须限制任务类型、参数、环境变量、文件写路径和动态链接行为，并在文档明示风险。
- Agent 自身配置、身份、代理配置/日志分开目录并明确各自的 owner。
- systemd 可使用 `NoNewPrivileges=true`、只读文件系统、`ReadWritePaths` 等限制（具体权限与功能相容时启用）；Alpine/OpenRC 则通过文件权限/专属用户实现同等约束。

## 可观测与审计

Agent 记录 task_id、operation、node_id、observed_revision、error_code、耗时、重试次数；禁止将密码、令牌、私钥和完整 Xray/sing-box 敏感配置写进日志。敏感操作的审计主要由 Panel 保存。

## 卸载安全

- 卸载 Agent = 停止 Agent 守护进程、删除 Agent 本地身份/二进制（需确认），默认**保留用户已运行的代理服务与配置**。
- 删除 Viorka 自管代理进程、清理节点业务数据或服务商映射必须另设显式选项，且保留必要的恢复手段。
