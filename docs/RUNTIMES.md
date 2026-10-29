# Runtime Manager 与代理核心适配器

> 这里定义目标接口。当前仓库仅有 `internal/runtime/engine.go` 统一接口，**没有任何实际 sing-box/Xray/Realm 适配器实现**。

## 1. 总体目标

- Viorka 不重新实现任何代理核心，而是管理二进制、进程、配置、健康与指标。
- Runtime 适配器在运行时进行 `version` 和 `capability` 检测，避免新旧版本命令/配置误用。
- 每种引擎每节点默认一个由 Viorka 管理的实例；同一进程可包含多个服务/入站。
- 发现用户已有第三方安装时，默认 `observed` 只读，明确授权才 `managed`。

## 2. 核心接口

```go
type Engine interface {
    Name() string
    Detect(context.Context) (Info, error)
    Validate(context.Context, []byte) error
    Apply(context.Context, []byte) error
    Start(context.Context) error
    Stop(context.Context) error
    Restart(context.Context) error
    Status(context.Context) (Status, error)
    Stats(context.Context) (Stats, error)
}
```

`Capabilities` 表示能否校验、热加载、采集引擎统计和多实例。实际函数实现可以再细化 staging/commit/rollback 接口；当前只是初始协作边界。

## 3. 引擎预期

| 引擎 | 主要用途 | 最初实现 |
|---|---|---|
| sing-box | Shadowsocks、Trojan、VLESS 等代理与路由服务 | Detect、Validate、Install/Start/Stop、Config apply、Health |
| Xray | VLESS、XTLS/REALITY 等代理服务 | Detect、Validate、Install/Start/Stop、Config apply、Health |
| Realm | TCP/UDP 流量中转 | Detect、Config apply、状态检查；M3 加入链路拓扑 |

### 命令校验（实现时需要按版本测试）

示意使用：

```bash
sing-box check -c /path/to/config.json
xray run -test -c /path/to/config.json
```

实际生产需依据探测出的二进制版本和参数能力执行，不应在未找到可执行文件时盲目调用或下载不可信来源。

## 4. 启动与运行状态

- systemd/OpenRC/foreground 三种方式统一映射到 Supervisor，不硬编码 `systemctl`。
- 对主机资源权限不够的 NAT 小鸡，允许只读观测；提示用户为什么无法安装/启动。
- 每种引擎拥有自己的工作目录、配置、日志和二进制版本，避免覆盖系统现有服务。
- 进程存活不等于业务可用；健康检查需包括配置预检、绑定端口、可选外部连通测试。
- 部署失败最多做有界重试，不能死循环反复重启导致可用服务被反复中断。

## 5. 更新安全

下载必须固定发行源、版本、哈希/签名，匹配 linux/amd64 或 linux/arm64。对任何可执行二进制进行校验后再切换，失败恢复旧版。不要依赖第三方网页里复制的随机 curl|sh 安装脚本。

## 6. Agent 与 Panel 权责

Panel 负责最终期望配置、用户审批、资源依赖和权限；Agent 负责现场兼容性检查与实际执行。Agent 能根据本地系统状态拒绝无法执行的请求，Panel 不应因用户在前端点击了“保存”就显示部署成功。
