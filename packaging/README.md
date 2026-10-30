# Agent 发布与安装

`.github/workflows/release.yml` 在推送 `v*` tag 后运行测试并构建 Linux amd64/arm64 静态二进制，创建待人工审核的 Draft Release：

- `viorka-agent-linux-amd64` 和 `viorka-agent-linux-arm64`
- 每个二进制对应的 `.sha256` 文件，内容为单独一行 SHA-256 十六进制摘要。

审核资产后手动发布正式 Release，Panel 安装脚本才可通过 GitHub `releases/latest` 发现它。草稿和预发布不会作为最新正式版本使用。工作流已配置不代表已有版本发布；需将变更推送并完成真实发布验收。

仓库根目录 `install.sh` 提供 POSIX sh 安装入口，Panel 注册弹框生成一行命令：

```sh
curl -fsSL --proto '=https' --proto-redir '=https' 'https://raw.githubusercontent.com/vioira/viorka-agent/main/install.sh' | sh -s -- install --url='https://panel.example.com' --token='面板生成的一次性令牌'
```

面板生成的命令携带 `--token`，复制执行后自动注册，无需再次输入。令牌仅可使用一次且有效期为 15 分钟；参数方式可能出现在 shell 历史和安装进程参数中，因此不要分享完整命令。安装器不打印或持久保存令牌，传给 Agent 时仍使用标准输入。省略 `--token` 时回退到终端隐藏输入。节点 ID、名称和类型由 Panel 令牌绑定，无需传入 `-id` 或名称。`--url`（兼容 `-url`）仅接受 HTTPS Origin，不是 `/wss` 地址。未实现的 `-secret`、`-auto_update` 等参数明确拒绝。

需要 Linux amd64/arm64、POSIX sh、curl、sha256sum，无需 root。仅隐藏输入模式额外需要 stty 和交互终端。可选 `--version=vX.Y.Z` 固定安装版本；默认解析最新正式 Release，再从同一版本下载二进制和 SHA-256。私有 CA 可追加 `--ca-file=/absolute/path/ca.pem`，仍验证主机名。

入口脚本来自 GitHub main，通过 HTTPS 获取；运行前可先下载并审阅，或将 URL 中 main 替换为审核过且包含安装器的提交/tag。SHA-256 验证的是发行二进制，不是入口脚本的独立签名。必须先将 `install.sh` 发布到对应 Git 引用，并发布正式 Release 资产；本地有文件不代表线上命令已经可用。

安装到当前用户 `$HOME/.local/share/viorka-agent`，目录 0700，配置 0600。目录已存在时拒绝覆盖，下载/注册失败保留现场供排查，不自动删除身份或重新注册。配置使用绝对状态路径。隐藏输入模式会恢复原来的终端回显状态；两种模式均通过标准输入将令牌传给 Agent。

当前仅提供前台启动，不创建 systemd/OpenRC 服务，不设置开机自启或自动更新。关闭终端会停止 Agent。重启已有 Agent：

```sh
"$HOME/.local/share/viorka-agent/viorka-agent" run --config "$HOME/.local/share/viorka-agent/agent.json"
```

安装器回归测试：`python3 scripts/test_install.py`。使用真实 shell、PTY、文件权限与 SHA-256，下载/平台/Agent 可执行文件使用本地 fixtures；不访问生产数据，不替代真实 Linux Release 安装验收。

SHA-256 用于完整性校验，发布来源依赖 GitHub HTTPS 与仓库发布权限；独立签名验证、服务管理和真实 Debian/Alpine 安装验收尚未完成。
