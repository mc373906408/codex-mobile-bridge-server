# Agent Note: Server 改用 AGPLv3

Status: implemented

## Problem

项目所有者要求 Server 和 App 统一采用 AGPL。Server 的许可证、网页页脚和架构说明仍写 MIT；若只换根文件，使用者会从不同入口看到矛盾授权，部署者也缺少修改版网络服务应提供对应源码的说明。

## Decision

Server 自有代码采用 `AGPL-3.0-only`，即 GNU Affero General Public License 第 3 版，不额外授权自动适用未来版本。根 [LICENSE](../../../../LICENSE)保存 [GNU 官方完整文本](https://www.gnu.org/licenses/agpl-3.0.txt)，[NOTICE](../../../../NOTICE)保留 `Copyright (c) 2026 waken`、明确版本、免责说明和源码地址；第三方组件沿用原有许可。

README、实现状态和中英文网页说明统一许可名称。首页、管理员页和云连接入口展示源码链接；Docker 镜像携带 LICENSE、NOTICE，并以 OCI 标签标明 SPDX 标识。根据 AGPLv3 第 13 节，修改后通过网络与用户交互的版本应向这些用户提供对应源码；部署修改版时，需要将页脚链接指向实际运行版本，不能仅指向未包含改动的上游。此改动本身不部署服务或发布镜像。

## 记录归属

已检索活跃笔记中的 MIT、GPL、AGPL、license、许可证、商业和开源关键词。[全局架构](../architecture/2026-11-03-server-architecture-and-scope-audit.md)部分重叠，更新当前许可事实并互链，继续持有系统架构。[商业使用许可边界](../../rejected/process/2026-11-09-server-commercial-use-license.md)是过时提案，转为 rejected 并保留旧理由及备选；其他权限类命中与版权许可无关。没有归档旧提案，也不追改历史 MIT 发布的授权。

## Alternatives considered

- 保留 MIT 可继续降低集成和商业采用成本，但不符合所有者此次明确选择的 AGPL。
- GPLv3 提供分发时的 copyleft 要求，授权范围更少涉及网络运行；所有者明确改选 AGPL，因此采用含第 13 节网络交互要求的许可证。
- AGPL-3.0-or-later 让接收者可选未来版本，减少将来迁移成本；本次明确采用第 3 版，不替所有者增加未来版本的许可授权。
- 非商业或限制第三方托管的源码可见许可可实现旧提案的商业限制，但与最终选择的标准 AGPL 不同，不加入自定义商业限制。

## Testing

Server、App 根目录及 App 插件的三个 LICENSE 副本与此次下载的 GNU 官方 AGPLv3 文本逐字节一致，SHA256 为 `0d96a4ff68ad6d4b6f1f30f713b18d5184912ba8dd389f86aa7710db079abcb0`。SPDX 标识、JSON 文案、三个模板源码链接和 Dockerfile 许可文件包含检查通过；现行自有代码说明不再宣称 MIT，历史方案及第三方说明保留。

使用 Go 1.27.3 对 `internal/api` 运行首页、管理员控制台、语言选择、模板占位符、静态文案及普通账号网页登录的六个既有测试，结果通过。笔记树、格式及归档三项门禁通过。Dockerfile 仅进行静态检查，本轮未构建或部署服务镜像。

## Consequences

Server 与 App 使用一致的标准网络 copyleft 许可，源码获取入口可见。后续发布需维护对应源码与实际部署版本的一致性，修改版维护者需要承担适用的源码提供义务。AGPL 仍允许商业使用，不能用来兑现旧提案的“禁止商用”。旧 MIT 版本及第三方授权不因换根文件而撤销；本次不声称已完成全部历史贡献和依赖组合的法律审计。
