# EasyTier P2P

服务端配置 `BRIDGE_P2P_BOOTSTRAP`（最多四个逗号分隔的地址）后向客户端声明能力。
支持 `tcp://host:port`、`udp://host:port` 和
`wss://host/v1/p2p/bootstrap`。客户端初次配对、设备身份与本地桌面批准不变。

推荐在原 Compose 命令后叠加 `-f deploy/compose.p2p.yaml`。引导节点启用
`relay_all_peer_rpc`，空业务网络白名单，并设置 `disable_relay_data=true`。
该版本的最后一个设置通过 TOML 配置，不是命令行参数。

WSS 引导复用现有 Caddy/HTTPS 入口，无需额外公网端口。Go 将字节送到部署者指定的
`BRIDGE_P2P_PROXY_TARGET=p2p:11010`，不接受客户端选择目的地址，限制并发、帧大小
和空闲时间。节点只协助发现和打洞，P2P 数据不经此入口，失败的数据回退仍走
Bridge 的 Noise 中继。可按需删除 Compose 中的 11010 端口发布，仅保留内部网络。

`access-v1` 控制连接支持服务器主动授权变化信令；匹配当前身份后重新核验并关闭
失效连接，避免迟到通知误撤销已经续期或重新批准的会话。既有目录同时返回按账号
合并检查的 `access` 与各绑定 `access_allowed`。断线、超时、5xx 不是撤销。
新 P2P 会话由桌面调用绑定 P2P 检查接口，服务器不可达不能建立新会话。

关闭 `BRIDGE_P2P_BOOTSTRAP` 后新连接回退中继。已建立的直连按用户选择持续到下次
明确校验结论；需要立即终止时使用桌面本地访问开关或管理员撤销。更新前备份数据库
与当前部署配置，保留前一服务镜像；本功能不需要数据库迁移或重新配对。
