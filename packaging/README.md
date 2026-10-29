# 安装与服务管理模板（未投入使用）

当前 Agent 尚无可运行的远程控制循环，因此 **不提供会实际安装/启动守护进程的 install.sh、systemd/OpenRC 服务文件**，以免用户误部署空壳服务。

M0 交付时需提供：

- `systemd/viorka-agent.service`：`StateDirectory`、配置文件权限、重启限速、安全限制。
- `openrc/viorka-agent`：适配 Alpine 的 `supervise-daemon`。
- 非 systemd/OpenRC 环境：foreground run + supervisord/s6 可选模板。
- 交叉编译发布、文件哈希及签名验证、二进制来源固定。
- `install.sh` 明示下载地址、版本、哈希检查、幂等安装及卸载流程。

不要通过未经校验的 `curl | sh` 分发高权限二进制。
