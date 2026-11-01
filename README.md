# Codex Mobile Bridge Server

为[桌面网关](https://github.com/mc373906408/codex-mobile-bridge)和[手机 App](https://github.com/mc373906408/codex-mobile-bridge-app)提供云端中转、账号管理和可选的推送服务。
它不是聊天客户端；手机直接连接桌面网关时，不需要部署这个服务。

## 选择部署模式

| 模式 | 适合谁 | 账号方式 |
| --- | --- | --- |
| `selfhost` | 自己或小团队使用，推荐从这里开始 | 管理员创建用户名和密码，不检查托管服务天数 |
| `hosted` | 向用户提供托管服务 | 邮箱注册和验证、服务天数兑换，需要配置 SMTP |

两种模式使用同一套服务。第三方推送默认关闭，配置好对应凭据和客户端后再启用。

## 快速部署

准备 Linux 服务器、Docker Engine、Compose v2 和 Python 3.11+，并将域名指向服务器。
以下命令仅用于全新部署，请把 `relay.example.com` 换成自己的域名：

```sh
git clone https://github.com/mc373906408/codex-mobile-bridge-server.git
cd codex-mobile-bridge-server
python3 -B scripts/init_deployment.py --domain relay.example.com --admin-login operator
docker compose up -d --build --wait
```

1. 打开 `https://relay.example.com/admin`，使用管理员账号 `operator` 登录。
2. 初始密码由脚本随机生成，保存在 `.local/secrets/admin_password`，请在服务器本地读取并妥善保管。
3. 自部署模式下，在后台创建用户，再让桌面端和 App 使用同一服务器地址登录、配对。

首次登录需修改管理员密码。后台只允许管理员登录，普通用户通过客户端使用服务。

初始化脚本不会覆盖已有配置。升级或恢复时不要重新初始化，先按[部署文档](docs/deployment.md)备份。

### 查看服务状态

```sh
docker compose ps
docker compose exec -T server /bridge-server healthcheck
```

## 托管模式与兑换码

使用 `hosted` 前，先配置 SMTP 邮件服务，再按[部署文档](docs/deployment.md#2-hosted-and-push-opt-ins)启用对应的 Compose 配置。
管理员负责生成、导出和撤销兑换码；用户在桌面端兑换服务天数，不会因此获得设备配对权限。

Android/iOS 云推送是可选功能，需要匹配的服务端凭据、客户端配置和用户同意；配置完成不代表通知已经送达。

## 安全与版本提醒

- 使用 HTTPS，不要将 PostgreSQL 或内部后端端口暴露到公网。
- 建议通过 VPN 或 IP 白名单限制管理入口；只更换端口不能代替访问控制。
- 不要上传 `.local/`、密码、推送密钥或数据库备份；备份数据库时也要单独保留 `data_key`。
- 项目仍在开发。真实设备兼容、推送送达和正式发布需要分别验证，不能只以构建成功为准。

## 更多说明

- [部署、邮件配置与备份恢复](docs/deployment.md)
- [实现状态与验证范围](docs/implementation-status.md)

许可证：[MIT](LICENSE)。
