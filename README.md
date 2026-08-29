# OpenCode Handoff

OpenCode Handoff 是一个独立的 Human-in-the-Loop sidecar。它在 OpenCode Session 进入 idle/error、Question Tool 等待回答或 Permission 等待授权时通知飞书，并把用户回复精确送回原 Session。

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

V1.1 已支持 Question Tool、Permission Approval 和飞书交互卡片。

项目启动器支持在飞书发送 `/project`，分页查看 OpenCode 已打开的项目并创建原生 Session。创建结果会形成一条带 Session 映射的卡片；引用回复该卡片即可发送第一条任务。配置了固定 `opencode.directory` 时只展示该目录，OpenCode 的 `global /` 项目不会作为可创建目标。

运行状态快捷命令 `/running`（简写 `/r`）会跨项目汇总当前 `busy/retry` Session，显示执行中、重试、等待授权或等待回答状态，并计算距离最后一条用户消息已经过了多久。最后一次用户输入会完整保留在默认收起的折叠块中。

## 前置条件

1. 使用同一个 OpenCode Server 承载 Desktop/TUI 和 Handoff。示例：

   ```powershell
   $env:OPENCODE_SERVER_PASSWORD = '<本机密码>'
   opencode serve --hostname 127.0.0.1 --port 4096
   ```

2. 按下方“飞书开放平台配置清单”创建并发布企业自建应用。

OpenCode Server 应只监听 loopback。配置为远程地址时，程序默认拒绝启动；确需使用可信内网地址时才设置 `opencode.allow_remote: true`，并同时启用 OpenCode Server 密码。

### 飞书开放平台配置清单

在飞书开放平台的开发者后台完成以下配置。控制台显示名称可能调整，括号内的权限或事件 ID 是核对依据。

| 控制台位置 | 配置项 | 是否必需 |
| --- | --- | --- |
| 基础信息 > 凭证与基础信息 | 获取 App ID 和 App Secret | 必需 |
| 应用能力 > 添加应用能力 | 添加“机器人”能力 | 必需 |
| 权限管理 | 以应用身份发消息 (`im:message:send_as_bot`) | 必需 |
| 权限管理 | 获取用户发给机器人的单聊消息 (`im:message.p2p_msg:readonly`) | 私聊配对和回复时必需 |
| 权限管理 | 接收群聊中 @ 机器人消息 (`im:message.group_at_msg:readonly`) | 使用群聊时必需 |
| 事件与回调 > 事件配置 | 使用长连接接收事件 | 必需 |
| 事件与回调 > 事件配置 | 接收消息 (`im.message.receive_v1`) | 必需 |
| 事件与回调 > 回调配置 | 使用长连接接收回调 | Question 和 Permission 卡片必需 |
| 事件与回调 > 回调配置 | 卡片回传交互 (`card.action.trigger`) | Question 与 Permission 按钮必需 |
| 版本管理与发布 | 创建版本并发布 | 每次修改权限、事件或回调后必需 |

配置顺序：

1. 创建企业自建应用，在“添加应用能力”中启用机器人。
2. 在“权限管理”中开通上表权限。只使用私聊时不需要群聊权限；需要在群里 `/bind`、回复或操作卡片时，应同时开通群聊权限。
3. 在“事件与回调 > 事件配置”中把订阅方式设为“使用长连接接收事件”，添加 `im.message.receive_v1`。
4. 在“事件与回调 > 回调配置”中把订阅方式设为“使用长连接接收回调”，添加 `card.action.trigger`。卡片提示“该应用尚未配置卡片回调”时，可以直接点击“一键完成配置”。
5. 在“版本管理与发布”中创建并发布新版本。开发者后台顶部出现“版本发布后，当前修改方可生效”时，未发布的配置不会生效。
6. 确认应用的可用范围包含配对用户；群聊使用时把机器人加入目标群聊。
7. 启动 Handoff 后使用日志中的 `/bind <配对码>` 完成绑定。私聊直接发送；群聊中需要 @机器人后发送。

事件和回调都使用长连接，因此不需要公网 Webhook 地址，也不需要为本项目填写 Verification Token 或 Encrypt Key。App Secret 只放在本机环境变量或被 `.gitignore` 排除的 `config.yaml` 中。

Question 卡片的选项按钮、自定义答案提交和“忽略”，以及 Permission 卡片的三种授权按钮，都共用 `card.action.trigger`。如果回调未生效，仍可引用回复卡片：Question 使用选项序号、自定义文本或“忽略”，Permission 使用“允许一次”“始终允许”或“拒绝”。

## 下载与安装

Windows x64 用户可从 [GitHub Releases](https://github.com/Hans2573/OpenCode-Handoff/releases) 下载 `OpenCode-Handoff-v1.1.1-windows-amd64.zip`，解压到一个固定目录。发布包包含：

```text
OpenCode-Handoff-v1.1.1-windows-amd64/
├─ opencode-handoff.exe
├─ start-handoff.bat
├─ config.example.yaml
└─ README.md
```

将 `config.example.yaml` 复制为 `config.yaml`，然后按照下方说明填写配置。首次运行时，程序会根据 `store.path` 自动创建 SQLite 数据库和表结构，不需要预先准备或下载数据库。

也可以直接双击 `start-handoff.bat` 启动。脚本会自动使用脚本所在目录作为工作目录；如果还没有 `config.yaml`，会从示例文件创建一份并打开记事本，填写并保存后再次双击即可。程序异常退出时，脚本会保留窗口以便查看错误。

`config.yaml` 包含本机凭据，SQLite 包含飞书绑定身份、Session 和消息映射，因此二者都不会放入 Release。升级时解压新版程序并替换 `opencode-handoff.exe`，同时保留原来的 `config.yaml` 和 `opencode-handoff.db`。

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

Permission 通知默认开启。旧配置若曾显式关闭预留开关，需要改为：

```yaml
handoff:
  notify_permission: true
```

配置优先级是：**环境变量 > YAML > 内置默认值**。即使 YAML 中写了固定的 `feishu.app_id`，只要 `FEISHU_APP_ID` 存在，运行时就使用环境变量。支持自动覆盖的变量为：

- `FEISHU_APP_ID`、`FEISHU_APP_SECRET`、`FEISHU_CHAT_ID`
- `FEISHU_ALLOWED_USERS`（逗号分隔）或 `FEISHU_ALLOWED_USER`
- `OPENCODE_BASE_URL`、`OPENCODE_DIRECTORY`
- `OPENCODE_SERVER_USERNAME`、`OPENCODE_SERVER_PASSWORD`
- `HANDOFF_MAX_OUTPUT_CHARS`
- `HANDOFF_NOTIFY_IDLE`、`HANDOFF_NOTIFY_ERROR`、`HANDOFF_NOTIFY_QUESTION`、`HANDOFF_NOTIFY_PERMISSION`

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
go build -trimpath -ldflags "-s -w -X main.version=1.1.1" -o bin/opencode-handoff.exe ./cmd/handoff
```

正常闭环如下：

```text
OpenCode busy -> idle/error -> 飞书通知
                                |
                          用户引用回复
                                |
原 Session <- prompt_async <----+
```

Question Tool 使用独立闭环，不会把选择结果当作普通 prompt：

```text
OpenCode question.asked -> 飞书选项卡片 -> /question/{id}/reply -> 原 Session 继续
                                      \-> /question/{id}/reject（忽略）
```

Permission Approval 同样使用独立闭环：

```text
OpenCode permission.asked -> 飞书授权卡片 -> /permission/{id}/reply
                                                  once / always / reject
```

从飞书创建 Session：

```text
/project -> 飞书项目卡片 -> 新建 Session -> POST /session?directory=...
                                                   |
                                      Session Created 卡片
                                                   |
                                     引用回复首条任务
                                                   |
                             /session/{id}/prompt_async
```

`/project` 每页显示 8 个 OpenCode 已打开项目，可使用卡片的上一页/下一页按钮，也可发送 `/project 2` 直接查看指定页。创建按钮的目录会在执行前重新与 OpenCode 项目列表核对；飞书回调事件会持久化去重，避免事件重投创建重复 Session。

单题单选可以直接点击飞书按钮。自定义答案可引用回复卡片并输入文本；多选用逗号分隔序号，多题时每题一行。回复“忽略”“拒绝”或 `reject` 会拒绝 Question。Question 记录只允许成功处理一次，并继续使用原 `session_id`；子 agent 的 Question 与结束/中断一样不会通知。

Permission 卡片会显示权限类型、本次请求范围、具体文件目标（OpenCode 提供时）和“始终允许”范围。可点击“允许一次”“始终允许”或“拒绝”，也可引用回复对应中文、`once`、`always`、`reject`。同一 Session 并发产生多条 Permission 时会分别发送卡片；“允许一次”只处理当前卡片，确认消息会提示剩余待处理数量。选择“拒绝”会按 OpenCode 语义同时拒绝该 Session 中其他待处理权限；“始终允许”会放行后续匹配范围，操作前应核对卡片中的 pattern。Permission 只允许处理一次，子 agent 的 Permission 不会通知。

服务启动时已处于 idle 的历史 Session 不会触发完成通知；但仍处于 pending 的 Question 和 Permission 会由轮询兜底发现。唯一键为 `session_id + last_assistant_message_id + handoff_type`，SSE 与轮询同时命中也只发送一次。

每条历史 Handoff 通知都会持久映射到创建它的 `session_id`；引用回复哪条通知，就会继续对应的原 Session，即使该通知此前已经恢复过一次。飞书入站 `message_id` 会单独持久化去重，事件重投不会重复执行。

在绑定会话中，如果只有一个 OpenCode Session 等待输入，也可以直接发送普通文本继续它；服务会从 OPEN handoff 记录解析唯一 `session_id`。如果多个 Session 同时等待，则必须引用回复对应的 Handoff 通知，机器人会提示选择，避免串线。成功注入后机器人会回复“已发送到 OpenCode Session，任务正在继续”。

如需从飞书中断 Session，请在飞书中**引用对应的 Handoff 通知**，回复 `/stop`。Handoff 会根据被引用消息映射到原始 `session_id`，调用 OpenCode 的 `POST /session/{sessionID}/abort`，成功后回复“已请求中断 OpenCode Session”。未引用消息时不会猜测目标，会提示必须引用对应通知。

只有已经发送过 Handoff 通知的消息才具备 Session 映射；如果某个 Session 从未产生 Handoff 通知，不能仅凭“中断”文本安全判断要停止哪个 Session。

每条飞书 Handoff 通知都以 `🆔 Session ID` 和 OpenCode 的 `🏷️ Session Name` 开头，并用 `✅/🚨` 区分完成和中断状态、`📁` 标识项目。最后输出位于默认收起的 `💬 最后输出（3000）` 卡片面板中，括号内数字来自当前的 `handoff.max_output_chars` 配置，便于在多个通知之间快速区分并准确引用。

中断命令仅支持 `/stop`，其他文本不会触发中断。

在飞书中发送 `/project` 可查看项目并创建 Session；发送 `/running` 或 `/r` 可查看当前运行中的 Session 及其持续时间；发送 `/help` 可随时查看上述使用说明。这些命令不会发送到 OpenCode Session。

## 开发验证

```powershell
go test ./...
go vet ./...
```

测试使用 `httptest`、临时 SQLite 和 fake Channel，不连接真实 OpenCode 或飞书服务。
