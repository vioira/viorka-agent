# Agent 注册和身份生命周期

> M0 当前状态：已实现注册、签名会话、WSS、重连/吊销和只读 node.inspect；完整运行说明与未完成验收见 [GETTING_STARTED.md](GETTING_STARTED.md)。
> 下述通用身份轮换仍为目标；当前注册/会话流程已实现，详见 M0 使用说明。

## 1. Enrollment

1. 用户在 Panel 登录，创建可限时的一次性 Enrollment Token，并指定目标节点类型或命名范围。
2. Agent 本地生成 Ed25519 密钥对，私钥权限 0600，不传给 Panel。
3. Agent 通过 HTTPS 发送 Enrollment Token、public key 和协议版本，资产在连接后上报，不通过 URL query 传令牌。
4. Panel 验证令牌有效期、使用状态和限流，绑定管理员预建的节点，令牌原子消耗；当前权限为实例级角色。
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

### 首次注册失败后的恢复

Agent 首次注册和显式重新注册的单次 HTTP 请求超时均为 30 秒。

首次注册请求前会保存密钥；`identity.json` 存在不代表注册完成。HTTP 502 等失败可能留下 `node_id` 为空的待注册身份。安装器重试会保留原配置和密钥，使用提供的令牌继续注册；已有非空 Node ID 的身份仍走更新流程，不自动重新注册。安装器与 Agent 二进制须使用支持统一 `register` 入口的版本。

如旧安装器已创建服务并反复报 `enrollment incomplete`，先停止 Agent 服务以释放状态目录锁（不会停止代理服务），在原目录执行 `./viorka-agent register --config agent.json --token-stdin`，通过标准输入提供有效令牌，成功后启动 Agent 服务。不要删除身份文件或使用 `--reenroll-node` 来修复首次注册。502 的根因需结合 Panel 和反向代理日志排查；若原令牌已失效或被消费，应先在 Panel 核对注册结果，再按其恢复流程处理。

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

Panel 管理员确认重置离线或禁用节点后，沿用原节点 ID 并签发新 token。安装命令增加 `--reenroll-node=ID`，安装器保留原配置，停止已验证归属的 Agent 服务，再调用 `register --token-stdin --replace-node=ID`，成功后启动服务。此操作不触碰代理服务或任务 Journal。外部 supervisor/前台 Agent 必须先停止；状态目录锁拒绝并发执行。

CLI 校验本地身份属于该 Panel 和目标节点，创建新的 Ed25519 密钥，保存在 0600 的 `reenrollment.json`（只存 token 哈希，不存原 token）。同一 token 重试复用该密钥，支持服务器已消费 token 但响应丢失的恢复。原身份在响应节点 ID 和协议验证成功后才被原子替换；失败保留原文件和重试密钥。原凭证已由 Panel 撤销，保留文件不表示还能连接。新 token 可重新开始失败的注册。网络错误或吊销不会自动触发此流程。

发布要求：先发布支持 `--replace-node` 的 Agent 二进制和支持 `--reenroll-node` 的安装脚本，再部署生成该命令的 Panel。旧二进制拒绝该参数，需指定已发布的新版本后重试。历史数据保留在 Panel 原节点记录下，遵循已有数据保留策略。

## 共享短期会话

管理连接在同一 Agent 进程内复用尚有效的 session；临近到期前 30 秒统一刷新，
并让旧管理连接退出后重连。并发调用串行完成一次 challenge/签名/session 请求，
不会因每次重连或未来的指标 POST 独立签发而互踢。会话凭证只保存在内存，
进程重启重新认证；本地 generation 用来拒绝迟到响应对新凭证的状态覆盖。

身份拒绝和证书验证失败不会通过再次 Enrollment 或切换上传通道绕过。
该共享协调器已接入现有 WSS；新版指标 POST/租约/调度仍在开发中，尚未启用。


## 删除旧节点后注册新节点

在管理 WSS 已连接时，Agent 收到 SIGTERM 或 SIGINT 会在退出前通知 Panel。支持主动停机的 Panel 随即将节点标为离线并推送页面更新；未收到通知时沿用心跳超时。

`stop` 只停止 Agent，不删除本地身份。Panel 删除节点后，旧身份无法再获取会话，日志会出现 `agent identity rejected; contact administrator`；OpenRC 可能随后达到重启次数上限。旧安装器遇到已有非空 Node ID 时忽略新令牌，因此重新执行新节点的安装命令不能修复。新版安装器检测到已注册身份和本次普通单节点安装提供新令牌时，提供「1) 覆盖重新安装 / 2) 退出并查看恢复说明」选择。回车、其他输入或无交互终端都退出，保留原数据。通过 `/dev/tty` 读取选择，与用于传递令牌的标准输入独立。

选择覆盖后，安装器复用版本和平台均匹配的本地 Agent，或下载并校验目标版本。配置校验通过后，安装器停止已验证归属的 Agent 服务，通过进程检查和 `flock` 排除旧状态占用，将旧配置和整个状态目录备份到权限 0700 的 `retired-node-*`，使用本次 URL、CA 和令牌重新注册。不会停止代理服务。失败保留备份和新注册状态供排查或重试，不自动恢复旧身份启动。覆盖重装不沿用自定义配置；原配置保存在备份中，原配置引用的外部状态目录不被删除。

仅升级已有节点时使用不含令牌的升级命令，见[安装说明](../packaging/README.md)。`--reenroll-node` 仅用于原节点换钥，不支持跨 Node ID 迁移。确认旧节点已经删除、要绑定新节点时，先停止 Agent，备份隔离整个旧状态目录，再执行新节点安装命令。不能仅删除 `identity.json`：旧 Journal、遥测设置和流量状态也属于旧身份。

以下适用于安装器默认路径和 OpenRC；先检查 `agent.json` 的 `state_dir` 确实为 `/root/.viorka-agent/state`，并确认没有其他前台 Agent 或安装进程。自定义路径应按实际配置处理。

```sh
rc-service viorka-agent stop
rc-service viorka-agent status
```

确认 Agent 已停止后，执行备份（任一步失败则停止处理）：

```sh
(
    set -eu
    umask 077
    cd /root/.viorka-agent
    test -d state
    test ! -L state
    backup=$(mktemp -d ./retired-node-XXXXXX)
    mv state "$backup/state"
    printf '旧节点状态已保留在 %s\n' "$backup"
)
```

随后执行 Panel 新节点的有效安装命令。原 `agent.json` 保留，因此此流程要求仍连接同一 Panel；跨 Panel 迁移需另行核对配置。新安装会创建独立状态和密钥，不触碰代理服务。备份含私钥，须保持受限权限；旧身份已失效，恢复备份不能恢复其 Panel 认证权限。注册失败时保留新建的待注册状态，按首次注册恢复流程重试，不反复移动状态目录。

成功以日志 `Panel connected; session established` 和 Panel 新节点在线为准，不能只看服务已启动。此处不迁移旧节点历史和流量基线。

## 托管引擎复用

原节点 `--replace-node` 换钥保留引擎和本地配置。删除原节点后使用新令牌注册时，Agent 核对 `/var/lib/viorka-engines` 中的 Viorka 托管引擎，随注册请求提交有效配置和原引擎 ID；Panel 原子恢复服务、凭证、修订与管理归属。此过程只读取节点的代理状态。引擎仅安装而没有活动配置时，Panel 记录为已安装、未发布配置。第三方服务、活动配置不一致或旧节点仍持有引擎时拒绝恢复，失败保留本地数据和待注册身份供重试。

节点已重新注册时，管理员可在「引擎与服务」中选择引擎，点击「恢复托管引擎」，查看只读核对结果后确认。该入口需要在线 Agent 宣告 `engine.recovery.v1`。Panel 保留当前节点 ID，以本机原引擎 ID 恢复记录。只有失败安装留下的空记录可替换，失败任务仍保留。原运行意愿、服务 ID 和凭证来自最后成功应用的配置。公网入口使用可匹配的已发布备份，旧订阅引用由管理员重新绑定。

先更新 Panel，再更新 Agent。新 Agent 可能在注册快照中上报 `unconfigured`，旧 Panel 的严格解码无法识别该字段。现有生命周期任务继续使用 `engine.manage.v8`。

## 批量自动注册

管理员在 Panel 节点页创建批量注册令牌（1–720 小时、1–10000 个新节点），同一令牌可在多台新服务器使用。Panel 无需预建节点，收到合法注册请求后原子创建离线节点、独立公钥身份及审计。真实 WSS 建立后才变为在线。

从 Panel 批量令牌弹框复制安装命令，在每台目标服务器执行即可。命令携带共享令牌，使用 `--registration --token-stdin` 将其通过标准输入交给安装器。完整命令可能留在剪贴板和 shell 历史中。非交互部署先取得并核对安装脚本，再从部署工具提供的受限文件输入：

```sh
sh install.sh install --url=https://panel.example.com --registration --token-stdin \
  < /run/secrets/viorka-registration-token
```

已有匹配版本二进制和本地配置时：

```sh
viorka-agent register --config /absolute/path/agent.json --token-stdin --name hk-01 \
  < /run/secrets/viorka-registration-token
viorka-agent run --config /absolute/path/agent.json
```

`--name` 可选，为 Panel 显示名称，最多 128 UTF-8 字节；省略时 Panel 使用令牌前缀加本机 hostname。它与本地配置的 `node_name` 启动标签独立。Panel 管理员之后可在节点详情修改显示名称，不会改本机 hostname、身份或代理配置。

`register` 使用 `POST /api/v1/agents/register`，传 `token`、`public_key`、`protocol_version: "1.0"`、`hostname` 和可选 `name`。Panel 自动识别单节点或批量令牌：单节点令牌绑定预建节点，批量令牌创建新节点。部署支持批量引擎恢复的 Panel 后，两种注册均可携带本机托管引擎快照。若引擎仍归属其他节点，Panel 返回 409 并回滚注册及配额。旧版 Panel 会拒绝携带快照的批量注册请求。它不自动替换已注册身份。安装器拒绝 `--registration` 与 `--reenroll-node` 或明文 `--token` 组合；共享令牌只经交互或标准输入传递。单节点注册同样使用 `register`；显式换钥使用 `register --replace-node`，不再提供 `enroll` 命令。

请求前落盘密钥，响应丢失或进程重启后复用原密钥；有效令牌下同一公钥重试不重复创建节点或扣配额。过期/撤销令牌不能用于恢复响应，禁用/删除节点也不会被重新创建。需管理员核对注册记录再走显式恢复流程，不能删掉身份文件盲目重试。不要把状态目录或私钥放进可克隆镜像。

注册成功后不保存批量令牌。后续仍以各自私钥签名 Challenge、使用短期 Session 和 WSS；令牌撤销不影响已注册节点。Panel 快照恢复会撤销所有批量令牌，删除节点不返还配额。

兼容边界：本次新增 REST bootstrap，不变更 Envelope 1.0 或能力协商。先发布支持 `register` 的 Agent 二进制和支持 `--registration` 的安装脚本，再启用配套 Panel/Web。统一入口需要配套新版 Panel 和 Agent；本地源码和测试不代表制品已发布。

支持 `engine.endpoints.v1` 的 Agent 在原 Panel 管理连接上定期接收已发布且已确认的服务公网入口，并在托管引擎目录内原子保存 `published-endpoints.json`（0600）。迁移前确认 Agent 日志出现 `Published endpoints saved` 且已同步最后一次入口修改。保留该目录及引擎状态，以新身份注册到支持本协议的 Panel 时，匹配有效配置的入口随引擎一起恢复。
