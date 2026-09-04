# frps-gateway

通过**飞书机器人**管理 frps IP 白名单的 sidecar 服务。

员工在飞书里发送 `/start` 即可打开动态操作卡片；可以查看本人的有效授权、通过按钮撤销授权，或按卡片提示手动提交公网 IP。网关将授权写入 SQLite，并调用 frps 管理 API 更新白名单；只有白名单内的来源 IP 才能访问 frps 映射的端口。

```
飞书消息（员工） → 一次性 HTTPS 授权链接 → SQLite 用户授权
    │ WebSocket 长连接（官方 SDK，出站连接，无需公网地址）
    ▼
frps-gateway ──HTTP + Basic Auth──▶ frps /api/v2/whitelist（持久化白名单，连接时校验）
```

> frps 白名单、飞书机器人、一次性链接和用户授权记录已经形成可运行闭环。公网 HTTPS 证书、真实飞书凭据、云防火墙及反向代理仍属于部署工作。

## 指令

| 指令 | 别名 | 说明 |
|---|---|---|
| `/start` | `开始` / `菜单` | 打开动态操作卡片 |
| `申请授权 1.2.3.4 4h` | 无 | 为指定公网 IP 添加临时授权 |
| `我的授权` | 无 | 列出本人有效授权，也可直接点击卡片按钮 |
| `撤销授权 1.2.3.4` | 无 | 撤销本人的指定授权，也可直接点击卡片按钮 |

`/start` 卡片中的“申请授权”暂时显示手动输入格式；“我的授权”直接展示当前记录；“撤销授权”列出每个 IP 的确认按钮。原有的申请访问、我的访问、撤销、加白、删白、白名单、help 及英文命令均不再接受。

生产模式建议 `allowAllUsers = true`，并在飞书开放平台将应用可用范围限制为公司成员。群聊默认必须 @机器人，程序只接受 `mentioned_type=bot` 的提及。

- `adminChatID`：仅在 `allowAllUsers = false` 时使用，指定一个允许操作机器人的飞书群；

不在允许范围内的消息一律忽略并记日志。支持 IPv4 / IPv6 单个地址。

## 快速开始

前置条件：frps 侧白名单功能已部署（定制 frp fork），并开启了 webServer（dashboard）。

```bash
# 构建
go build -o frps-gateway .

# 配置
cp bot.toml.example bot.toml
# 编辑 bot.toml：填 frps 地址与账号、飞书 AppID/AppSecret

# 运行
./frps-gateway -c bot.toml
```

## 飞书应用配置（阶段一：个人测试）

1. 注册飞书账号，创建一个自己的团队（免费，个人即可，你自动成为管理员）；
2. 到[飞书开放平台](https://open.feishu.cn)创建**企业自建应用**，开启「机器人」能力；
3. **权限管理**开通：接收消息（`im:message`）、以机器人身份发送消息（`im:message:send_as_bot`）；
4. **事件与回调** > 事件配置选择**使用长连接接收**，添加事件「接收消息 `im.message.receive_v1`」；回调配置也选择长连接，并添加「卡片回传交互 `card.action.trigger`」；
5. **可用范围**设为自己（测试阶段）；发布版本生效（自建应用无需飞书审核）；
6. 「凭证与基础信息」页复制 **App ID / App Secret** 填入 `bot.toml`；
7. 测试时可设置 `allowAllUsers = true`；如只允许指定群使用，则设置为 `false` 并填写 `adminChatID`。

运行 bot 的机器只需能访问公网（长连接为出站方向），无需开放任何入站端口。

## 阶段二：迁移到公司租户

在公司飞书租户新建自建应用（同上步骤，走企业管理员审批发布），然后：

- 替换 `bot.toml` 中的 `appID` / `appSecret` 为公司应用的值；
- 如使用指定群模式，将 `adminChatID` 换成公司租户内的群 ID；
- 重启 bot 即完成迁移，零代码改动。

## 配置说明

见 `bot.toml.example` 内注释。要点：

| 配置 | 说明 |
|---|---|
| `[frps] apiAddr/user/password` | frps webServer 地址与 Basic Auth 账号 |
| `[feishu] appID/appSecret` | 飞书自建应用凭证 |
| `[bot] allowAllUsers/adminChatID` | 全员使用开关与可选的指定群限制 |
| `[bot] defaultTTL` | `申请授权` 不带时长时的默认有效期 |
| `[bot] maxTTL/maxActiveIPs` | gateway 强制的 24h 上限和每人 3 个有效 IP 上限 |
| `[bot] enabled` | 紧急停用飞书长连接；不影响授权页处理已有链接 |
| `[server]` | 授权页监听地址、HTTPS 公网地址及可信反向代理 |
| `[storage] sqliteFile` | 用户授权、一次性令牌、消息幂等及回复 outbox 数据库 |

网关提供 `/healthz`（进程存活）和 `/readyz`（SQLite、frps API 就绪）。飞书事件通过 SQLite inbox 去重并恢复，回复先进入 outbox 再发送；白名单每 30 秒自动对账一次。

`bot.toml` 含密钥，已在 `.gitignore` 中忽略。
生产环境推荐通过 `passwordEnv` / `passwordFile` 和 `appSecretEnv` / `appSecretFile` 外置密钥，避免把明文放进 TOML。完整上线和回滚步骤见 [`docs/deployment-runbook.md`](./docs/deployment-runbook.md)。

## 开发

```bash
go test ./...
go vet ./...
```

包结构还包括 `store`（SQLite 授权和幂等）、`authorize`（一次性 HTTPS 授权页与可信代理解析）。

## 项目文档

- [`PROJECT.md`](./PROJECT.md)：项目定位和已确定的架构决策
- [`ROADMAP.md`](./ROADMAP.md)：进度、阶段任务和完成标准
- [`docs/api.md`](./docs/api.md)：frps 白名单 API 契约
- [`docs/feishu-command-design.md`](./docs/feishu-command-design.md)：飞书指令、动态卡片及未来按服务授权设计
- [`docs/ip-whitelist-requirements.md`](./docs/ip-whitelist-requirements.md)：详细需求与验收标准
- [`docs/whitelist-review-2026-09-03.md`](./docs/whitelist-review-2026-09-03.md)：最近五个 frp 提交的专项审查与修复记录
- [`docs/security-audit-2026-09-03.md`](./docs/security-audit-2026-09-03.md)：frps-gateway、发布配置、依赖和内网穿透暴露面的安全审计
