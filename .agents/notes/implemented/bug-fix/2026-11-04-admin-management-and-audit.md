# Agent Note: 保留首次登录并补齐全局管理与审计

Status: implemented

## Problem

服务端省察 F4/F5 中，管理台只看到管理员自己的设备/会话，截断列表无法翻页，授权操作缺少前后状态审计。用户要求修复服务端问题，同时明确保留 admin/admin 初始化以便部署后首次登录。

## Decision

默认 admin/admin 和本机 reset-admin 行为保留，首次密码仅授予短期 setup 权限，修改强密码并重新登录前不能访问管理或业务。已有管理员不会因重启恢复默认口令。F3 的首次认领窗口作为用户确认的取舍记录，不声称消除。

管理员使用独立、带目标账号的接口查看与撤销设备、绑定和会话，事务内复查操作者与所有权，提交后关闭在线通道。普通接口继续限于本人。用户搜索由服务端执行，用户、设备、绑定、会话、兑换码、兑换历史和审计采用稳定游标分页，前端切用户/搜索时丢弃旧响应。

每次生成的兑换码拥有独立批次，同账号每批只允许使用一个；生成与兑换审计记录批次，列表和 CSV 可核对批次 ID，规则和部署迁移见[批次兑换限制](../feature/2026-11-04-redemption-batch-account-limit.md)。

管理接口在 `internal/api/admin_devices.go`，角色、目标账号和请求来源以现有管理员认证/CSRF 为前提；mutation 在 `internal/store/revocation.go` 锁账号后再次核对角色和当前会话有效期，统一设备撤销、绑定级联及推送取消。设备按 gateway/mobile 分页以避免两个身份表 ID 并列时游标歧义。`pagination.go` 只允许明确的管理员 GET 路由携带 q/role/cursor/limit，所有值参数化；其他客户端和 WebSocket query 规则不放宽。每页默认 50，最大 200，时间与 UUID 联合排序，审计 ID 保持字符串表示并按数据库 bigint 排序。

授权状态变更与审计同事务：登记、关系发现/开关/撤销、会话创建/撤销、通知同意、账号禁用和管理员操作保存操作者、作用账号、设备/会话及安全前后状态。无变化轮询不重复记录，不保存密码、token、密钥、正文。审计失败回滚业务变更；清理失败记录不含私密数据库值的结构化告警。沿用未上线新库基线，不修改部署数据。

## 记录归属

[直接充值与控制台样式](../feature/2026-11-11-admin-topups-and-console-style.md)复用本篇管理员身份复查和同事务审计，增加账号天数充值回执；原设备管理、分页和撤销边界继续成立。

[注册体验活动](../feature/2026-11-09-registration-trial-campaigns.md)复用本篇管理员权限、Origin/CSRF、事务内复查、审计和游标分页约束；活动规则与一次赠送由该篇持有。分页器使用所选排序时间列建立游标，支持赠送时间和创建时间。

[原初始化提案](../../rejected/bug-fix/2026-11-04-administrator-ownership-and-audit.md)因默认密码取舍被否，保留原理由并互链。全局[省察](../../implemented/architecture/2026-11-03-server-architecture-and-scope-audit.md)部分重叠，更新 F3–F5 处置；[当前 schema](../../implemented/simplification/2026-11-04-prerelease-account-and-binding-schema.md)和[推送撤回](../../implemented/bug-fix/2026-11-04-push-withdrawal-independent-of-desktop-access.md)继续持有各自边界，无其他重复提案。

## Alternatives considered

- 随机初始化秘密可以消除公开默认口令的认领窗口，但用户明确选择 admin/admin 首登，故保留受限 setup 和强制改密。
- 放宽普通接口可少写路由，却模糊客户端与管理员权限，因此选独立的目标账号管理接口。
- 增大列表上限成本较低，但仍会截断且搜不到未加载用户，因此采用服务端搜索与稳定游标。
- 异步审计少耦合一个事务写入，但失败后无法追溯已生效变更，因此审计与授权变更一起提交或回滚。

## Testing

隔离 PostgreSQL 18.1 上完整 Go suite 104 个测试事件通过、无失败或跳过；vet、模块校验、格式通过。新增回归覆盖普通账号/失效管理员/错配目标/CSRF 拒绝、215 个同时间用户分页无遗漏重复、未加载用户搜索、数值审计游标、秘密排除、审计失败回滚、重复无变化不写审计，以及真实 WebSocket 的逐设备/会话关闭且其他手机继续通信。

9 项 Node UI 和 9 项部署 Python 回归通过，公开协议向量一致。Edge 与 WebKit 分别跑英文宽屏和中文窄屏的真实生产 Go 管理页/JS，共四组通过：使用合成 admin/admin 首登、强制改密、分页、选用户、撤销手机、审计展示和布局；截图已检查，修复了通用 button 样式覆盖分页 hidden 属性的问题。测试只访问随机回环端口的独立 schema，不调用外部 SMTP/推送或真实账号。

材料和本轮基线在 `D:/github/.codex-tools/server-remediation-20261104/`。默认配置、密码特例、初始化脚本、CLI 和 setup 登录字段与本轮开始时逐字节相同，首登现有测试继续通过。Windows 未启用 CGO，未执行 Go race；Linux CI 仍要求 race 与 PostgreSQL 全套无跳过。

## Consequences

管理员可定位并处置其他账号的具体设备与登录，授权变化有同事务的前后证据，普通账号接口边界保持清晰。代价是审计增加持久写入与空间，失败会阻止相应操作；分页不是跨请求快照，界面需要明确当前目标账号。首次认领前的公开默认口令窗口按用户决定保留。真实恢复/压测、签名设备和厂商送达仍须各自验收；这次没有部署、提交、推送或重置真实数据库。

后续用户授权的[当前 schema 部署](../process/2026-11-04-current-schema-server-deployment.md)已完成，记录上线验证与按要求清理旧库/旧版本的结果。
