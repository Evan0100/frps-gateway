# frps 白名单专项审查与修复记录

## 审查范围

本次检查覆盖 `frp` 仓库截至 2026-09-03 的最近五个白名单提交：内存存储与管理 API、连接入口拦截、管理页面、访问时段和访问记录。审查重点是鉴权边界、重启行为、审计可追溯性、跨时区显示及配置关闭后的兼容行为。

## 问题与处理结果

### 1. 管理接口可能在未认证状态下暴露（P1，已修复）

原实现复用 dashboard Basic Auth，但 frp 在用户名和密码都为空时会跳过认证。如果管理员把 webServer 监听到外部地址却漏配凭据，攻击者可以调用管理 API 将自己的 IP 加入白名单。

处理：当 `whitelist.enabled = true` 时，配置校验强制要求 `webServer.port`、`webServer.user`、`webServer.password` 和 `whitelist.storageFile`。管理端口仍建议只监听 `127.0.0.1` 或受控管理网络，跨主机调用应使用 HTTPS 或安全隧道。

### 2. frps 重启后名单清空并导致业务全部拒绝（P1，已修复）

原白名单只存在于进程内存中。启用 fail-closed 后，frps 重启会以空名单启动，所有受支持的业务连接都会被拒绝，而原计划负责恢复名单的 gateway 闭环当前又被推迟。

处理：新增 `whitelist.storageFile`，默认值为 `./whitelist-state.json`。每次成功添加、延期或删除均通过同目录临时文件和原子替换保存；启动时恢复未过期条目，非法或不兼容的状态文件会阻止 frps 带着错误权限状态启动。状态文件请求以 `0600` 权限写入；Linux/macOS 上仅允许进程账号读写，Windows 部署还应通过目录 ACL 限制访问。

### 3. 访问记录不能替代白名单变更审计（P1，已修复当前阶段范围）

原访问记录是可覆盖的内存环形缓冲，只记录业务流量的放行或拒绝；其中 `user` 是代理所属 frpc 用户，不是执行白名单操作的人，也没有记录添加、延期和删除。

处理：保留访问记录作为运行诊断日志，另增持久化的白名单变更审计。审计记录顺序号、时间、操作类型、IP、Basic Auth 管理账号、管理请求来源 IP、到期时间和访问时段；新增 `GET /api/v2/whitelist/auditlog` 并在管理页面展示。状态文件默认保留最近 5000 次成功变更。

当前限制：Basic Auth 只有一个配置账号，因此目前只能追溯到管理账号和来源地址，不能唯一识别共享账号背后的自然人。后续飞书 gateway 仍需把飞书 open_id、消息 ID 和申请上下文写入 SQLite 长期审计。

### 4. 分散用户跨时区查看时，页面状态可能与服务端相反（P2，已修复）

原页面用 Unix 时间戳校正浏览器时间，但 JavaScript 仍用浏览器时区计算星期和小时。浏览器与 frps 不在同一时区时，页面可能显示“可访问”，而服务端实际判定为“时段外”，反之亦然。

处理：status 接口增加 `serverTimeRFC3339` 和 `serverUTCOffset`；名单条目增加由 frps 直接计算的 `allowedNow`。页面使用服务器 UTC 偏移显示时间，并以服务端状态为准，5 秒刷新一次。

### 5. 关闭访问记录后页面持续出现 500 错误（P2，已修复）

当 `whitelist.accessLogSize` 为负数时，原接口因日志对象不存在返回 500，而页面仍每 5 秒请求一次并重复提示失败。

处理：status 接口增加 `accessLogEnabled` 和 `accessLogSize`；访问记录关闭时查询接口返回 HTTP 200 和空数组，页面明确显示“访问记录已关闭”。同时把两个日志查询接口的 `limit` 限制为 0–5000。

## 保留的能力边界

以下属于已知范围限制，不是本次回归缺陷：

- UDP 按包来源和 XTCP 打洞直连当前不拦截。
- STCP/SUDP visitor 校验的是 visitor frpc 主机 IP，而不是该主机后面的最终用户 IP。
- TCP、HTTPS、TCPMux、STCP 和 SUDP 已建立的长连接不会因为条目删除、过期或进入禁止时段而被主动断开；限制作用于新连接，HTTP vhost 每个请求都会重新检查。
- frps 本地变更审计只保存成功操作和管理账号；失败尝试、飞书用户身份、消息去重和长期留存仍由后续 gateway 审计实现。

## API 与配置变化

- `GET /api/v2/whitelist`：新增 `allowedNow`、创建/更新时间、管理账号和操作来源字段。
- `POST /api/v2/whitelist`：成功后持久化名单和变更审计。
- `DELETE /api/v2/whitelist`：成功后持久化删除和变更审计。
- `GET /api/v2/whitelist/status`：新增服务器 RFC3339 时间、UTC 偏移及访问日志状态。
- `GET /api/v2/whitelist/accesslog`：日志关闭时返回空数组；`limit` 最大 5000。
- `GET /api/v2/whitelist/auditlog`：新增，返回持久化变更记录，最新在前。
- `whitelist.storageFile`：新增，默认 `./whitelist-state.json`。

详细请求和响应格式见 [API 契约](./api.md)。

## 验证清单

- 已通过：`go test ./client/... ./server/... ./pkg/...` 全量单元测试。
- 已通过：`go vet ./server/... ./pkg/...` 静态检查。
- 已覆盖：持久化与重启恢复、审计顺序、配置校验、status 新字段、审计查询、关闭访问日志、TTL、IPv4/IPv6 标准化、访问时段、并发、入口拦截和管理页面。
- 未在当前 Windows 环境完成：白名单 TCP/HTTP 专项端到端测试。新构建的 `frps.exe` 被 Windows Defender 识别为潜在不需要的应用并阻止执行；这是测试环境阻塞，不代表用例通过或失败，发布前需在允许运行自建 frp 二进制的隔离环境补跑。
