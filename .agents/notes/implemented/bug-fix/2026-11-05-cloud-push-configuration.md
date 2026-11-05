# Agent Note: 云通知默认启用所需的友盟投递配置

Status: implemented

## Problem

用户报告 App 没有收到通知，并要求云登录后默认开启通知、没有开关。实际网关在线且生成事件，线上能力却返回 `push.umeng=false`，当前服务容器没有配置 AppKey 和服务端签名秘密。手机云通知状态也未初始化，客户端修复需和服务器配置分别验证。

## Decision

使用本机已有、与测试手机一致的 U-Push AppKey 和服务端 App Master Secret，为当前部署注入友盟参数。秘密只经固定主机公钥的 SSH 通道写入服务器私有目录，由 Compose secret 只读挂载，不进入源码、日志、Android 包或命令行进程参数。

沿用当前服务镜像和数据库，只重新创建 server 容器；HTTPS 和 PostgreSQL 保持运行。当前安装目标是 debug 测试手机，明确设置 `UMENG_PRODUCTION=false`。以原配置私有副本提供失败恢复，公网健康和能力验证失败则恢复原配置。生产发布模式需配套正式签名客户端另行验证，不能把测试模式当成生产验收。

## 记录归属

检索通知和部署活跃记录后，[当前基线部署](../../implemented/process/2026-11-04-current-schema-server-deployment.md)部分重叠，持有数据库、镜像和未配置推送的原始状态；[推送撤回隔离](../../implemented/bug-fix/2026-11-04-push-withdrawal-independent-of-desktop-access.md)继续持有权限缩减语义。本次不替换二进制、不改变公开格式或数据库。没有完全吸收记录或过时提案。

## Alternatives considered

- 只修客户端默认状态能恢复 App 前台读取，但后台厂商投递仍因服务端未配置而不可用，不能完成此次通知修复。
- 把 Master Secret 放在 Compose 环境值里更直接，但会扩大配置和容器环境的秘密暴露面；使用现有 `_FILE` 加载与只读 secret 挂载。

## Testing

2026-11-05 部署后 readyz 与受信 TLS 正常，capabilities 返回 `umeng=true` 且 AppKey 与测试手机构建输入一致。数据库和 HTTPS 容器身份不变，服务继续使用 `bridge-server:redemption-batches-3f6d61f36b7a8cc0`。当前 Compose SHA256 为 `43235b49f7da0977d54cc03d8801fec5e7faea515c47387b007f3693cba61ac5`；私有参数目录为部署根下 `artifacts/cloud-push-20261105/private/`，入口仍使用 `artifacts/redemption-batches-20261104/private/compose.current.json`。

手机覆盖更新和前台恢复后默认初始化云通知，原账号及已授权 Desktop 保留；实际绑定登记为 enabled/consent true。App 切到后台后提交一条通用内容的有效加密预览，服务端任务尝试 1 次、厂商结果 accepted、最终状态 acked；手机相同 notification ID 的原生回执已保存并确认，系统通知栏存在对应 `bridge.events.v1` 通知。没有用真实聊天内容作测试，没有修改账号、服务有效期或设备权限。

本机部署和只读查询结果在 `.tmp/cloud-notification-repair-20261105/`，手机安装/回执证据由 App 仓库同名临时目录持有。没有新增服务器业务代码或数据库迁移；配置验收不替代全套 Go 回归。正式签名发布、iOS、强制停止及其他厂商设备尚未验证。

## Consequences

当前测试手机具备已验证的后台密文推送和回执链路，App 默认通知状态能够登记实际端点。代价是部署开始持有服务器端友盟签名秘密，需继续采用私有文件及限定挂载；配置更新短暂重建 server 并使中继重连。当前设置为测试发送模式，正式发布需要配套切换和验证，不能外推到其他设备与平台。
