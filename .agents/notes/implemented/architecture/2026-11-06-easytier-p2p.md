# Agent Note: P2P 能力发现与服务端资格结果

Status: implemented

## Problem

P2P 可以降低业务中继流量，但关闭云 WebSocket 不再意味着关闭客户端之间的数据通道。必须让客户端区分明确权限结论与网络故障。

## Decision

增加可选 EasyTier 能力及自有引导地址，保留 relay v2 兼容。引导节点只处理发现和打洞 RPC，禁止业务转发，失败仍使用 Bridge 中继。

目录与连接校验返回有明确作用范围的授权结论，复用账号、设备、绑定和托管资格检查，按账号合并资格读取。网络故障不作为撤销；收到明确失效结果才关闭已有 P2P。新 P2P 会话要求在线校验。不开新高频轮询，不增加客户端短期授权票据。

用户补充在线时由服务器主动发送断开信令：支持 `access-v1` 的 WSS 控制连接接收作用范围明确的授权变化通知，然后读取当前授权，避免旧通知覆盖续期。普通数据库错误和网络断开不发送权限失效结论。

独立引导端口受公网网络策略阻断时，`/v1/p2p/bootstrap` 复用既有 HTTPS origin，以有界 WebSocket/TCP 管道连接部署者固定的内部节点。客户端无法指定目的地。空中继白名单及 `disable_relay_data` 由上游 TOML 执行。实际部署保留原 8787 HTTPS 入口、数据库、凭据挂载，关闭额外公网端口。

## 记录归属

[持久中继 v2](../../implemented/architecture/2026-11-05-persistent-relay-v2.md)及[服务端审查](../../implemented/architecture/2026-11-03-server-architecture-and-scope-audit.md)部分重叠，保留中继强制断开及服务端资格判断；本篇新增 P2P 延迟生效语义。检索未发现其他 EasyTier/P2P 提案，无完全重复或需 reject 的提案。

## Alternatives considered

- 让 EasyTier 节点承担业务回退可以复用其机制，但流量不易核算并重复现有中继。
- 短期票据和周期强制检查有明确撤销上限，但用户明确接受下次成功校验时生效。

## Testing

121 项 Go/PostgreSQL 测试通过，无数据库跳过，包含 WSS 固定目标代理及浏览器拒绝测试。真实三端夹具验证 P2P 绕过中继、云失败持续通信及恢复后撤销关闭。服务端部署镜像 `bridge-server:p2p-wss-20261106`，二进制 SHA256 `f9c3916a2bc26758cbdc34147ee62a79e1dcbd82736d66b3e1893e78543abb2b`；引导使用上游 2.6.4 固定二进制 `35b854e0c5853c02733870488c977200f6aa06d09c08d5dc7f9f9e150e7be8d5`。数据库备份和前一 Compose 配置保存在部署主机 `artifacts/p2p-20261106/`，现有秘密挂载和网络配置核对保留。公网 HTTPS 能力和 WSS 证书/升级通过。Windows 环境未执行 Go race（未配置 CGO）；不得声称 race 通过。

## Consequences

P2P 业务不再占云端中继带宽，引导 RPC 复用现有 TLS 入口；保留发现与授权控制连接的少量开销。已有 P2P 在服务器不可达时可能持续，没有远程撤销的固定时限。引导节点和固定目标代理需要独立监控并发和可用性；服务器不因 P2P 成功而删除原有中继兜底。
