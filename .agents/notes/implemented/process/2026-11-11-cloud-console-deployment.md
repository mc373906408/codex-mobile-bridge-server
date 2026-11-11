# Agent Note: 云管理台、直接充值与购买二维码部署

Status: implemented

## Problem

用户授权部署本轮 server 功能并上传代码。当前生产仍为 2026-11-10 的 HTTP 转发版本；本轮增加 006 充值回执迁移、管理台样式和公开二维码图片接口，需保留账号、权益和设备数据。

## Decision

server 功能提交 `597c8b81d31f7b4e5185adf2e13c8ef41473a65e` 与桌面提交 `62208392` 均推送到各自 origin/main，服务端从确定的提交归档构建 Linux amd64 二进制。沿用固定 SSH 主机密钥、现有 Compose、数据库／HTTPS／P2P 容器与证书，只更新 server 镜像。

切换前生成加密 PostgreSQL 备份并验证目录，停服后再备份并记录保护表摘要。候选二进制执行 migrate，旧表数据不变，新表为空且仅新增 006 迁移，再启动候选镜像。健康、公网 TLS、页面资源、二维码下发、未认证／跨 Origin 拒绝与实际二进制摘要均单独核验。

旧二进制拒绝未知迁移名，因此保留“旧生产源码 + 同一 006 迁移”的兼容回退镜像。故障时只回退服务进程，不删除迁移记录或回滚数据库，以免丢失部署后的充值。兼容镜像来源和补丁单独留在私有交付归档中。

## 记录归属

检索现有部署和 schema 决策，与[上一轮 HTTP 转发部署](../../implemented/architecture/2026-11-10-http-forwarding-without-route-policy.md)部分重叠，沿用同一服务切换与备份边界；本轮新增迁移，不能直接复用其“旧镜像原样回滚”的条件。功能归属为[充值与控制台](../../implemented/feature/2026-11-11-admin-topups-and-console-style.md)和[服务端购买图片](../../implemented/feature/2026-11-11-sponsorship-qr-endpoint.md)。

## Alternatives considered

直接使用旧镜像回退最简单，但旧迁移器拒绝 006；删除迁移记录或恢复整库可能破坏新充值及后续升级，所以准备带同一增量迁移的兼容回退程序。

## Testing

部署前隔离 PostgreSQL 的 140 项 Go 测试事件全部通过，无失败或跳过；19 项 Node UI、vet 通过。候选和兼容回退版本在生产加密备份恢复出的临时数据库各运行两次 migrate，23 张既有业务表、517 条记录摘要不变，新增 006 只执行一次且充值回执为空。

2026-11-11 生产更新为 `bridge-server:cloud-console-20261111-597c8b8`。运行二进制 SHA-256 为 `14aa9ec0a7c24790005f3f619ad06a7d2877c428643db7fbf5e69f4e7ab75882`，与提交归档构建一致。首次切换后验证脚本错误期待 18787 的未知购买图路径返回 404，实际上该入口先校验登录并返回 401；已自动切回兼容版本，健康恢复。只将验收预期改为现有正确的 401 后再次切换，第二次用时 15.5 秒；没有修改服务端权限行为，也没有回滚数据库。

最终 server 健康；保留 24 张业务表（含新增空回执表）、517 条记录，原账号、有效期、设备、绑定、兑换与活动摘要一致。database、HTTPS、P2P 容器 ID／镜像／启动时间不变。远端核对 10 个静态资源，公网独立核对 8787／18787 TLS 和 9 个资源；未认证充值为 401，跨 Origin Web 请求为 403。公开二维码 SHA-256 为 `8eb63e89a18a91bcc3e5e69d8288f9fc6adf80492b1423fbac0522789bfb0aaf`，Windows 新安装包中的网关也通过公网 HTTPS 成功取得同一图片。

最后加密备份为远端 release 下 `backup/before-retry-stopped.dump.enc`，107088 字节、161 个归档条目，SHA-256 为 `39bbf07e99a03090e81829aa302a7e5f8b707ac513a7102e1b7bd5fb672958e0`。脚本、源码清单、候选／回退程序和结果保存在本地忽略目录 `.tmp/cloud-console-deploy-20261111/` 与远端 `/opt/codex-mobile-bridge-deploy-01a18ba6/artifacts/cloud-console-20261111-597c8b8/`，备份密钥仅在受限 private 目录。

## Consequences

新版管理台、充值接口与购买二维码已经上线，两仓代码可从 origin/main 取得。切换短暂断开云连接，客户端自行重连；没有给真实账号充值或修改设备权限，生产支付／真实充值效果未作为验收动作执行。备份和恢复材料含私有数据，仅保存在受限部署目录，不提交 Git。后续回退必须使用兼容镜像，不能直接启动不识别 006 的旧程序。
