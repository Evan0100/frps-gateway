# frps Whitelist API

## frps-gateway 用户授权 HTTP 契约

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| `GET` | `/healthz` | 无 | 健康检查，返回 `200 ok` |
| `GET` | `/authorize/{token}` | 一次性令牌 | 显示检测到的公网 IP 和确认页 |
| `POST` | `/authorize/{token}` | 一次性令牌 | 原子消费令牌、记录用户授权并同步 frps |

令牌为 32 字节加密随机数，SQLite 仅保存 SHA-256；默认 5 分钟过期且只能成功消费一次。GET 与 POST 对无效、过期或重复使用的令牌统一返回 `410 Gone`。每名员工默认最多保留 3 个不同的有效 IP；刷新已有 IP 不占新名额，超限返回 `409 Conflict`，并且不会消费令牌。只有直接连接来自 `server.trustedProxyCIDRs` 时才解析 `X-Forwarded-For`，否则使用 TCP 对端地址。数据库已写入但 frps 同步失败返回 `502`，启动对账会再次应用有效授权。

员工授权只提交 TTL，不提交访问时段。同一公网 IP 有多个员工授权时，frps 条目的到期时间取所有有效 grant 的最大值；管理员直接在 frps 设置的访问时段是该 IP 的全局策略，gateway 刷新 TTL 时省略 `windows`，因此不会覆盖该策略。

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
