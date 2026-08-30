# 配置参考

桌面端优先通过“设置”页面管理配置。旧 CLI 或需要版本化模板时，可复制仓库根目录的 [`config.example.yaml`](../config.example.yaml) 为本机 `config.yaml`。

## 完整示例

```yaml
opencode:
  base_url: http://127.0.0.1:4096
  directory: ""
  username: opencode
  password: ""
  allow_remote: false

watcher:
  sse: true
  polling_fallback: true
  polling_interval: 3s

handoff:
  max_output_chars: 3000
  notify_idle: true
  notify_error: true
  notify_question: true
  notify_permission: true

channel:
  type: feishu

feishu:
  app_id: ${FEISHU_APP_ID}
  app_secret: ${FEISHU_APP_SECRET}
  chat_id: ""

security:
  allowed_users: []

store:
  path: opencode-handoff.db

logging:
  level: info
```

`config.yaml` 已被 `.gitignore` 排除。`${变量名}` 会在加载 YAML 前展开；变量不存在时配置检查会失败。

## 配置优先级

运行时优先级为：

```text
环境变量 > YAML > 内置默认值
```

即使 YAML 中填写了固定值，只要存在对应的受支持环境变量，运行时就会使用环境变量。桌面设置页会把这些字段显示为只读并标出变量名。

## 环境变量

| 环境变量 | 配置字段 | 说明 |
| --- | --- | --- |
| `OPENCODE_BASE_URL` | `opencode.base_url` | OpenCode Server 地址 |
| `OPENCODE_DIRECTORY` | `opencode.directory` | 限制监听的项目绝对路径 |
| `OPENCODE_SERVER_USERNAME` | `opencode.username` | Basic Auth 用户名 |
| `OPENCODE_SERVER_PASSWORD` | `opencode.password` | Basic Auth 密码 |
| `FEISHU_APP_ID` | `feishu.app_id` | App ID |
| `FEISHU_APP_SECRET` | `feishu.app_secret` | App Secret |
| `FEISHU_CHAT_ID` | `feishu.chat_id` | 旧式手动路由的 Chat ID |
| `FEISHU_ALLOWED_USERS` | `security.allowed_users` | 逗号分隔的用户 ID |
| `FEISHU_ALLOWED_USER` | `security.allowed_users` | 单值兼容变量；复数变量优先 |
| `HANDOFF_MAX_OUTPUT_CHARS` | `handoff.max_output_chars` | 最后输出的最大字符数 |
| `HANDOFF_NOTIFY_IDLE` | `handoff.notify_idle` | 是否发送完成通知 |
| `HANDOFF_NOTIFY_ERROR` | `handoff.notify_error` | 是否发送异常通知 |
| `HANDOFF_NOTIFY_QUESTION` | `handoff.notify_question` | 是否转发 Question |
| `HANDOFF_NOTIFY_PERMISSION` | `handoff.notify_permission` | 是否转发 Permission |

布尔环境变量使用 `true` 或 `false`，`HANDOFF_MAX_OUTPUT_CHARS` 必须是正整数。

PowerShell 示例：

```powershell
$env:FEISHU_APP_ID = "cli_xxx"
$env:FEISHU_APP_SECRET = "xxx"
$env:OPENCODE_SERVER_PASSWORD = "xxx"
```

## OpenCode 连接与安全

默认配置为：

```yaml
opencode:
  base_url: http://127.0.0.1:4096
  directory: ""
  username: opencode
  password: ""
  allow_remote: false
```

- `directory` 为空时，Handoff 订阅 `/global/event` 并轮询 OpenCode 已知项目。
- `directory` 为绝对路径时，监听范围限制到单一项目；项目启动器也只展示该目录。
- `allow_remote: false` 时，只接受 `localhost` 或 loopback IP。
- 只有在可信内网确有需要时才设置 `allow_remote: true`，并同时为 OpenCode Server 启用密码。不要把 OpenCode Server 暴露到公网。

## Watcher

`watcher.sse` 控制实时事件监听，`watcher.polling_fallback` 控制轮询兜底。至少必须启用一种方式。默认两者都启用，轮询间隔为 3 秒，用于在 SSE 重连或事件遗漏时发现 Session 状态、Question 和 Permission。

## Handoff 通知

`handoff.max_output_chars` 默认是 3000，用于限制通知中最后一条 Assistant 输出。四个 `notify_*` 开关控制对应类型；Permission 默认开启。旧配置如果曾显式关闭预留开关，需要改为：

```yaml
handoff:
  notify_permission: true
```

## Feishu 路由与绑定

新安装只需要 App ID 和 App Secret，`chat_id` 可留空，然后使用 `/bind` 自动发现会话和用户身份。`feishu.chat_id` 与 `security.allowed_users` 都填写时会沿用旧式手动路由并跳过配对流程。

`security.allowed_users` 可以填写飞书用户的 `open_id`、`user_id` 或 `union_id`。配置 allowlist 后，持有配对码的用户也必须在列表中。完成配对后，非绑定用户、非绑定会话，以及无法安全映射到 Session 的消息都会被忽略。

开放平台权限和配对步骤见[飞书 / Lark 配置](feishu-setup.md)。

## 存储与日志

- `store.path` 是 SQLite 路径。相对路径按配置文件所在目录解析；首次运行会自动创建数据库和表结构。
- `logging.level` 支持 `debug`、`info`、`warn`、`error`。
- 桌面端默认把配置、数据库和日志放在 `%LOCALAPPDATA%\Agent Handoff`。

## 配置校验

旧 CLI 提供纯配置校验；该命令不会连接 OpenCode 或 Feishu：

```powershell
go run ./cmd/handoff -config ./config.yaml -check
```

配置解析使用严格字段检查。未知 YAML 字段、缺少 `${ENV}`、非法 URL、远程地址未显式放行、Watcher 全部关闭、缺少飞书凭据、非正数输出限制或不支持的日志级别都会导致校验失败。
