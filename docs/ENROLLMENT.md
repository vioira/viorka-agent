# Agent 首次注册和身份生命周期（目标方案）

> M0 当前状态：已实现注册、签名会话、WSS、重连/吊销和只读 node.inspect；完整运行说明与未完成验收见 [GETTING_STARTED.md](GETTING_STARTED.md)。
> 下述通用身份轮换仍为目标；当前注册/会话流程已实现，详见 M0 使用说明。

## 1. Enrollment

1. 用户在 Panel 登录，创建可限时的一次性 Enrollment Token，并指定目标节点类型或命名范围。
2. Agent 本地生成 Ed25519 密钥对，私钥权限 0600，不传给 Panel。
3. Agent 通过 HTTPS 发送 Enrollment Token、public key、inventory，不通过 URL query 传令牌。
4. Panel 验证 TTL/使用次数/限流和组织权限，创建节点身份，令牌原子消耗。
5. Agent 保存 node_id、服务器地址、已建立的身份和协议版本。
6. 后续通过短期签名 challenge 获取短期 session token；WSS Upgrade 时放 `Authorization` 头。

示意：

```text
Panel admin ---create once token----> user
user ------secure provisioning------> Agent
Agent ------HTTPS enrollment--------> Panel
Agent <-----node ID------------------- Panel
Agent ------challenge signature-----> Panel
Agent <===== WSS/TLS session =======> Panel
```

## 2. 不允许的做法

- 所有节点共用一个长期 `AGENT_SECRET`；不得只凭 node_id 接入。
- 允许 `--insecure` 跳过 TLS 认证后用于真实生产。
- 在日志中打印 Enrollment Token、Authorization 或身份私钥。
- 把敏感凭证写进安装命令行参数并留在 shell history。
- 因连接错误自动重新 Enrollment、绕过吊销。

## 3. 密钥更新和节点解绑

- 私钥按 Node 身份存储，Panel 允许管理员吊销/换发。
- 双向滚动换钥时应有明确过渡窗口，吊销后无法建立新会话。
- 节点解绑默认仅撤销管理连接和删除面板资产，**不停止现有代理服务或清空本地配置**。
- 清理 Agent 本地身份和文件应由明确的卸载步骤完成。

## 4. NAT 网络要求

- 节点只需能够向 Panel 发起 TCP/443 出站连接。
- 没有入站公网端口也能被管理，Panel 不通过 SSH 直连。
- 代理业务服务自己的公网端口仍由服务商或用户负责配置，Agent 管理通道不代替端口映射。
- WSS 代理可能在空闲超时断开，必须使用保活和指数退避策略，不能在掉线时结束本地服务。

## 显式重新注册（保留节点历史）

Panel 管理员确认重置离线或禁用节点后，沿用原节点 ID 并签发新 token。安装命令增加 `--reenroll-node=ID`，安装器保留原配置，停止已验证归属的 Agent 服务，再调用 `enroll --token-stdin --replace-node=ID`，成功后启动服务。此操作不触碰代理服务或任务 Journal。外部 supervisor/前台 Agent 必须先停止；状态目录锁拒绝并发执行。

CLI 校验本地身份属于该 Panel 和目标节点，创建新的 Ed25519 密钥，保存在 0600 的 `reenrollment.json`（只存 token 哈希，不存原 token）。同一 token 重试复用该密钥，支持服务器已消费 token 但响应丢失的恢复。原身份在响应节点 ID 和协议验证成功后才被原子替换；失败保留原文件和重试密钥。原凭证已由 Panel 撤销，保留文件不表示还能连接。新 token 可重新开始失败的注册。网络错误或吊销不会自动触发此流程。

发布要求：先发布支持 `--replace-node` 的 Agent 二进制和支持 `--reenroll-node` 的安装脚本，再部署生成该命令的 Panel。旧二进制拒绝该参数，需指定已发布的新版本后重试。历史数据保留在 Panel 原节点记录下，遵循已有数据保留策略。
