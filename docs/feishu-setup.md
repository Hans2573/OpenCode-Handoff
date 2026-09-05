# 飞书 / Lark 配置

OpenCode Handoff 使用 Feishu / Lark WebSocket 长连接接收事件和卡片回调，因此不需要公网 Webhook 地址，也不需要为本项目填写 Verification Token 或 Encrypt Key。

## 创建企业自建应用

在飞书开放平台开发者后台创建企业自建应用，并添加“机器人”能力。记录 App ID 和 App Secret；App Secret 只应保存在本机环境变量或被版本控制忽略的配置文件中。

## 权限、事件与回调

控制台名称可能调整，请同时按权限或事件 ID 核对。

| 控制台位置 | 配置项 | 是否必需 |
| --- | --- | --- |
| 基础信息 > 凭证与基础信息 | 获取 App ID 和 App Secret | 必需 |
| 应用能力 > 添加应用能力 | 添加“机器人”能力 | 必需 |
| 权限管理 | 以应用身份发消息 (`im:message:send_as_bot`) | 必需 |
| 权限管理 | 获取用户发给机器人的单聊消息 (`im:message.p2p_msg:readonly`) | 私聊配对和回复时必需 |
| 权限管理 | 接收群聊中 @ 机器人消息 (`im:message.group_at_msg:readonly`) | 使用群聊时必需 |
| 事件与回调 > 事件配置 | 使用长连接接收事件 | 必需 |
| 事件与回调 > 事件配置 | 接收消息 (`im.message.receive_v1`) | 必需 |
| 事件与回调 > 回调配置 | 使用长连接接收回调 | Question / Permission 卡片必需 |
| 事件与回调 > 回调配置 | 卡片回传交互 (`card.action.trigger`) | Question / Permission 按钮必需 |
| 版本管理与发布 | 创建版本并发布 | 每次修改权限、事件或回调后必需 |

推荐配置顺序：

1. 添加机器人能力。
2. 开通所需权限。只使用私聊时不需要群聊权限；群内 `/bind`、回复或卡片操作需要群聊权限。
3. 在“事件配置”中选择长连接，添加 `im.message.receive_v1`。
4. 在“回调配置”中选择长连接，添加 `card.action.trigger`。如果卡片提示尚未配置回调，可使用控制台的“一键完成配置”。
5. 创建并发布新版本。开发者后台提示“版本发布后，当前修改方可生效”时，未发布的修改不会生效。
6. 确认应用可用范围包含配对用户；群聊使用时，把机器人加入目标群聊。

Question 的选项按钮、自定义答案提交和“忽略”，以及 Permission 的三种授权按钮，都使用 `card.action.trigger`。如果卡片回调暂时不可用，仍可引用回复卡片完成操作。

## 在 Handoff 中填写凭据

桌面端可在“设置”中填写 App ID 和 App Secret。也可以通过环境变量提供：

```powershell
$env:FEISHU_APP_ID = "cli_xxx"
$env:FEISHU_APP_SECRET = "xxx"
```

配置优先级和完整字段见[配置参考](configuration.md)。

## 首次配对

全新安装且没有旧式手动路由时，启动日志会出现类似内容：

```text
Feishu is not paired; send this command to the bot command="/bind 8A31C29F10"
```

桌面端日志位于：

```text
%USERPROFILE%\.agent-handoff\logs\agent-handoff.log
```

可用 PowerShell 查看末尾日志：

```powershell
Get-Content "$env:USERPROFILE\.agent-handoff\logs\agent-handoff.log" -Tail 100
```

将完整的 `/bind <配对码>` 发给机器人：

- 私聊：直接发送。
- 群聊：先把机器人加入群聊，然后 @机器人并发送。

机器人提示配对成功后，`chat_id` 和用户的 `open_id / user_id / union_id` 会持久化到 SQLite，重启无需再次配对。配对码由本机安全随机生成，仅显示在本机日志中。配置了 `security.allowed_users` 时，配对用户还必须位于 allowlist 中。

## 使用 Handoff 消息

每条 Handoff 通知都映射到创建它的 OpenCode `session_id`。引用回复哪条通知，就继续哪条原 Session；历史通知即使已经恢复过一次，仍保留其 Session 映射。飞书入站 `message_id` 会持久化去重，事件重投不会重复执行。

如果绑定会话中只有一个 Open Handoff，可以直接发送普通文本继续它；多个 Session 同时等待时必须引用对应通知，避免串线。成功注入后，机器人会确认任务已发送到 OpenCode Session。

通知卡片会在顶部显示 `Session ID` 和 OpenCode 的 Session Name，用完成 / 中断状态和项目字段帮助区分并发任务。最终答复的末尾预览位于默认收起的面板中，面板标题会显示当前 `handoff.max_output_chars` 限制。预览被截断时，可以点击“查看详细答复”，机器人会回复一个包含全部最终正文的折叠块；详情不会包含 reasoning、工具调用、执行命令或工具输出，也不会发送完整 Conversation。

### Finished 与 Error

引用回复通知并输入下一步要求，文本会通过 `prompt_async` 成为原 Session 的真实 User Message。OpenCode Desktop / TUI 会显示同一条连续历史。

### Question

- 单题单选可以直接点击按钮。
- 自定义答案可以引用回复卡片并输入文本。
- 多选使用逗号分隔序号。
- 多题时每题一行。
- 回复“忽略”“拒绝”或 `reject` 会拒绝 Question。

Question 只允许成功处理一次，并调用 OpenCode Question API，而不是创建普通 Prompt。

### Permission

Permission 卡片会显示权限类型、请求范围、OpenCode 提供的文件目标和“始终允许”范围。可以点击或引用回复：

- `允许一次` / `once`
- `始终允许` / `always`
- `拒绝` / `reject`

同一 Session 并发产生的 Permission 会分别发送卡片。“允许一次”只处理当前卡片；“拒绝”会按 OpenCode 语义同时拒绝该 Session 中其他待处理权限；“始终允许”会放行后续匹配范围，操作前应核对卡片中的 pattern。

## 机器人命令

以下命令只由 Handoff 处理，不会作为普通消息发送给 OpenCode：

| 命令 | 作用 |
| --- | --- |
| `/project [页码]` | 每页查看 8 个已打开项目，选择模型和档位并显式创建 OpenCode Session |
| `/models [页码或关键词]` | 每页查看 6 个脱敏模型；可按 Provider、名称、ID 或档位搜索 |
| `/running`、`/r` | 汇总最多 20 个 `busy / retry` Session，并标识 Question / Permission 等待状态 |
| `/stop` | 中断被引用通知所映射的 Session |
| `/help` | 查看机器人使用说明 |

`/project` 是显式创建 Session 的入口，也是普通 Handoff 不创建新 Session 这一规则的唯一产品级例外。创建结果会形成带 Session 映射的卡片，引用回复首条任务时才真正把选定模型和 variant 交给 OpenCode。固定配置 `opencode.directory` 时只显示该目录；OpenCode 的 `global /` 项目不会作为可创建目标。

`/models <关键词>` 支持模糊搜索。最近使用记录仅保存在本机 SQLite，最多 20 条。Handoff 只解析 Provider、模型、能力和档位等脱敏字段，不会把 Provider 响应中的 API Key 或连接参数发送到飞书。对运行中 Session 选择模型不会中断当前执行，新模型从下一条经飞书发送的普通任务开始生效。

`/models` 首页会展示最近使用模型和按 Provider 分组的入口。飞书环境支持表单时，创建或切换 Session 的模型首页还会显示搜索框并保留当前 Session 上下文；如果飞书拒绝表单，卡片会自动降级，但 Provider 分组和 `/models <关键词>` 仍可使用。

`/running` 会跨项目汇总当前 `busy / retry` Session，显示执行中、重试、等待回答或等待授权状态、当前模型，以及距离最后一条用户消息经过的时间。最后一次用户输入会完整保留在默认收起的折叠块中。该命令只读，不会创建、恢复或中断 Session。

使用 `/stop` 时必须引用对应的 Handoff 通知；未引用时服务不会猜测目标。只有已发送过 Handoff 的消息具备 Session 映射，从未产生通知的 Session 无法仅凭文本安全中断。

## 故障排查

- 收不到任何消息：确认应用版本已发布、机器人可用范围正确、长连接事件已启用。
- 能收到通知但按钮无响应：确认回调使用长连接且已添加 `card.action.trigger`，修改后重新发布版本。
- 群聊中无响应：确认机器人已进群、群聊权限已开通，并使用 @机器人。
- 配对失败：确认命令完整、配对码来自当前本机日志，并检查 allowlist。
