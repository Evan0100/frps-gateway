# frps-gateway 上线、回滚与密钥管理手册

## 1. 上线边界

- 公网只开放业务所需端口和 TCP 443；不得公开 frps dashboard/API 端口。
- frps dashboard/API 与 gateway 授权服务均监听 `127.0.0.1`。
- 由现有 Nginx/Caddy 在 443 终结 TLS并自动续期证书，再反向代理到 `127.0.0.1:8080`。
- `trustedProxyCIDRs` 只配置实际反向代理地址，单机部署通常为 `127.0.0.0/8`，禁止配置 `0.0.0.0/0`。
- 对外 HTTP 服务仍应逐个配置 frpc 的 `httpUser/httpPassword`，它与 IP 白名单互相独立，尤其适用于公司 NAT、酒店和 CGNAT 等共享出口。

## 2. 发布前检查

1. 分别提交 `frp` 和 `frps-gateway` 的代码，记录两个提交 ID；工作区必须没有未确认改动。
2. 使用仓库声明的 Go 安全工具链执行：`go test ./...`、`go vet ./...`、`govulncheck ./...`。最低版本以漏洞扫描和官方安全公告为准，不写死为长期固定版本。
3. 使用相同提交构建 frps、frpc 和 frps-gateway，并记录文件 SHA-256。
4. 在测试机验证：空名单拒绝、授权放行、过期失效、本人撤销、共享 IP 撤销、第四个 IP 被拒绝、重复令牌返回 410、frps 重启恢复、gateway 在命令执行及回复发送前后重启仍能恢复。

## 3. 密钥

- `FRPS_GATEWAY_FRPS_PASSWORD`：frps dashboard/API Basic Auth 密码。
- `FRPS_GATEWAY_FEISHU_APP_SECRET`：飞书 AppSecret。
- 推荐由 systemd `EnvironmentFile` 或权限为 `0600` 的独立文件提供；服务配置和代码仓库不得包含真实值。
- frps 通信 token、上述密码和 AppSecret 使用不同随机值。发生主机入侵、人员离职或疑似泄露时立即轮换；常规至少每 90 天复核并轮换高权限凭据。
- 一次性授权链接不使用长期签名密钥：令牌来自系统加密随机源，SQLite 只保存 SHA-256，因此没有需要保存的签名密钥。
- 飞书长连接由官方 SDK 建立并鉴权；如果飞书应用启用了事件加密/校验，可通过 `encryptKeyEnv/File` 和 `verificationTokenEnv/File` 配置，并按 AppSecret 的方式保护。
- SQLite 当前不做应用层加密。依靠磁盘加密、目录权限、备份权限和主机隔离保护；其中含 open_id、IP 和操作时间，应按内部敏感日志管理。

## 4. 备份

部署或升级前保存到仅管理员可读的带时间戳目录：

- 当前 frps、frpc、frps-gateway 二进制及其 SHA-256；
- frps 配置和 `whitelist.storageFile`；
- gateway 配置、SQLite 主文件及存在时的 `-wal`、`-shm` 文件；
- systemd 服务文件和 Nginx/Caddy 配置。

备份 SQLite 前优先停止 gateway，或使用 SQLite 在线备份命令，避免只复制主文件而遗漏 WAL。至少保留最近 7 份每日备份，并定期在隔离目录做恢复验证。

## 5. 部署顺序

1. 先部署包含状态持久化改动的新 frps；dashboard/API 保持本机监听，确认 Basic Auth 和通信 token 已配置。
2. 启动 frps，检查状态文件可写，使用本机管理 API完成增删和重启恢复测试。
3. 部署 gateway，配置公司 `tenantKey`、默认 4h、最大 24h、每人最多 3 个有效 IP、`enabled=true`。
4. 启动 gateway，确认 `/healthz` 返回 200，且 `/readyz` 在 SQLite 和 frps 正常时返回 200、停止 frps 后返回 503；两个端点仅供反向代理或监控访问。
5. 在公司飞书中完成一次完整授权，并验证 SQLite 用户记录、frps 条目和实际业务访问一致。
6. 检查云安全组：443 和明确需要的业务端口开放；frps 管理端口、gateway 8080 不对公网开放。

## 6. 快速停用与事故处置

- 机器人异常：将 `bot.enabled=false` 后重启 gateway。飞书长连接不会建立，授权页仍能处理停用前已签发且未过期的链接。若需完全停止授权，同时停止 gateway 或在反向代理禁用授权路由。
- 怀疑令牌链接泄露：停止 gateway 或临时关闭授权路由；一次性令牌最多在 `linkTTL` 内有效。
- 怀疑服务器入侵：隔离主机、保留日志与磁盘证据、轮换 frp token、Basic Auth、飞书 AppSecret及所有可能接触过的业务凭据，不要只修改白名单。

## 7. 回滚

1. 停止 gateway 和 frps，保存故障版本的数据库、状态文件和日志用于排查。
2. 恢复上一版本二进制与对应配置。状态文件版本不兼容时，恢复部署前备份，不让旧程序直接改写新格式。
3. 启动 frps，确认 dashboard 仅本机可达、通信认证正常，再启动 gateway。
4. 验证白名单、业务访问、飞书回复和授权页；确认无误后再恢复外部流量。
5. 回滚后不得删除故障数据；记录原因、时间、提交 ID 和处理人。

## 8. 共享 IP 与访问时段

- 员工自助授权只能设置 TTL，不允许设置 `windows`。
- 同一 IP 的多个有效授权取最大到期时间；某员工撤销后，只在该 IP 不再有任何有效授权时删除 frps 条目。
- frps 管理员页面设置的 `windows` 是该公网 IP 的全局策略。gateway 调整 TTL 时不发送 `windows`，保留管理员策略。
- 需要按员工而非公网 IP 区分时段或权限的敏感服务，应使用独立账号认证、VPN或零信任访问，不应依赖共享 IP 白名单。
