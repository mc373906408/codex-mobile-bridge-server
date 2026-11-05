# Agent Note: 当前数据库基线服务端部署

Status: implemented

## Problem

用户要求部署已经完成验证的服务端改动。当前版本删除旧开发数据库升级路径，直接覆盖旧库无法通过 `001_current.sql` 初始化；管理与审计修改还需要确认部署二进制与本地验收源码一致。

## Decision

本地验收清单核对 72 个构建输入，Go 1.27.3 生成禁用 CGO 的 Linux amd64 二进制，发布归档包含 93 个源码/文档文件。原部署 SSH 的 Ed25519 指纹核对后执行发布，运行镜像为 `bridge-server:current-aed977f0cef2e2fb`，站点为 `https://bridge.waken.cloudns.be:8787`。

当前 `001_current.sql` 在独立空库 `bridge_current_20261104` 初始化，两次 migrate 通过后切换服务端。用户补充“不用保留旧版本和旧库”，因此不创建旧版备份或回退发布；公开验收后删除旧 `bridge` 库、10 个历史服务镜像、9 个旧发布目录及旧数据库备份。部署目录内旧源码由当前发布源码替换，只保留当前镜像。原 PostgreSQL 与 HTTPS 容器、证书、SMTP 及 hosted 模式继续使用。新库只包含待初始化的 admin 管理员，密码仍为 admin，部署过程未替用户改密。

运行配置收敛为私有 `compose.current.json`，部署根目录 `compose.sh` 和 `source/compose.yaml` 均指向当前配置，避免后续启动又叠加已删除的旧版本配置。数据库初始化 SQL 使用新库名，当前数据库 URL 和管理员默认配置同步。证书续期路径继续有效。

只切换服务端，不重新安装客户端或改变 DNS。新库不迁移旧账号/设备，符合用户“尚未上线，历史数据库和已有绑定无需保留”的决定。公开验证只读检查 TLS、健康、管理页和未认证接口拒绝；完整改密测试只在独立合成验收库进行。

## 记录归属

本仓检索后没有既有部署过程笔记。[当前账号/绑定结构](../simplification/2026-11-04-prerelease-account-and-binding-schema.md)持有新库决定，[管理与审计](../bug-fix/2026-11-04-admin-management-and-audit.md)持有 admin/admin 与管理员权限。两者部分重叠，以链接保留；[实施状态](../../../../docs/implementation-status.md)保存本次部署结果与历史记录，不把旧验证外推到新版本。

## Alternatives considered

- 原提案保留旧镜像与数据库以便回退；用户明确不需要保留，已拒绝该方案。不在旧服务运行期间原地清空数据库，以减少不可用时间；先验证新库再切换和删除旧库。
- 对旧库补迁移可保留开发账号，但与已确认的删除旧兼容路径决定冲突，因此不重新引入。

## Testing

上传后归档/二进制哈希通过，运行容器中二进制 SHA256 为 `3a6010aec363594527d5440c4549e25043d00b73c564b4ad983128f9e8666dda`，与本地构建一致。生产同镜像在隔离库验证 admin/admin → 仅 600 秒 setup token → 强制改密 → 重新登录 → 管理员搜索/分页及目标账号设备/绑定/会话查询；重启后新密码有效，admin/admin 不再生效。合成库与容器已删除，SMTP/推送在该测试中禁用。

公网 13 项检查通过：受信 TLS、HSTS、readyz、capability、管理入口、未认证管理员/目录接口返回 401、旧验证/手动配对路由返回 404、四个静态资源逐字节匹配发布包。清理后重复公网验证和服务健康通过。数据库目录只剩当前业务库与 PostgreSQL 系统库，Bridge 服务镜像只剩当前版本。

第一次预检在创建空新库后因 umask 把挂载秘密文件收窄为 0400 而停止，旧服务未切换。复核现场确认新库尚无表后修正为私有父目录内的 0444 只读挂载文件，再完成初始化、隔离验收与切换；没有盲目重放初始化或删除旧库。证据在 `D:/github/codex-mobile-bridge-server/.tmp/server-deployment-20261104/`，当前发布及非秘密结果在主机 `/opt/codex-mobile-bridge-deploy-01a18ba6/artifacts/current-schema-20261104-aed977f0/`。

## Consequences

新库需要重新注册账号和登记客户端，当前 App/桌面与服务端需配套使用。旧中继连接在 server 重建时断开，按用户选择不提供旧版/旧库回退；首次 admin/admin 认领窗口按用户选择保留。真实邮件和厂商送达未测试，当前配置未启用 Umeng/APNs。部署只更新服务端，没有安装 App/桌面或执行 Git 提交/推送。

后续[中继关闭隔离修复](../bug-fix/2026-11-04-relay-channel-close-isolation.md)在同一当前业务库上更新镜像，保留新注册账号与绑定，没有重复本篇的新库初始化。该更新清除旧镜像/二进制，但保留本发布私有目录中仍由当前服务挂载的管理员配置和数据库 URL。

[云推送配置](../bug-fix/2026-11-05-cloud-push-configuration.md)随后为既有测试手机启用友盟服务端参数并完成后台送达及回执验收，取代本篇部署时“Umeng 未配置”的运行事实；没有再次初始化数据库。
