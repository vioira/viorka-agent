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
