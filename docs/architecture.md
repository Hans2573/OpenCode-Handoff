# 架构说明

## 产品边界

OpenCode Handoff 是 OpenCode 的 Human-in-the-Loop Runtime Companion，不是 Feishu / Lark 聊天客户端。

```text
OpenCode 执行
    ↓
Finished / Question / Permission / Error
    ↓
OpenCode Handoff
    ↓
Feishu / Lark
    ↓
用户回复 / 授权
    ↓
原 OpenCode Session 继续
```

OpenCode Desktop / TUI 是主工作界面，OpenCode Session 是唯一事实源。Handoff 不控制 UI、不模拟键盘、不维护第二套 Conversation，也不转发 Thinking、Tool Call 或 Token Stream。

显式的 `/project` 项目启动器可以通过 OpenCode 原生 API 新建 Session；除此以外，普通消息、历史通知回复和事件重投都不能隐式创建 Session。

## 进程与组件

```text
┌──────────────────────────────┐
│ OpenCode Server              │
│ Session / Message / Event    │
│ Question / Permission        │
└──────────────┬───────────────┘
               │ HTTP / SSE
               ▼
┌──────────────────────────────┐
│ OpenCode Handoff             │
│                              │
│ Desktop Manager              │
│ Runtime Service              │
│ Watcher → Engine             │
│ OpenCode Adapter             │
│ Channel Adapter              │
│ SQLite Store                 │
└──────────────┬───────────────┘
               │ WebSocket
               ▼
┌──────────────────────────────┐
│ Feishu / Lark                │
│ Notification / Reply / Card  │
└──────────────────────────────┘
```

### Desktop shell

`main.go` 创建 Wails 应用、主窗口、系统托盘和单实例行为，并嵌入 `frontend/dist`。`appservice.go` 把 Dashboard、设置、项目路由、事件导出、打开 Session、自启动和退出等操作暴露给 React。

`internal/desktop.Manager` 负责路径迁移、SQLite、项目发现、路由注册、配置重载、运行状态和 Handoff Runtime 生命周期。关闭窗口不销毁 Manager；显式退出时才停止服务。

### Runtime Service

`internal/runtime.Service` 组装 OpenCode Adapter、Watcher、Engine、Feishu Channel 与 Store，并负责启动、停止和 `/bind` 配对初始化。桌面端和旧 CLI 复用同一套 Runtime；实例锁阻止两个引擎同时消费相同事件。

### OpenCode Adapter

`internal/opencode` 封装 OpenCode HTTP / SSE API，包括：

- 项目、Session、状态和消息查询
- `/global/event` 或项目级事件流
- `prompt_async`、abort 和原生 Session 创建
- Question 查询、回答和拒绝
- Permission 查询和决策
- Provider、模型和 variant 的脱敏读取

上层不依赖用户使用 Desktop、TUI、Web、`serve` 或 `attach` 中的哪一种界面，只依赖同一个 OpenCode Server 和 `session_id`。

### Watcher

`internal/handoff.Watcher` 同时支持 SSE 和轮询。默认轮询每 3 秒执行一次，用于补偿 SSE 重连、事件遗漏或 API 差异。

Watcher 观察 Session 状态转换、`session.error`、`question.asked` 和 `permission.asked`。它在内存中记录已见状态和请求 ID，防止 SSE 与轮询立即重复发出同一 Signal。

### Engine

`internal/handoff.Engine` 把 Signal 转换为 Handoff：

```text
Signal
  ├─ pending Question   → QUESTION
  ├─ pending Permission → PERMISSION
  ├─ session error      → ERROR
  └─ busy/retry → idle  → FINISHED
```

Engine 会读取 Session 与最后一条 Assistant 消息、排除带 `parentID` 的子 Agent Session、应用项目路由和通知开关、截断最后输出、持久化记录，再调用 Channel 发送消息。

### Channel Adapter

`internal/channel.Channel` 定义 Handoff、回复、项目、模型、运行状态和交互卡片所需能力。当前实现位于 `internal/channel/feishu`，使用 Feishu / Lark 长连接，因此不要求公网 Webhook。

Channel 只负责外部消息格式与输入解析。是否能够恢复 Session、如何回答 Question / Permission，以及去重策略仍由 Engine 和 Store 决定。

### Store

`internal/store` 使用 `modernc.org/sqlite` 持久化：

- Handoff 与 Feishu message 到 OpenCode Session 的映射
- Question / Permission 请求与处理状态
- 入站消息和回调幂等键
- Feishu 绑定身份和会话
- 项目路由选择
- 最近使用模型（最多 20 条）
- 桌面事件记录

通知去重的核心维度是：

```text
session_id + last_assistant_message_id + handoff_type
```

入站 `message_id` 与卡片回调也会单独去重，避免平台重投造成重复 Prompt、重复 Session 或重复授权。

## Handoff 与恢复路径

### 普通 Finished / Error

```text
Feishu 引用回复
    ↓ reply_message_id
Store 查找 Handoff
    ↓ session_id + directory
OpenCode prompt_async
    ↓
原 Session 增加真实 User Message
```

当绑定会话中只有一个 OPEN Handoff 时，可以不引用直接发送普通文本；多个 Session 同时等待时必须引用对应通知，系统不会猜测目标。

### Question

```text
question.asked
    ↓
Question Card
    ↓ 答案 / reject
/question/{id}/reply 或 /reject
    ↓
原 Agent Loop 解除等待
```

### Permission

```text
permission.asked
    ↓
Permission Card
    ↓ once / always / reject
/permission/{id}/reply
    ↓
原 Agent Loop 按 OpenCode 语义继续或拒绝
```

Question 和 Permission 不会转换成普通 Prompt，并且每个请求只允许成功处理一次。

## 项目路由

桌面端从 OpenCode 发现项目，并通过 `internal/desktop.RouteRegistry` 包装 Adapter。所有项目默认关闭路由，用户在“项目接入”中显式启用后，新事件才会到达 Engine。路由选择保存在 SQLite，刷新与重启不会重置。

设置固定 `opencode.directory` 时，监听和项目启动器都限制到该目录；为空时使用全局事件和多项目轮询。

## 安全模型

- OpenCode Server 默认只允许 loopback URL；远程地址必须显式 `allow_remote: true`。
- 推荐始终设置 OpenCode Server 密码。
- Feishu App Secret 只保存在本机配置或环境变量中。
- 一次性配对码只写入本机日志。
- 可用 `security.allowed_users` 进一步限制 `open_id / user_id / union_id`。
- 非绑定用户、非绑定会话和无法精确解析目标 Session 的消息会被拒绝。
- Provider 响应只解析模型元数据，不向 Feishu 发送 API Key 或连接配置。

## 扩展边界

架构允许增加新的 Agent Adapter 或 Channel Adapter，但扩展仍应遵守 Handoff 语义：只在需要人工关注时通知，恢复原 Session，不复制完整执行流，也不替代 Agent 的主工作界面。
