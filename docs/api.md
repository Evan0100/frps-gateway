# frps Whitelist API

## Overview

白名单接口属于 frps dashboard API v2，由 frps 实现，`frps-gateway` 作为调用方。接口只管理当前生效的 `IP + expireAt`；飞书用户、申请和审计数据不进入 frps。

| Module | Method | Path | Summary | Auth | Status | Updated |
|---|---|---|---|---|---|---|
| Whitelist | DELETE | `/api/v2/whitelist` | 删除一个 IP | Basic Auth | Added | 2026-09-02 |
| Whitelist | GET | `/api/v2/whitelist` | 查询当前生效条目 | Basic Auth | Added | 2026-09-02 |
| Whitelist | POST | `/api/v2/whitelist` | 添加 IP 或刷新有效期,可选设置访问时段 | Basic Auth | Modified | 2026-09-03 |
| Whitelist | GET | `/api/v2/whitelist/status` | 查询拦截开关、默认 TTL 和服务器时间 | Basic Auth | Modified | 2026-09-03 |

三个管理接口已在 frps 中实现并完成自动化测试;自 Phase 3 起 `whitelist.enabled = true` 即在连接入口执行拦截。frps 同时在 `GET /whitelist` 内置了一个使用这些接口的管理页面(同一 Basic Auth)。

## Enforcement

- `whitelist.enabled = false`(默认)时行为与原版 frps 完全一致,接口仍可用于预填名单。
- `whitelist.enabled = true` 且名单为空时拒绝全部受支持的业务连接(fail-closed)。
- 允许条件:`IP 在名单 && 条目未过期 && 当前服务器时间在条目允许时段内`。条目无时段时全天可访问。
- 拦截点:TCP/HTTPS/TCPMux/STCP/SUDP visitor 连接在申请 work connection 之前拒绝;HTTP vhost 请求直接返回 403。拦截发生在连接建立/请求时,窗口外不主动断开已建立的连接。
- 不拦截:UDP/SUDP 按包来源、XTCP 打洞直连;STCP/SUDP visitor 连接上校验的是 visitor frpc 主机 IP 而非最终用户 IP。

## Common Behavior

- 管理 API 复用 `webServer.user` 和 `webServer.password` 的 Basic Auth。
- 生产环境只允许通过本机回环地址或受控管理内网访问，远程访问必须使用 HTTPS 或安全隧道。
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
      "windows": [
        { "days": [1, 2, 3, 4, 5], "start": "09:00", "end": "18:00" }
      ]
    }
  ]
}
```

`windows` 为空或省略表示全天可访问。`days` 为 ISO 星期编号,1=周一…7=周日;`start`/`end` 为 `HH:MM`(服务器本地时间),`start > end` 表示跨午夜(如 22:00–06:00);区间为半开 `[start, end)`,即到点即拒。

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
    "serverTime": 1788399316
  }
}
```

`defaultTTL` 为 Go `time.Duration` 字符串形式;`serverTime` 为服务器当前时间的 Unix 秒,管理页面用它计算时段内外状态,避免浏览器时区与服务器不一致造成误判。该接口为纯只读查询,用于管理页面和管理端展示当前拦截状态,避免在 `whitelist.enabled = false` 时误以为拦截已生效。

#### Error Responses

| Status | Code | Message | Reason |
|---|---:|---|---|
| 401 | - | `Unauthorized` | Basic Auth 缺失或错误 |
| 500 | 500 | 实际内部错误 | 服务端内部错误 |

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
```

服务端自动化测试必须覆盖成功、无鉴权、错误鉴权、非法 JSON、非法 IP、非法 TTL、重复添加刷新和删除不存在条目；拦截行为另由 e2e 测试覆盖默认拒绝、加白放行、过期失效和关闭开关回退。
