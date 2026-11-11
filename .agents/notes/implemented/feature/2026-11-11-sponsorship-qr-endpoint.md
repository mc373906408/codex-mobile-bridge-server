# Agent Note: 官方服务下发购买二维码

Status: implemented

## Problem

用户明确要求购买二维码由服务端下发，桌面本地生成并内置购买链接会使更换购买渠道依赖客户端升级。

## Decision

官方 hosted 服务在 `GET /v1/sponsorship` 公开返回仅含 PNG data URL 的 JSON。图片作为 `internal/api/web/sponsorship.png` 随服务器嵌入；当前资产解码为用户提供的闲鱼商品链接，更新图片后部署服务器即可更新所有新版桌面的弹窗。响应 no-store，不要求登录或服务资格。

仅 hosted 且精确 Origin 为 `https://bridge.waken.cloudns.be:8787` 提供官方购买图；selfhost 和其他 hosted Origin 返回 404。该接口只读取固定嵌入资产，不接受图片路径、外部 URL 或账户凭据，不提供支付或充值能力。桌面每次打开购买弹窗读取图片，失败不回退本地购买链接。

## 记录归属

检索 sponsorship、二维码、闲鱼，仅命中现有配对说明；不存在购买图下发记录。与[直接充值](../../implemented/feature/2026-11-11-admin-topups-and-console-style.md)面向同一云服务但职责不同：图片不改变账号有效期，管理员充值权限与审计仍由原篇持有。

## Alternatives considered

桌面内置链接能离线显示二维码，但用户要求服务端控制此内容，因此移到服务器。动态引入二维码生成依赖能接受任意购买 URL，但当前只有一个固定商品，采用服务器静态 PNG 即可满足下发和更新，不增加运行依赖。

## Testing

Go 接口测试通过，覆盖官方 hosted 无登录返回 PNG、no-store、JSON 仅含 image、图片与嵌入文件一致，以及 selfhost／其他 hosted Origin 返回 404。`go vet ./...` 通过。生成资产时实际解码 320×320 PNG，确认完整目的地为 `https://m.tb.cn/h.jjD7PcA?tk=gr8Q6d3Taty`。

隔离 Go HTTPS 测试服务使用真实生产接口和临时 CA，桌面 Python Controller 校验证书后读取并确认图片字节一致。测试入口只接受 capabilities／sponsorship 且拒绝 Cookie／Authorization，未访问生产账号或修改线上服务。桌面另有无本地回退、类型／大小／尺寸拒绝及重新读取服务端图片的回归。

Windows 打包网关随后通过同一真实 HTTPS 入口读取图片并比对资产 SHA-256，也读到服务端公布的网页 origin。Linux amd64 服务端编译通过，产物位于忽略目录 `.tmp/sponsor-delivery-20261111/`。

## Consequences

购买目的地归服务端控制，桌面不再持有商品链接；代价是每次打开需要一次公共 HTTPS 请求。旧服务返回 404 时新版桌面显示读取失败。更新图片须核验扫码目的地并重新构建服务端，不需重新打包桌面。接口已按用户后续授权[部署到生产](../process/2026-11-11-cloud-console-deployment.md)，公网与 Windows 打包网关实际读取的图片摘要均已核验。
