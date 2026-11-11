# Codex Mobile Bridge Server

为[桌面网关](https://github.com/mc373906408/codex-mobile-bridge)和[手机 App](https://github.com/mc373906408/codex-mobile-bridge-app)提供云端中转、账号管理和可选的推送服务。
它不是聊天客户端；手机直接连接桌面网关时，不需要部署这个服务。

## 选择部署模式

| 模式 | 适合谁 | 账号方式 |
| --- | --- | --- |
| `selfhost` | 自己或小团队使用，推荐从这里开始 | 管理员创建用户名和密码，不检查托管服务天数 |
| `hosted` | 向用户提供托管服务 | 先验证邮箱验证码，再设置密码注册；支持服务天数兑换，需要配置 SMTP |

两种模式使用同一套服务。第三方推送默认关闭，配置好对应凭据和客户端后再启用。

中继采用 [v2 持久加密会话](protocol/relay-v2.md)，手机、桌面与服务器须同步升级。此次升级保留当前数据库、账号和配对密钥；客户端不定时查询服务资格，托管资格及连接到期由服务器独立执行。

可选 [EasyTier P2P](docs/p2p.md) 让已授权手机与桌面直接传输，打洞失败继续使用中继。引导 RPC 可复用 HTTPS 443，已有直连在云暂时不可达时继续工作；收到明确到期、撤销或禁用结果后关闭。

可选 [Web 入口](docs/web-client.md) 在独立 HTTPS 18787 提供普通云账号登录和电脑选择，直接透传在线桌面的现有网站。无需逐浏览器批准，网页更新只需更新桌面端；Web 内容由云端代理处理，手机加密中继保持独立。

## 快速部署

准备 Linux 服务器、Docker Engine、Compose v2 和 Python 3.11+，并将域名指向服务器。
以下命令仅用于全新部署，请把 `relay.example.com` 换成自己的域名：

```sh
git clone https://github.com/mc373906408/codex-mobile-bridge-server.git
cd codex-mobile-bridge-server
python3 -B scripts/init_deployment.py --domain relay.example.com
docker compose up -d --build --wait
```

1. 打开 `https://relay.example.com/admin`，使用 `admin/admin` 首次登录。
2. 先修改为新的强密码并重新登录，再进入管理台。也可在初始化时使用 `--admin-login operator`，由脚本生成独立初始密码并保存到 `.local/secrets/admin_password`。
3. 自部署模式下，在后台创建用户，再让桌面端和 App 使用同一服务器地址登录。

首次登录需修改管理员密码。后台只允许管理员登录，普通用户通过客户端使用服务。

管理台支持搜索和分页浏览全部用户，在用户行选择“管理设备与会话”后查看其桌面、手机、绑定及登录记录，并逐项撤销。设备批准仍在桌面完成；管理操作和授权变更的审计包含受影响账号与前后状态。

托管模式下，已验证且未停用的普通用户行提供“充值天数”：默认 30 天，可增加 1–3650 天并填写备注。未到期从原有效期顺延，已过期或未激活从服务器当前时间起算。充值回执、有效期和操作审计一起保存；响应未确认时使用“核实本次充值”继续同一请求，不会重复加天数。自部署模式不提供充值。

首页、登录、首次改密、密码重置及管理台沿用 18787 云入口的蓝色控件与无衬线字体，支持系统深浅色和手机布局。

初始化脚本不会覆盖已有配置。升级或恢复时不要重新初始化，先按[部署文档](docs/deployment.md)备份。

### 查看服务状态

```sh
docker compose ps
docker compose exec -T server /bridge-server healthcheck
```

## 托管模式与兑换码

使用 `hosted` 前，先配置 SMTP 邮件服务，再按[部署文档](docs/deployment.md#2-hosted-and-push-opt-ins)启用对应的 Compose 配置。
管理员负责生成、导出和撤销兑换码；用户在桌面端兑换服务天数，不会因此获得设备配对权限。

`hosted` 管理后台还提供[注册体验活动](docs/deployment.md#registration-trial-campaigns-hosted-only)。升级会预建“邮箱注册 7 天体验”草稿；管理员保存并启用后，符合活动时间和名额的新账号在完成邮箱验证注册时自动获赠。草稿、待开始或进行中的活动同时只能有一期，启用后规则固定，结束不可恢复；已有账号不补发，已发放的有效期不会因活动结束缩短，兑换码可继续顺延。默认草稿不发放服务。

Android/iOS 云推送是可选功能，需要匹配的服务端凭据、客户端配置和用户同意；配置完成不代表通知已经送达。

## 安全与版本提醒

- 使用 HTTPS，不要将 PostgreSQL 或内部后端端口暴露到公网。
- 建议通过 VPN 或 IP 白名单限制管理入口；只更换端口不能代替访问控制。
- 不要上传 `.local/`、密码、推送密钥或数据库备份；备份数据库时也要单独保留 `data_key`。
- 项目仍在开发。真实设备兼容、推送送达和正式发布需要分别验证，不能只以构建成功为准。

## 更多说明

- [部署、邮件配置与备份恢复](docs/deployment.md)
- [实现状态与验证范围](docs/implementation-status.md)
- [全局架构、授权逻辑与范围审查](.agents/notes/implemented/architecture/2026-11-03-server-architecture-and-scope-audit.md)

许可证：[GNU AGPLv3（仅第 3 版，AGPL-3.0-only）](LICENSE)，版权与适用范围见 [NOTICE](NOTICE)。第三方组件保留各自许可证。

修改本服务并提供网络访问时，应按 AGPLv3 第 13 节向远程用户提供相应版本的对应源码，并将网页页脚的源码链接指向该版本。AGPLv3 允许商业使用；此前按 MIT 发布的版本仍保留原有授权。
