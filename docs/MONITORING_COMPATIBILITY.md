# 监控协议兼容性实测

日期：2026-11-03。此记录针对开发测试构建，不代表发布或生产数据库升级承诺。

## 被测版本

| 标识 | 来源 | 本地二进制 SHA-256（macOS 构建） |
| --- | --- | --- |
| Panel 旧 | 提交 `fedad58072b291eed69dce4a19ac0620ce1451bc`，本地无 Panel tag | `ed27c6158d0e80ace6007359a729345436c723e63255baa9481b523ac4a7f52f` |
| Agent 旧 | tag `v0.1.0-bate.4`，提交 `a64d0c95cfcf4b214a0bfc61d6986a0464d4663e` | `d9168db7608a85b86643a305948934e1585ccbf9f65836e8a5df190f74c2159a` |
| Panel 新 | 基于上述 Panel 提交的当前未提交监控改造工作区 | `0611af5115d715d01cfbea70bcd4b014874d8d2bed54583aa8e342183a2821bd` |
| Agent 新 | 基于 `9f83e5d165b33432b3275f23bd4e6b1e0a4e9543` 的当前未提交监控改造工作区 | `207db9d6352290a6805f29987d4b8abf75cf088b215c26266f8df3f10f79b3ea` |

新协议 Schema SHA-256：`b08d15df54483e9e8223dd0a0a1e2c51ff8afb8e43ae5c9ae12548df5772e6fc`。最终提交后需要补入两个实际提交号；上述二进制摘要限定本次实验，不能用后续工作区改动替换其版本身份。

旧代码通过 `git archive` 提取到 `/tmp/viorka-compat.LH8x1Fok/{panel,agent}`，未切换或重置原工作区。各组合使用独立临时数据库、临时身份、回环TLS、显式CA和stdin Token，没有旧历史导入或用户数据库操作。

## 组合与证据

| Panel | Agent | 启用开关 | 结果 |
| --- | --- | --- | --- |
| 旧 | 旧 | 均关闭 | 通过；旧 WSS 指标 |
| 旧 | 新 | Agent v3 开启 | 通过；首次连接保持旧 WSS 指标，未取得v3快照 |
| 新 | 旧 | Panel v3 开启 | 通过；旧 WSS 指标，旧 Agent 严格解析与任务正常 |
| 新 | 新 | 双方 v3 开启 | 通过；v3 HTTP快照、Panel重启保持run、Agent重启新run |

四项均实际完成：注册、在线资产、指标、node.inspect、任务列表/汇总、幂等重投、Panel kill/restart、Agent停止/restart、身份吊销后Agent非零退出，以及独立哨兵进程存活。macOS指标以unsupported_platform表达，不能据此称Linux指标已测；Linux结果另见实施记录。

复现入口在Panel `scripts/e2e_agent_connection.py`。可传 `--panel-binary` / `--agent-binary` 选择已构建的绝对可执行路径，省略则构建源码。`--agent-telemetry-v3`、`--panel-telemetry-v3` 独立控制开关；只有新新组合传 `--expect-telemetry-v3`，它还验证Panel重启后run不变、Agent重启后run更新。不要给严格解析旧Agent配置添加新字段。

本次日志位于 `/tmp/viorka-compat.LH8x1Fok/{old-old,old-new,new-old,new-new}.log`。测试输出二进制摘要和最终结果，不输出凭证。

## 范围限制

- 旧新组合通过不等于已抓包证明所有未知POST均不存在；未协商禁止发送的细节仍依赖Client协商测试与实现审查。
- 已协商v3后关闭/再次开启的行为已由真实TLS/WSS fixture验证；实际已发布旧Panel二进制接替新Panel、复用旧身份的完整降级场景尚未执行。旧Panel不得直接打开新监控数据库。
- 新设置文件与旧设置文件的兼容、独立revision不能仅凭这张矩阵推定全部通过，需保持单元/合约与持久化恢复测试证据。
- Ubuntu amd64实测与24小时观测不覆盖Alpine、arm64或NAT72小时。最终协议发布与双仓提交/推送尚未完成。
