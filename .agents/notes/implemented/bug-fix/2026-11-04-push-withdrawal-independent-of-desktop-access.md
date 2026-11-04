# Agent Note: 手机推送撤回独立于桌面访问资格

Status: implemented

## Problem

[服务端省察 F1](../architecture/2026-11-03-server-architecture-and-scope-audit.md) 确认 `RegisterEndpoint` 在关闭分支前要求 active binding 和双方有效会话，导致已登录手机在桌面关闭/退出后无法撤回自身通知同意。App 也会把禁用设备排除在撤回列表外；只修一端不能完成权限缩减。

## Decision

关闭 endpoint 的事务只要求调用方是当前有效手机、账号有效、自己拥有该 binding，且 binding 双方属于同账号。桌面连接状态、桌面会话、桌面撤销和托管有效期不作为撤回前提。事务先锁 binding，再将 endpoint 的 enabled/consent 置 false 并取消 queued/leased 任务。不存在 endpoint 时也可幂等完成关闭。

启用仍走原有 active binding、双方有效会话、托管资格与 provider/token 校验。跨手机、跨账号、gateway token 和已失效的调用方会话均拒绝。App 的对应专用关闭入口不建立业务连接；既有账号和密钥不迁移，不修改 schema。

## Alternatives considered

- 只允许 active binding 撤回可以复用业务授权查询，但使用户缩减权限依赖对端在线资格，正是本次故障原因。
- 将全部 endpoint 操作改成所有权检查会减少分支，却放宽启用条件；因此只对关闭分支使用独立的缩减权限检查。

## Consequences

关闭桌面访问、对端退出和服务到期不会阻止手机撤回推送；再次允许连接保留 consent=false。两类操作的前提不同，需要分别测试，不应再次共用一个 active 查询。对端或厂商已经接受的通知无法被这个事务追回；客户端本地同意和收据规则仍然必要。

## Testing

`internal/api/push_withdrawal_test.go` 通过真实 API 和隔离 PostgreSQL 覆盖 binding disabled、gateway session revoked、gateway revoked、hosted expired 四个状态，启用拒绝、撤回成功、重复撤回、queued/leased 取消，以及恢复访问不恢复 consent。另覆盖同账号其他手机、其他账号、gateway token 和撤销调用方会话的拒绝。完整 Go suite 与 vet 的结果记录在同批修复验证中；没有真实推送或生产数据库操作。
