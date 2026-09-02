# frps IP 白名单（飞书机器人管理）需求文档

| 项目 | 内容 |
|---|---|
| 文档版本 | v1.1 |
| 日期 | 2026-09-02 |
| 状态 | Phase 2 已完成：白名单存储和管理 API 已实现，连接拦截待 Phase 3 |
| 涉及仓库 | `frp`（frps 侧改动）+ 独立仓库 `frps-gateway`（机器人与管理 sidecar） |

## 1. 背景与目标

frp 用于将内网服务通过 frps 暴露到公网端口。当前任何知道端口的来源 IP 都可以直接访问映射端口，
缺乏访问控制。本需求在 frps 上增加**来源 IP 白名单**：只有白名单内的 IP 才能访问映射端口。

白名单的日常管理（增/删/查）不通过配置文件和重启，而是通过**飞书机器人**完成：
管理员在飞书里给机器人发消息（如 `加白 1.2.3.4 2h`），机器人调用 frps 管理 API 更新白名单并回复确认。

选择飞书而非微信的原因：飞书开放平台是成熟的官方企业 API（自建应用 + 机器人 + 长连接事件订阅），
有官方 Go SDK，身份体系基于员工 user_id/open_id，适合公司内控场景。

## 2. 总体架构

```
管理员在飞书发消息（私聊或指定群内 @机器人）
    │
    ▼
frps-gateway（独立 sidecar 进程，飞书官方 SDK WebSocket 长连接收消息）
    │  校验发送者是管理员 → 记录操作者与消息上下文 → 解析指令
    ├── SQLite（用户、授权、审计、恢复来源）
    ▼
frps 管理 API（HTTP + Basic Auth，本机/内网调用）
    POST/GET/DELETE  /api/v2/whitelist
    │
    ▼
frps 内存白名单 { IP → 过期时间 }（后台协程定期清理过期条目）
    ▲
    │ 每个用户连接进入时校验来源 IP
frps 连接处理汇聚点 GetWorkConnFromPool（server/proxy/proxy.go）
```

设计原则：**机器人与 frps 之间只通过 HTTP API 交互，无代码级耦合**，两者可独立部署、升级、重启。

## 3. 功能需求

### 3.1 frps 白名单模块（本仓库）

| 需求点 | 说明 |
|---|---|
| 作用范围 | **全局一份白名单**，所有代理端口共用 |
| 条目格式 | 单个 IPv4/IPv6 地址 + 有效期（TTL），到期自动失效 |
| 拦截范围 | 所有携带真实来源 IP 的用户连接：tcp、http、https、tcpmux、stcp/xtcp 的 visitor 监听路径 |
| 不拦截 | UDP/SUDP 代理（数据报路径拿不到来源地址）；xtcp 打洞直连路径 |
| 校验位置 | `BaseProxy.GetWorkConnFromPool()` 入口（分配 workConn 之前），一处覆盖全部 TCP 系代理类型 |
| 拦截行为 | 直接断开连接，记录 warn 日志（含来源 IP 与代理名） |
| 过期清理 | 后台协程定期清理过期条目；校验时也做惰性判断（过期即视为不在白名单） |
| 存储形态 | frps 使用内存态；gateway 使用 SQLite 记录授权，frps 重启后自动恢复未过期条目 |
| 功能开关 | `whitelist.enabled = false` 时完全恢复原版行为（方便回退） |
| 默认有效期 | frps 配置 `whitelist.defaultTTL`，API 未指定 TTL 时使用 |
| 空名单行为 | 启用白名单且名单为空时 fail-closed，拒绝全部受支持的业务连接 |

配置示例（frps.toml）：

```toml
# 自定义功能：IP 白名单
[whitelist]
enabled = true
defaultTTL = "2h"
```

行为说明（需在运维文档中注明）：全局白名单同样作用于 stcp/xtcp 的 visitor 连接，
即发起访问的 frpc 客户端所在 IP 也必须在白名单内。

### 3.2 frps 管理 API（本仓库）

在现有 v2 管理 API（`server/http/controller_v2.go`、`server/api_router.go`）上扩展，
复用 dashboard Basic Auth，并遵循 `{ "code": 200, "msg": "success", "data": ... }` 响应结构。
冻结后的完整契约、错误响应和测试方法见 [`api.md`](./api.md)。

| 方法与路径 | 请求体 | 响应 |
|---|---|---|
| `GET /api/v2/whitelist` | - | v2 envelope，`data` 为未过期条目数组 |
| `POST /api/v2/whitelist` | `{ "ip": "1.2.3.4", "ttl": "2h" }`（ttl 可省） | v2 envelope，`data` 为最终条目 |
| `DELETE /api/v2/whitelist` | `{ "ip": "1.2.3.4" }` | v2 envelope，`data` 为 `null` |

- IP 或 TTL 非法返回 400；重复添加视为刷新有效期，不返回 409。
- 删除不存在条目返回 404；列表不返回过期条目，并按标准化 IP 升序排列。
- 未通过鉴权返回 401（沿用现有中间件行为）。

### 3.3 飞书机器人 sidecar（独立仓库 `frps-gateway`）

| 需求点 | 说明 |
|---|---|
| 接入方式 | 飞书企业自建应用 + 机器人能力；事件订阅用**长连接模式**（官方 Go SDK `larksuite/oapi-sdk-go/v3`，WebSocket 出站连接，无需公网回调地址） |
| 消息来源 | 管理员私聊，或指定运维群内 @机器人（群成员即管理员，推荐） |
| 管理员鉴权 | 配置 `adminOpenIDs` 名单或 `adminChatID` 指定群，**二选一**；非管理员消息忽略并记日志 |
| 指令集 | 见 3.4 |
| 回复 | 每条指令执行后回复结果（成功/失败及剩余有效期）；飞书免费版 API 计量 1 万次/月，本场景用量远低于上限 |
| 事件时限 | 飞书要求事件 3 秒内处理完：指令处理放独立 goroutine，先应答事件再执行 |
| 审计上下文 | 执行层接收 open_id、用户名称、chat_id 和 message_id；暂时取不到的用户名称允许为空，后续补齐 |

配置示例（bot.toml）：

```toml
[frps]
apiAddr = "http://127.0.0.1:7500"   # frps webServer 地址
user = "admin"                        # webServer basic auth
password = "xxx"

[feishu]
appID = "cli_xxxxxxxx"
appSecret = "xxxxxxxx"

[bot]
# adminChatID 与 adminOpenIDs 二选一；群模式推荐（入职拉群、离职踢群）
adminChatID = "oc_xxxxxxxx"
# adminOpenIDs = ["ou_xxxxxxxx"]
defaultTTL = "2h"                     # 「加白」不带时长时使用
```

### 3.4 指令规范

| 指令 | 别名 | 说明 |
|---|---|---|
| `加白 1.2.3.4` | `add` | 加入白名单，有效期用 bot 配置的 defaultTTL |
| `加白 1.2.3.4 2h` | `add 1.2.3.4 2h` | 指定有效期，支持 `30m` / `2h` / `3d` 等格式 |
| `删白 1.2.3.4` | `remove` | 移除条目 |
| `白名单` | `list` | 列出全部条目及剩余有效期 |

解析失败时回复用法提示。IP 格式校验在 bot 与 frps API 双侧进行。

## 4. 非功能需求

- **安全性**：管理 API 走 Basic Auth 且仅允许本机或管理内网访问；bot 仅受理管理员消息；白名单变更记录 IP、操作者、消息、时间和结果。
- **恢复性**：gateway 的 SQLite 是授权和审计来源，frps 重启后自动恢复尚未过期的有效条目。
- **可回退性**：`whitelist.enabled = false` 一键恢复原版行为；bot 进程崩溃不影响 frps 转发。
- **性能**：白名单读路径为 RWMutex + map 查找，单次开销可忽略；连接热路径不引入网络调用。
- **低侵入性**：frps 侧改动集中（白名单模块、`GetWorkConnFromPool` 一处校验、API 路由），便于后续同步上游 frp 更新。

## 5. 分阶段实施

| 阶段 | 环境 | 内容 |
|---|---|---|
| 阶段一：测试 | 个人注册飞书 + 自建单人小团队（免费，本人即管理员） | 完成全部开发；在测试环境验证拦截、指令、过期、鉴权；产出测试报告 |
| 阶段二：上线 | 公司飞书租户 | 公司租户新建自建应用，走企业管理员审批发布；**换 bot 配置中的 AppID/AppSecret 及管理员 open_id/群 ID 即完成迁移，零代码改动** |

注意：同一员工在测试团队与公司租户中的 open_id 不同，迁移时管理员名单/群 ID 必须换成公司租户的值。

## 6. 验收标准

1. 非白名单 IP 访问 tcp 映射端口：连接被立即断开，frps 日志有拒绝记录；
2. 白名单内 IP 正常访问；条目过期后自动失效（无需重启）；
3. http/https 域名型代理同样受白名单控制；
4. `whitelist.enabled = false` 时行为与原版 frps 完全一致；
5. 飞书指令端到端（发消息 → 收到回复）≤ 2 秒（正常网络）；
6. 非管理员发送指令被忽略且记日志；管理 API 无鉴权访问返回 401；
7. bot 进程停止/重启期间，frps 转发与既有白名单不受影响；
8. frps 重启后，gateway 能重新同步 SQLite 中尚未过期的有效授权。

## 7. 范围外（Out of Scope）

- CIDR 网段条目（如 `10.0.0.0/24`）；
- UDP/SUDP 代理的来源拦截；
- frps 自身直接持久化白名单（持久化和恢复由 gateway 负责）；
- 按代理/按用户区分的多份白名单；
- 飞书卡片消息交互界面（后续可选增强）。

## 8. 代码改动清单（本仓库）

| 文件 | 改动 |
|---|---|
| `pkg/config/v1/server.go` | 新增 `whitelist` 配置段（enabled、defaultTTL） |
| `server/controller/whitelist.go`（新增） | 白名单模块：内存表、TTL 清理、Contains/Add/Remove/List |
| `server/controller/resource.go` | 将白名单实例挂入 ResourceController |
| `server/proxy/proxy.go` | `GetWorkConnFromPool` 入口加校验（src 为 nil 的 UDP/XTCP 路径跳过） |
| `server/http/controller_v2.go` | 新增 whitelist 增删查 handler |
| `server/api_router.go` | 注册 `/api/v2/whitelist` 路由 |
| `conf/` | 补充配置示例 |
| 单元测试 | 白名单模块 TTL/并发、API handler、指令解析（bot 侧） |

机器人 sidecar 全部代码位于独立仓库 `frps-gateway`，与 `frp` 仓库仅通过上述 HTTP API 交互。
