# 托管引擎运行时

## 当前实现

`internal/runtime/managed.go` 实现类型化任务运行时，`render.go` 生成 sing-box/Xray 的 VLESS REALITY 配置和 Realm TCP 配置，`download.go` 下载校验固定发行资源，`preflight.go` 进行端口和目标预检。旧 `Engine` 接口仅为接口定义，实际任务通过 `Managed.Execute` 路径执行。

支持范围为 Linux + systemd + root，架构 amd64/arm64；其他环境不声明 `engine.manage.v1`。运行目录 `/var/lib/viorka-engines/<name>`，独立 unit 为 `viorka-engine-<name>.service`。第三方同类进程、常见安装和 unit 会阻止安装，不自动接管。尚无持续第三方资产发现。

安装不启动。配置以结构化任务为输入，不接受任意 Shell、URL 或原生配置。sing-box 执行 `check -c`，Xray 执行 `run -test -c`；Realm 2.9.6 没有原生配置检查命令，使用严格结构化渲染校验和启动后的本地检查。修改运行配置可能重启整个引擎。停止意愿会 disable/stop，停止时应用配置不启动。

预检包含绑定端口、目标 DNS/TCP 和 REALITY TLS 校验。Realm 的明确允许目标暂不可达仅豁免网络失败，不能绕过自循环和格式检查。失败尝试有界恢复，回滚失败返回 degraded。进程/本地监听正常不代表外部客户端可用。

## 任务与文件安全

先持久化执行意图再执行副作用。终态重投返回本地结果；中断记录返回 unknown，禁止自动重复执行。任务 journal 有数量限制，长期运行到上限需要受控维护，目前不自动删除执行凭据。

发行清单同步自 Panel 的 `api/agent/v1/engine-releases.json`，固定 HTTPS 官方资源、版本、架构及 SHA-256。归档限制大小并拒绝链接和重复可执行条目。systemctl 采用固定参数，不经过 Shell；命令和回滚有超时。机密配置和状态保存在私有目录，不输出完整配置和进程输出。

## 验收边界

本地 Go、race、vet、协议和 Linux 双架构编译检查覆盖实现。运行时测试采用临时文件、本地监听及模拟 supervisor；没有在真实 Linux/systemd 和外部客户端上执行安装、VLESS REALITY、Realm、升级、故障回滚及重启恢复。因此清单为开发适配清单，不能宣称生产兼容。

当前只有每种引擎一个固定版本；升级路径存在但尚无第二个可选版本及真实升级证据。受控运行日志查看、被动第三方实例发现仍未实现。用户确认本次记录实机验收缺口，不以模拟测试替代实机验收。
