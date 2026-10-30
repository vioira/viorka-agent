# Agent 发布与安装

`.github/workflows/release.yml` 在推送 `v*` tag 后运行测试并构建 Linux amd64/arm64 静态二进制，创建待人工审核的 Draft Release：

- `viorka-agent-linux-amd64` 和 `viorka-agent-linux-arm64`
- 每个二进制对应的 `.sha256` 文件，内容为单独一行 SHA-256 十六进制摘要。

审核资产后手动发布正式 Release，Panel 安装脚本才可通过 GitHub `releases/latest` 发现它。草稿和预发布不会作为最新正式版本使用。工作流已配置不代表已有版本发布；需将变更推送并完成真实发布验收。

Panel 注册弹框提供 Bash 脚本：识别 Linux 架构，解析最新正式版本，再从同一版本下载二进制和校验文件；校验通过后安装到当前用户 `$HOME/.local/share/viorka-agent`。目录已存在时拒绝覆盖，失败保留目录供排查。需要 Bash、curl、sha256sum 和 CA 信任；令牌通过终端隐藏输入和标准输入传递。

当前仅提供前台启动，不创建 systemd/OpenRC 服务，不设置开机自启。关闭终端会停止 Agent。重新运行已安装 Agent 时先进入安装目录，再执行 `./viorka-agent run --config agent.json`，因为配置中的状态目录相对于工作目录。

SHA-256 用于完整性校验，发布来源依赖 GitHub HTTPS 与仓库发布权限；独立签名验证、服务管理和真实 Debian/Alpine 安装验收尚未完成。
