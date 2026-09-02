# frps Whitelist API

## Overview

白名单接口属于 frps dashboard API v2，由 frps 实现，`frps-gateway` 作为调用方。接口只管理当前生效的 `IP + expireAt`；飞书用户、申请和审计数据不进入 frps。

| Module | Method | Path | Summary | Auth | Status | Updated |
|---|---|---|---|---|---|---|
| Whitelist | DELETE | `/api/v2/whitelist` | 删除一个 IP | Basic Auth | Added | 2026-09-02 |
| Whitelist | GET | `/api/v2/whitelist` | 查询当前生效条目 | Basic Auth | Added | 2026-09-02 |
| Whitelist | POST | `/api/v2/whitelist` | 添加 IP 或刷新有效期 | Basic Auth | Added | 2026-09-02 |

三个接口已经在 frps 中实现并完成 Phase 2 自动化测试；连接拦截将在 Phase 3 接入。

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

## Whitelist

### GET /api/v2/whitelist

**Summary:** 查询当前全部未过期白名单条目

**Auth:** Basic Auth required

**Status:** Added

**Updated:** 2026-09-02

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
      "expireAt": 1788350400
    }
  ]
}
```

#### Error Responses

| Status | Code | Message | Reason |
|---|---:|---|---|
| 401 | - | `Unauthorized` | Basic Auth 缺失或错误 |
| 500 | 500 | 实际内部错误 | 服务端内部错误 |

### POST /api/v2/whitelist

**Summary:** 添加 IP；如果 IP 已存在则从当前时间重新计算 TTL

**Auth:** Basic Auth required

**Status:** Added

**Updated:** 2026-09-02

#### Request Body

| Field | Type | Required | Description |
|---|---|---|---|
| `ip` | string | Yes | 单个合法 IPv4 或 IPv6 地址 |
| `ttl` | string | No | 正数时长，支持 `d/h/m/s` 组合；省略时使用服务端默认 TTL |

```json
{
  "ip": "1.2.3.4",
  "ttl": "2h"
}
```

#### Success Response

新建和刷新均返回 HTTP 200：

```json
{
  "code": 200,
  "msg": "success",
  "data": {
    "ip": "1.2.3.4",
    "expireAt": 1788350400
  }
}
```

#### Error Responses

| Status | Code | Message | Reason |
|---|---:|---|---|
| 400 | 400 | `invalid request body` | JSON 缺失、损坏或包含错误字段类型 |
| 400 | 400 | `invalid ip` | IP 缺失或格式非法 |
| 400 | 400 | `invalid ttl` | TTL 非正数或格式非法 |
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
```

服务端自动化测试必须覆盖成功、无鉴权、错误鉴权、非法 JSON、非法 IP、非法 TTL、重复添加刷新和删除不存在条目。
