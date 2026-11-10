# 云账号 Web 访问

入口为 `https://bridge.waken.cloudns.be:18787`。先在桌面云服务中开启“允许网页访问本电脑”，浏览器再使用同一云账号登录并选择在线电脑，无需逐个批准浏览器。桌面网页许可默认关闭，独立于手机权限；关闭会断开网页通道。托管账号仍须有有效服务天数；自部署按现有账号规则使用。

云端只保存登录和电脑列表页面。`/d/{gateway_id}/` 的 HTML、脚本、样式、字体、预览和 API 均由所选桌面当前的私有 HTTP 服务返回。更新桌面网页后刷新浏览器即可看到新版本；无需向云端导出聊天网页。电脑离线时，其网站不可用，列表显示离线；桌面在线但网页通道未就绪时提示检查桌面网页访问设置或版本，不将通道离线推断为用户拒绝。

## 连接与信任

Web 使用 HTTPS 和单独的 `bridge-web-proxy-v1` 出站 WSS，云服务会处理聊天与文件明文。它不创建手机安装记录、不授予手机绑定权限。手机保持原 8787 入口、Noise v2、桌面批准和 P2P。

浏览器 Cookie 为 HttpOnly、Secure、SameSite=Strict，只有 `web_client` 用途会话可用。Cookie 不按端口隔离，服务端使用独立 Cookie 名称、精确 Host、Origin、CSRF 和用途检查。管理员 Cookie、普通 Web Cookie、原生 Bearer 互不替代。转发层不维护业务接口白名单或黑名单，桌面 HTTP 处理器校验各接口的权限和能力；目标限定为所选桌面的相对路径。转发不携带浏览器 Cookie、Authorization 或桌面登录凭据。

Web 会话最多 12 小时，退出、禁用、撤销及服务到期取消相关请求。桌面切换云账号或关闭时销毁内部 Web 会话。终端使用既有 HTTP 轮询；聊天保留 SSE。中断的写入不自动重放。单次文件/上传上限 20 MiB，每浏览器会话 8 个请求、每桌面 64 个请求；超过 1 MiB 的传输每浏览器 1 个、每桌面 2 个，按 `BRIDGE_BYTES_PER_SECOND` 对 Web 通道节流。

沙箱预览仍使用固定 HTML 壳和 CSP。图表壳内只内嵌桌面白名单中的两个固定脚本，并赋予随机 nonce，避免依赖沙箱子资源无法携带的 Strict Cookie。草稿和偏好按云账号、电脑分开存储。

## 标准部署

先升级服务端与桌面端。数据库迁移 `004_web_client_sessions.sql` 只增加会话用途，不删除账号或设备。`BRIDGE_WEB_ORIGIN` 默认为空，即关闭此入口。

标准 443 部署可追加以下配置，保留原先 hosted/push 等 overlay：

```sh
docker compose -f compose.yaml -f deploy/compose.web.yaml config --quiet
docker compose -f compose.yaml -f deploy/compose.web.yaml up -d --build --wait
```

Caddy 在同一域名上额外监听 18787 并代理到同一 Go 服务，服务按 Host 分流。需要放通 TCP 18787。标准模板继续通过原 443 的 ACME TLS 验证签发和续期。

## 现有 8787、定制证书部署

`bridge.waken.cloudns.be` 使用现有 8787 映射和证书时，**不要用标准模板覆盖正在使用的 Caddyfile**。追加 `deploy/compose.web-existing.yaml`，保留所有既有 overlay、证书挂载与续期任务；在当前 Caddyfile 增加独立站点：

```caddyfile
https://bridge.waken.cloudns.be:18787 {
    # 在这里复用当前 8787 站点的 tls 配置及其容器内证书路径。
    reverse_proxy server:8080 {
        flush_interval -1
    }
}
```

原 8787→容器 443 映射保留，新端口使用 18787→18787。检查合并 Compose 后，先 `caddy validate --config /etc/caddy/Caddyfile` 再重载/重建服务。证书仍使用同一域名；不要重新初始化数据库或替换 data_key。

上线前备份数据库、镜像、Compose、实际 Caddyfile 和证书配置。升级后确认：8787 `/v1/capabilities`、管理员页面、手机连接仍可用；能力返回 `web_client.enabled=true`；18787 证书有效；普通用户只能列出自己的电脑，退出后旧页面请求失败；桌面网页更新后版本地址变化。

回退时先清空 `BRIDGE_WEB_ORIGIN` 并移除新增站点/端口，恢复旧镜像。新增数据库列可保留，已有会话与手机权限不依赖删除该列。这里的模板不是生产已部署的证明。

## 当前部署验证

生产 18787 已于 2026-11-07 启用，2026-11-08 复核外网 HTTPS、原 8787 能力接口、服务端与数据库健康状态，以及原账号和设备记录保留。本机 Windows 网关更新后，用户在 Edge 使用现有云账号登录，已成功打开在线电脑的原桌面网站、真实聊天列表及当前聊天内容，无需逐浏览器批准。

2026-11-10 已部署 `http-forwarding-20261110-d35a7a5`，移除转发层方法清单并支持非 GET/HEAD 正文和 HEAD 响应语义。公网两个入口的证书、健康和登录页验证通过；未登录的 PATCH/HEAD 桌面请求返回 401，跨 Origin 仍返回 403。数据库结构不变，账号/设备等持久记录摘要一致；原数据库、HTTPS、P2P 容器保持运行，桌面和 Lenovo 手机已重新连接并读取聊天列表。详情见[通用转发记录](../.agents/notes/implemented/architecture/2026-11-10-http-forwarding-without-route-policy.md)。

2026-11-08 入口页改为与桌面原版网页一致的字体、蓝色按钮、圆角控件和深浅色风格；登录后仅显示账号与电脑卡片，离线和空列表有明确提示。独立入口样式不影响管理后台。改版已部署，完成实际账号、原版网页连接及桌面/390px 手机宽度的浏览器验证。

未登录访问电脑目录与桌面 API 返回 401，跨 8787 Origin 请求 Web 会话接口返回 403。生产写入、审批、上传下载和手机真机连接未在此轮实际操作中验证；相应协议与合成场景测试范围见决策记录。旧版桌面必须更新后才能提供此入口。

## 开发验证

Go 集成测试必须设置指向隔离 PostgreSQL 的 `BRIDGE_TEST_DATABASE_URL`，否则数据库用例会跳过。运行 `go test ./...` 和 `go vet ./...`。

端到端夹具：在服务端运行 `go run ./internal/webproxy/testdata/fixture.go <私有临时目录>/fixture.json`，它绑定回环 18878/18879，创建独立测试 schema 和两个合成桌面；在桌面仓库运行 `python -B tests/browser/cloud-web-fixture.py <同一文件>`，再运行 `playwright test -c tests/browser/cloud-web.config.cjs`。需要 Chromium/WebKit；本机可设置 `CMB_CHROMIUM_CHANNEL=msedge` 使用已安装 Edge。夹具含临时设备令牌，禁止提交或上传。运行结束后停止夹具并清理其测试 schema/数据库。

线协议见 [account-Web 协议](../protocol/web-proxy-v1.md)。
