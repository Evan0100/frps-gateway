# frps Whitelist API

## frps-gateway 用户授权 HTTP 契约

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| `GET` | `/healthz` | 无 | 健康检查，返回 `200 ok` |
| `GET` | `/readyz` | 无 | SQLite 可用且 frps 管理 API 可达时返回 `200 ready`，否则返回 `503` |
| `GET` | `/authorize/{token}` | 一次性令牌 | 显示检测到的公网 IP 和确认页 |
| `POST` | `/authorize/{token}` | 一次性令牌 | 原子消费令牌、记录用户授权并同步 frps |

令牌为 32 字节加密随机数，SQLite 仅保存 SHA-256；默认 5 分钟过期且只能成功消费一次。GET 与 POST 对无效、过期或重复使用的令牌统一返回 `410 Gone`。每名员工默认最多保留 3 个不同的有效 IP；刷新已有 IP 不占新名额，超限返回 `409 Conflict`，并且不会消费令牌。只有直接连接来自 `server.trustedProxyCIDRs` 时才解析 `X-Forwarded-For`；可信代理请求缺少有效的非可信来源地址时拒绝处理。自动识别和手工新增均拒绝私网、环回、链路本地、组播和未指定地址。数据库已写入但 frps 同步失败返回 `502`；启动及每 30 秒一次的周期对账会再次应用有效授权。

飞书事件先写入 SQLite inbox，落盘成功后才确认；重复事件按 message ID 去重，处理中断后会自动重试。回复采用 SQLite outbox：命令结果先持久化，再由后台发送；发送失败按指数退避重试，并使用由 message ID 派生的稳定 UUID 防止重试产生重复消息。`/healthz` 只表示进程存活；发布验收和监控应使用 `/readyz`，其检查超时为 3 秒。两个端点都不会暴露内部错误或凭据。

飞书交互契约：`/start`、`开始`、`菜单` 返回动态 `interactive` 卡片；手动输入仅接受 `申请授权 <IP> [有效期]`、`我的授权` 和 `撤销授权 <IP>`。原申请访问、我的访问、撤销、加白、删白、白名单、help 及英文命令已移除。卡片动作通过长连接回调 `card.action.trigger` 接收；操作者身份只取回调中的 `operator.open_id`，聊天范围只取 `context.open_chat_id`，不接受按钮 value 传入身份。`list` 动作读取当前用户有效 grant；`revoke` 动作只接受合法 IP，并复用现有按用户撤销、共享 IP 聚合和 MessageID 幂等逻辑；未知动作只返回错误提示，不执行状态变更。

员工授权只提交 TTL，不提交访问时段。同一公网 IP 有多个员工授权时，frps 条目的到期时间取所有有效 grant 的最大值；管理员直接在 frps 设置的访问时段是该 IP 的全局策略，gateway 刷新 TTL 时省略 `windows`，因此不会覆盖该策略。

## frps-gateway 管理后台 HTTP 契约

后台默认关闭；配置 `[admin] enabled=true` 后挂载在 `/admin`。后台账号必须与 frps dashboard 账号不同，密码至少 16 个字符，并通过 `passwordEnv` 或 `passwordFile` 外置。会话仅保存在 gateway 内存中，使用 32 字节随机令牌、`Secure`、`HttpOnly`、`SameSite=Strict` Cookie；gateway 重启或会话到期后需要重新登录。所有状态变更要求同源请求和会话 CSRF 令牌，请求体上限 16 KiB。

配置 `admin.knockSecretEnv` 或 `admin.knockSecretFile` 后启用可选敲门层。未持有有效 gate Cookie 且没有已登录会话时，所有 `/admin` 页面返回无正文 `404`。用户必须在 `knockWindow` 内连续访问 `/admin/knock/{secret}` 达到 `knockHits` 次；前 N-1 次同样返回空 `404`，第 N 次签发仅在内存有效的短期 gate Cookie，并返回 `303 /admin/login`。登录成功后 gate 立即失效；会话失效后需要重新敲门。

| 方法 | 路径 | 鉴权 | 状态 | 行为 |
|---|---|---|---|---|
| `GET` | `/admin/login` | gate（敲门关闭时无） | Changed | 创建短期登录 nonce 并显示登录页 |
| `GET` | `/admin/knock/{secret}` | 高熵敲门 secret + 进度 Cookie | Added | 前 N-1 次空 `404`；第 N 次返回 `303 /admin/login` 并签发短期 gate |
| `POST` | `/admin/login` | gate + 登录 nonce | Changed | 校验独立后台账号；成功返回 `303 /admin`，连续失败触发 `429` |
| `POST` | `/admin/logout` | 会话 + CSRF | Added | 销毁当前内存会话并返回 `303 /admin/login` |
| `GET` | `/admin` | 会话 | Added | 展示 frps 当前条目、gateway 授权、访问记录和后台审计 |
| `POST` | `/admin/grants` | 会话 + CSRF | Added | 新增管理员授权并立即对账 frps |
| `POST` | `/admin/grants/{id}/extend` | 会话 + CSRF | Added | 从当前时间重新计算指定授权的有效期并对账 |
| `POST` | `/admin/grants/{id}/revoke` | 会话 + CSRF | Added | 只撤销指定 grant；同 IP 的其他有效授权继续生效 |

`GET /admin` 支持 `ip`、`user`、`status=active|expired|revoked`、`action=allow|deny`、`actor`、`from`、`to` 过滤；授权、访问记录和后台审计分别使用 `grantPage`、`accessPage`、`auditPage` 分页，每页 50 条。frps 中没有 gateway grant 的条目标记为“frps 应急/手工”，仅展示、不自动接管或删除。

管理员新增字段为 `subject_id`、`subject_name`、`ip`、`ttl`；延期字段为 `ttl`。有效期不得超过 `admin.maxGrantTTL`。新增、延期和撤销先在同一 SQLite 事务中写入 grant 与 `admin_audit`，随后执行 gateway 对账。同步成功时审计结果为 `synced`；同步失败时授权记录仍保留，结果为 `sync_failed`，周期对账会继续重试。

错误行为：表单或 IP/TTL 非法返回 `400`；会话或 CSRF 失败返回重定向或 `403`；记录不存在返回 `404`；记录已经撤销/过期返回 `409`；登录限速返回 `429`；数据库内部错误返回 `500`。响应不包含凭据和内部数据库结构。

敲门层只用于降低后台被一次性扫描发现的概率，不替代登录、MFA、VPN、管理网或反向代理访问控制。secret 至少 24 个字符，只允许 URL 安全的字母、数字、`-`、`_`，且不得复用密码。由于 secret 位于 URL，反向代理必须对 `/admin/knock/` 关闭或脱敏访问日志，浏览器不得收藏、同步或分享该地址。

## Overview

白名单接口属于 frps dashboard API v2，由 frps 实现，`frps-gateway` 作为调用方。frps 保存当前生效条目、操作账号元数据和最近 5000 次成功变更；飞书用户、申请上下文和长期审计仍由 gateway 负责。

| Module | Method | Path | Summary | Auth | Status | Updated |
|---|---|---|---|---|---|---|
| Whitelist | DELETE | `/api/v2/whitelist` | 删除一个 IP | Basic Auth | Added | 2026-09-02 |
| Whitelist | GET | `/api/v2/whitelist` | 查询当前生效条目 | Basic Auth | Added | 2026-09-02 |
| Whitelist | POST | `/api/v2/whitelist` | 添加 IP 或刷新有效期,可选设置访问时段 | Basic Auth | Modified | 2026-09-03 |
| Whitelist | GET | `/api/v2/whitelist/status` | 查询拦截开关、默认 TTL 和服务器时间 | Basic Auth | Modified | 2026-09-03 |
| Whitelist | GET | `/api/v2/whitelist/accesslog` | 查询访问记录(放行/拒绝及原因) | Basic Auth | Added | 2026-09-03 |
| Whitelist | GET | `/api/v2/whitelist/auditlog` | 查询持久化的白名单变更审计 | Basic Auth | Added | 2026-09-03 |

六个白名单接口已在 frps 中实现并完成自动化测试；`whitelist.enabled = true` 时在连接入口执行拦截。frps 同时在 `GET /whitelist` 内置了使用这些接口的管理页面（同一 Basic Auth）。

## Enforcement

- `whitelist.enabled = false`(默认)时行为与原版 frps 完全一致,接口仍可用于预填名单。
- `whitelist.enabled = true` 且名单为空时拒绝全部受支持的业务连接(fail-closed)。
- 允许条件:`IP 在名单 && 条目未过期 && 当前服务器时间在条目允许时段内`。条目无时段时全天可访问。
- 拦截点:TCP/HTTPS/TCPMux/STCP/SUDP visitor 连接在申请 work connection 之前拒绝;HTTP vhost 请求直接返回 403。拦截发生在连接建立/请求时,窗口外不主动断开已建立的连接。
- 不拦截:UDP/SUDP 按包来源、XTCP 打洞直连;STCP/SUDP visitor 连接上校验的是 visitor frpc 主机 IP 而非最终用户 IP。

## Common Behavior

- 管理 API 复用 `webServer.user` 和 `webServer.password` 的 Basic Auth；启用白名单时缺少 webServer 端口、用户名、密码或 `whitelist.storageFile` 会导致配置校验失败。
- `POST` 与 `DELETE` 请求体最大 16 KiB，只允许一个 JSON 值并拒绝未知字段；超限返回 `413`，其他 JSON 结构错误返回 `400`。
- 生产环境只允许通过本机回环地址或受控管理内网访问，远程访问必须使用 HTTPS 或安全隧道。
- 当前名单和最近 5000 次成功变更原子写入 `whitelist.storageFile`，frps 重启时恢复未过期条目。
- IPv4 和 IPv6 必须标准化后存储；重复添加同一 IP 会刷新过期时间。
- `expireAt` 为 Unix 秒时间戳。
- GET 只返回未过期条目，并按标准化 IP 字符串升序排序。
- DELETE 不具备幂等成功语义：目标不存在时返回 404。
- 当前不提供分页、CIDR、永久条目和批量操作。

除 Basic Auth 失败外，v2 API 使用统一响应结构：

```json
{
  "code": 200,
  "msg": "success",
  "data": null
}
```

Basic Auth 失败由 frps 现有中间件直接返回 HTTP 401 和纯文本 `Unauthorized`。

## Changes

| Date | Change | Method | Path | Summary |
|---|---|---|---|---|
| 2026-09-02 | Added | DELETE | `/api/v2/whitelist` | 实现删除接口和 400/401/404 错误行为 |
| 2026-09-02 | Added | GET | `/api/v2/whitelist` | 实现未过期条目查询和稳定排序 |
| 2026-09-02 | Added | POST | `/api/v2/whitelist` | 实现添加、TTL 默认值和重复刷新 |
| 2026-09-03 | Added | GET | `/api/v2/whitelist/status` | 暴露 `whitelist.enabled` 与默认 TTL,供管理页面和管理端展示拦截状态 |
| 2026-09-03 | Modified | GET/POST | `/api/v2/whitelist` | 条目支持可选访问时段 `windows`(星期 + 时段);status 增加 `serverTime` |
| 2026-09-03 | Added | GET | `/api/v2/whitelist/accesslog` | 查询内存访问记录(环形缓冲,重启清空),支持按结果和 IP 过滤 |
| 2026-09-03 | Changed | GET/POST/DELETE | `/api/v2/whitelist` | 增加跨重启持久化、操作账号/来源元数据和服务端计算的 `allowedNow` |
| 2026-09-03 | Changed | GET | `/api/v2/whitelist/status` | 增加 RFC3339 时间、UTC 偏移和访问日志启用状态 |
| 2026-09-03 | Added | GET | `/api/v2/whitelist/auditlog` | 查询持久化的成功增删/延期记录 |
| 2026-09-03 | Added | GET | `/readyz` | gateway SQLite 与 frps 管理链路就绪检查 |
| 2026-09-04 | Added | GET/POST | `/admin/*` | gateway 独立后台登录、授权管理、筛选、访问记录和审计 |
| 2026-09-04 | Added | GET | `/admin/knock/{secret}` | 可选多次秘密敲门和短期后台 gate；未敲门时后台返回空 404 |
| 2026-09-04 | Changed | POST | `/authorize/{token}` | 可信代理缺少有效来源头及特殊地址时拒绝授权 |

## Whitelist

### GET /api/v2/whitelist

**Summary:** 查询当前全部未过期白名单条目(含访问时段)

**Auth:** Basic Auth required

**Status:** Added

**Updated:** 2026-09-03

#### Success Response

| Status | Description |
|---|---|
| 200 | 返回按 IP 升序排列的生效条目 |

```json
{
  "code": 200,
  "msg": "success",
  "data": [
    {
      "ip": "1.2.3.4",
      "expireAt": 1788350400,
      "allowedNow": true,
      "createdAt": 1788343200,
      "updatedAt": 1788343200,
      "createdBy": "admin",
      "updatedBy": "admin",
      "updatedFrom": "127.0.0.1",
      "windows": [
        { "days": [1, 2, 3, 4, 5], "start": "09:00", "end": "18:00" }
      ]
    }
  ]
}
```

`allowedNow` 是 frps 按服务器当前时间计算的实际访问状态。`createdBy`/`updatedBy` 是 Basic Auth 管理账号，`updatedFrom` 是管理请求来源 IP。`windows` 为空或省略表示全天可访问。`days` 为 ISO 星期编号,1=周一…7=周日;`start`/`end` 为 `HH:MM`(服务器本地时间),`start > end` 表示跨午夜(如 22:00–06:00);区间为半开 `[start, end)`,即到点即拒。

#### Error Responses

| Status | Code | Message | Reason |
|---|---:|---|---|
| 401 | - | `Unauthorized` | Basic Auth 缺失或错误 |
| 500 | 500 | 实际内部错误 | 服务端内部错误 |

### POST /api/v2/whitelist

**Summary:** 添加 IP;如果 IP 已存在则从当前时间重新计算 TTL,并按 `windows` 语义更新时段

**Auth:** Basic Auth required

**Status:** Modified

**Updated:** 2026-09-03

#### Request Body

| Field | Type | Required | Description |
|---|---|---|---|
| `ip` | string | Yes | 单个合法 IPv4 或 IPv6 地址 |
| `ttl` | string | No | 正数时长,支持 `d/h/m/s` 组合;省略时使用服务端默认 TTL |
| `windows` | array | No | 访问时段,见下方三态语义 |

`windows` 三态语义(用 JSON 字段是否出现区分):

| 请求形态 | 行为 |
|---|---|
| 字段省略 | 刷新有效期并**保留**该 IP 已有时段(新条目则为全天);页面"延期"使用此形态 |
| `"windows": []` | 清空时段,恢复全天可访问 |
| `"windows": [...]` | 整体替换为新的时段列表,每组 `{days, start, end}` |

```json
{
  "ip": "1.2.3.4",
  "ttl": "2h",
  "windows": [
    { "days": [1, 2, 3, 4, 5], "start": "09:00", "end": "18:00" },
    { "days": [6], "start": "22:00", "end": "06:00" }
  ]
}
```

多组时段任一命中即可访问;跨午夜时段的凌晨部分归属起始日的规则(周三 22:00–06:00 覆盖周四凌晨)。时段校验失败返回 400 `invalid window`,不影响 TTL 刷新。

#### Success Response

新建和刷新均返回 HTTP 200,`data.windows` 为该条目实际生效的时段:

```json
{
  "code": 200,
  "msg": "success",
  "data": {
    "ip": "1.2.3.4",
    "expireAt": 1788350400,
    "allowedNow": true,
    "createdAt": 1788343200,
    "updatedAt": 1788343200,
    "createdBy": "admin",
    "updatedBy": "admin",
    "updatedFrom": "127.0.0.1",
    "windows": [
      { "days": [1, 2, 3, 4, 5], "start": "09:00", "end": "18:00" }
    ]
  }
}
```

#### Error Responses

| Status | Code | Message | Reason |
|---|---:|---|---|
| 400 | 400 | `invalid request body` | JSON 缺失、损坏或包含错误字段类型 |
| 400 | 400 | `invalid ip` | IP 缺失或格式非法 |
| 400 | 400 | `invalid ttl` | TTL 非正数或格式非法 |
| 400 | 400 | `invalid window` | 时段非法:星期越界或为空、`HH:MM` 格式错误、start 等于 end |
| 401 | - | `Unauthorized` | Basic Auth 缺失或错误 |
| 500 | 500 | 实际内部错误 | 服务端内部错误 |

### DELETE /api/v2/whitelist

**Summary:** 删除一个白名单 IP

**Auth:** Basic Auth required

**Status:** Added

**Updated:** 2026-09-02

#### Request Body

| Field | Type | Required | Description |
|---|---|---|---|
| `ip` | string | Yes | 单个合法 IPv4 或 IPv6 地址 |

```json
{
  "ip": "1.2.3.4"
}
```

#### Success Response

```json
{
  "code": 200,
  "msg": "success",
  "data": null
}
```

#### Error Responses

| Status | Code | Message | Reason |
|---|---:|---|---|
| 400 | 400 | `invalid request body` | JSON 缺失、损坏或包含错误字段类型 |
| 400 | 400 | `invalid ip` | IP 缺失或格式非法 |
| 401 | - | `Unauthorized` | Basic Auth 缺失或错误 |
| 404 | 404 | `whitelist entry not found` | IP 当前不在白名单中 |
| 500 | 500 | 实际内部错误 | 服务端内部错误 |

### GET /api/v2/whitelist/status

**Summary:** 查询连接拦截是否开启以及服务端默认 TTL

**Auth:** Basic Auth required

**Status:** Added

**Updated:** 2026-09-03

#### Success Response

| Status | Description |
|---|---|
| 200 | 返回拦截开关和默认 TTL |

```json
{
  "code": 200,
  "msg": "success",
  "data": {
    "enabled": true,
    "defaultTTL": "2h0m0s",
    "serverTime": 1788399316,
    "serverTimeRFC3339": "2026-09-03T16:15:16+08:00",
    "serverUTCOffset": 28800,
    "accessLogEnabled": true,
    "accessLogSize": 5000
  }
}
```

`defaultTTL` 为 Go `time.Duration` 字符串形式；`serverTimeRFC3339` 和 `serverUTCOffset` 明确服务器墙上时间与时区。列表中的 `allowedNow` 由服务端计算，页面不再依赖浏览器时区猜测。`accessLogEnabled`/`accessLogSize` 用于区分“暂无记录”和“记录已关闭”。

#### Error Responses

| Status | Code | Message | Reason |
|---|---:|---|---|
| 401 | - | `Unauthorized` | Basic Auth 缺失或错误 |
| 500 | 500 | 实际内部错误 | 服务端内部错误 |

### GET /api/v2/whitelist/accesslog

**Summary:** 查询白名单访问记录(经白名单管控的每次访问,放行与拒绝都记录)

**Auth:** Basic Auth required

**Status:** Added

**Updated:** 2026-09-03

#### Query Parameters

| Parameter | Type | Required | Description |
|---|---|---|---|
| `action` | string | No | `allow` 或 `deny`;省略返回全部 |
| `ip` | string | No | 按来源 IP 大小写不敏感子串匹配 |
| `limit` | int | No | 返回条数上限,默认 200,最大 5000 |

#### Success Response

记录按时间倒序(最新在前):

```json
{
  "code": 200,
  "msg": "success",
  "data": [
    {
      "instance": "9f86d081884c7d65",
      "seq": 42,
      "time": 1788399316,
      "ip": "198.51.100.7",
      "user": "alice",
      "source": "tcp/my-proxy",
      "action": "deny",
      "reason": "not_in_whitelist"
    }
  ]
}
```

字段说明:

| Field | Description |
|---|---|
| `instance` | 产生记录的 frps 进程标识(每次重启重新生成),与 `seq` 联合唯一 |
| `seq` | 进程内单调递增序号;日志采集方按 `(instance, seq)` 去重,可安全重复拉取 |
| `time` | 记录时间的 Unix 秒(服务器时间) |
| `ip` | 来源 IP(标准化后) |
| `user` | 代理属主(frpc 登录用户);HTTP vhost 记录为空 |
| `source` | 访问目标:`tcp/<代理名>` 或 `http/<域名>` |
| `action` | `allow`(放行)/ `deny`(拒绝) |
| `reason` | `whitelisted`(名单内放行)/ `not_in_whitelist`(不在名单或名单为空)/ `expired`(条目已过期)/ `outside_time_window`(时段外) |

gateway 会拒绝 `instance` 为空或 `seq=0` 的访问记录，以防旧版 frps 与新版 gateway 混合部署时把多条记录折叠成同一个主键。必须先升级 frps，再启用 gateway 的 `ingest`。采集器重复拉取最近最多 5000 条并去重；轮询失败会记录错误，检测到序号断档时会告警。断档通常表示 frps 内存环在采集恢复前已经覆盖了部分记录，告警不能恢复已丢失的数据。

gateway 对 frps API 响应实施大小上限；超过上限返回本地 `ErrResponseTooLarge`，不再将截断内容误报为普通 JSON 解码错误。

存储为内存环形缓冲(容量 `whitelist.accessLogSize`,默认 5000,负数关闭记录),frps 重启后清空。关闭时接口返回 HTTP 200 和空数组。它属于运行访问记录，不作为白名单变更审计；持久化变更见 `/api/v2/whitelist/auditlog`。TCP 族在连接建立时记录;HTTP vhost 每个请求记录(不含 URL path)。

#### Error Responses

| Status | Code | Message | Reason |
|---|---:|---|---|
| 400 | 400 | `invalid action` | action 不是 allow/deny |
| 400 | 400 | `invalid limit` | limit 非数字、负数或大于 5000 |
| 401 | - | `Unauthorized` | Basic Auth 缺失或错误 |

### GET /api/v2/whitelist/auditlog

**Summary:** 查询持久化的白名单成功变更记录，最新记录在前

**Auth:** Basic Auth required

**Status:** Added

**Updated:** 2026-09-03

#### Query Parameters

| Parameter | Type | Required | Description |
|---|---|---|---|
| `limit` | int | No | 返回条数上限，默认 200，最大 5000 |

#### Success Response

```json
{
  "code": 200,
  "msg": "success",
  "data": [
    {
      "sequence": 12,
      "time": 1788399316,
      "operation": "extend",
      "ip": "1.2.3.4",
      "operator": "admin",
      "remoteAddr": "127.0.0.1",
      "expireAt": 1788406516,
      "windows": []
    }
  ]
}
```

`operation` 为 `add`、`extend` 或 `remove`。审计与白名单状态在同一文件内原子更新，默认保留最近 5000 次成功操作。Basic Auth 当前只有一个配置账号，因此 `operator` 表示管理账号；飞书用户身份仍由后续 gateway 长期审计关联。

#### Error Responses

| Status | Code | Message | Reason |
|---|---:|---|---|
| 400 | 400 | `invalid limit` | limit 非数字、负数或大于 5000 |
| 401 | - | `Unauthorized` | Basic Auth 缺失或错误 |

## Test Method

gateway 后台、会话、CSRF、代理边界、SQLite 审计和 frps 客户端安全行为：

```bash
go test ./admin ./authorize ./command ./config ./frps ./store
```

浏览器验收应通过实际 HTTPS 地址访问 `/admin`，依次验证：未登录跳转、错误密码限速、跨站 POST/缺失 CSRF 返回 403、新增/延期/撤销产生审计、同 IP 其他 grant 不受单条撤销影响，以及 frps 手工条目显示为只读。

```bash
curl -u admin:password http://127.0.0.1:7500/api/v2/whitelist

curl -u admin:password \
  -H "Content-Type: application/json" \
  -d '{"ip":"1.2.3.4","ttl":"2h"}' \
  http://127.0.0.1:7500/api/v2/whitelist

curl -u admin:password \
  -X DELETE \
  -H "Content-Type: application/json" \
  -d '{"ip":"1.2.3.4"}' \
  http://127.0.0.1:7500/api/v2/whitelist

curl -u admin:password http://127.0.0.1:7500/api/v2/whitelist/status

curl -u admin:password "http://127.0.0.1:7500/api/v2/whitelist/auditlog?limit=200"
```

服务端自动化测试必须覆盖成功、无鉴权、错误鉴权、非法 JSON、非法 IP、非法 TTL、重复添加刷新和删除不存在条目；拦截行为另由 e2e 测试覆盖默认拒绝、加白放行、过期失效和关闭开关回退。
