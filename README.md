# OpenCode Handoff

OpenCode Handoff 是一个独立的 Human-in-the-Loop sidecar。它只在 OpenCode Session 从运行态进入 idle 或 error 时通知飞书；用户引用回复通知后，内容会作为真实 User Message 写回原 Session。

V1.0 已实现：

- OpenCode SSE 监听与 3 秒轮询兜底
- idle / error handoff 检测
- 最后一条 assistant 输出提取与长度限制
- 飞书长连接收消息，无需公网 Webhook
- 通过一次性配对码自动发现并持久化飞书会话和用户身份
- 飞书引用消息到 OpenCode Session 的精确映射
- `prompt_async` 恢复原 Session
- SQLite 持久化、通知去重、回复幂等
- 多 Session 与多项目目录支持
- 自动忽略带 `parentID` 的子 agent Session
- 飞书用户 allowlist 和 OpenCode loopback 地址保护

Question Tool、Permission Approval 和交互卡片属于 V1.1，配置字段与核心类型已预留，但 V1.0 不会处理它们。

## 前置条件

1. 使用同一个 OpenCode Server 承载 Desktop/TUI 和 Handoff。示例：

   ```powershell
   $env:OPENCODE_SERVER_PASSWORD = '<本机密码>'
   opencode serve --hostname 127.0.0.1 --port 4096
   ```

2. 在飞书开放平台创建企业自建应用并启用机器人。
3. 为应用开通发送/接收消息所需权限，至少包含 `im:message:send_as_bot`，并订阅 `im.message.receive_v1` 事件。
4. 事件订阅方式选择“使用长连接接收事件”，发布应用版本，并把机器人加入目标群聊。

OpenCode Server 应只监听 loopback。配置为远程地址时，程序默认拒绝启动；确需使用可信内网地址时才设置 `opencode.allow_remote: true`，并同时启用 OpenCode Server 密码。

## 配置

以 [config.example.yaml](./config.example.yaml) 为模板创建本机 `config.yaml`。新安装只需要提供飞书应用凭据：

```yaml
feishu:
  app_id: cli_xxx
  app_secret: xxx
  chat_id: ""

security:
  allowed_users: []
```

`config.yaml` 已被 `.gitignore` 排除。更推荐把敏感值通过环境变量注入：

```powershell
$env:FEISHU_APP_ID = 'cli_xxx'
$env:FEISHU_APP_SECRET = 'xxx'
$env:OPENCODE_SERVER_PASSWORD = 'xxx'
```

配置优先级是：**环境变量 > YAML > 内置默认值**。即使 YAML 中写了固定的 `feishu.app_id`，只要 `FEISHU_APP_ID` 存在，运行时就使用环境变量。支持自动覆盖的变量为：

- `FEISHU_APP_ID`、`FEISHU_APP_SECRET`、`FEISHU_CHAT_ID`
- `FEISHU_ALLOWED_USERS`（逗号分隔）或 `FEISHU_ALLOWED_USER`
- `OPENCODE_BASE_URL`、`OPENCODE_DIRECTORY`
- `OPENCODE_SERVER_USERNAME`、`OPENCODE_SERVER_PASSWORD`

YAML 中也可以显式使用 `${任意变量名}`；这种写法要求该变量存在，否则配置检查会失败。

### 首次配对

首次启动且没有旧式手动路由配置时，终端会显示：

```text
Feishu is not paired; send this command to the bot command="/bind 8A31C29F10"
```

在希望接收通知的飞书私聊中直接发送该命令；在群聊中先加入机器人，然后 @机器人并发送该命令。机器人回复“OpenCode Handoff 配对成功”后，会话的 `chat_id` 和配对用户的 `open_id/user_id/union_id` 会自动保存到 SQLite，后续重启无需再次配置或配对。

配对码由本机安全随机生成，仅显示在本机日志中。未持有配对码的用户无法绑定。如果还配置了 `security.allowed_users`，配对用户必须同时位于 allowlist 中。

`feishu.chat_id` 和 `security.allowed_users` 仍作为旧式手动配置保留；两者都填写时会跳过配对流程。一般不需要再手动获取它们。

如需额外限制配对人，`security.allowed_users` 可填写用户的 `open_id`、`user_id` 或 `union_id`。配对完成后，非绑定用户、非绑定会话以及没有引用原通知的消息都会被忽略。

默认 `opencode.directory` 为空，此时 Handoff 订阅 `/global/event` 并轮询 OpenCode 已知项目。设置具体绝对路径可把监听范围限制到单一项目。

先做纯配置校验；该命令不会连接 OpenCode 或飞书：

```powershell
go run ./cmd/handoff -config ./config.yaml -check
```

## 运行

```powershell
go run ./cmd/handoff -config ./config.yaml
```

构建独立二进制：

```powershell
go build -trimpath -ldflags "-s -w -X main.version=1.0.0" -o bin/opencode-handoff.exe ./cmd/handoff
```

正常闭环如下：

```text
OpenCode busy -> idle/error -> 飞书通知
                                |
                          用户引用回复
                                |
原 Session <- prompt_async <----+
```

服务启动时已处于 idle 的历史 Session 不会触发通知；只有本次进程观察到从非 idle 到 idle 的状态变化才会创建 handoff。唯一键为 `session_id + last_assistant_message_id + handoff_type`，SSE 与轮询同时命中也只发送一次。

每条历史 Handoff 通知都会持久映射到创建它的 `session_id`；引用回复哪条通知，就会继续对应的原 Session，即使该通知此前已经恢复过一次。飞书入站 `message_id` 会单独持久化去重，事件重投不会重复执行。

在绑定会话中，如果只有一个 OpenCode Session 等待输入，也可以直接发送普通文本继续它；服务会从 OPEN handoff 记录解析唯一 `session_id`。如果多个 Session 同时等待，则必须引用回复对应的 Handoff 通知，机器人会提示选择，避免串线。成功注入后机器人会回复“已发送到 OpenCode Session，任务正在继续”。

每条飞书 Handoff 通知都以 `🆔 Session ID` 和 OpenCode 的 `🏷️ Session Name` 开头，并用 `✅/🚨` 区分完成和中断状态、`📁` 标识项目。最后输出位于默认收起的 `💬 最后输出（3000）` 卡片面板中，括号内数字来自当前的 `handoff.max_output_chars` 配置，便于在多个通知之间快速区分并准确引用。

## 开发验证

```powershell
go test ./...
go vet ./...
```

测试使用 `httptest`、临时 SQLite 和 fake Channel，不连接真实 OpenCode 或飞书服务。
