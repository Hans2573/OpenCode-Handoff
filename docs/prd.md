# OpenCode Handoff

**Repository:** `opencode-handoff`
**Product Type:** OpenCode Human-in-the-Loop Companion
**Version:** V1.0
**Primary Channel:** Feishu / Lark
**Positioning:** Async Session Handoff for OpenCode

---

## 1. 产品简介

OpenCode Handoff 是运行在 OpenCode 之外的轻量级 Human-in-the-Loop 协作服务。

它不将飞书变成 OpenCode 的完整聊天客户端，也不转发 OpenCode 的实时流式输出。

它只关注 OpenCode Session 的关键状态变化：

**OpenCode 执行 → 停止 / 等待 / 异常 → 通知用户 → 用户远程输入 → 原 Session 继续执行。**

核心目标是：

> 用户离开电脑后，无需持续盯着 OpenCode。只有当 OpenCode 真正需要用户关注时，Handoff 才通知用户；用户通过飞书给出下一步输入后，原 OpenCode Session 原地继续。

---

# 2. 背景

使用 OpenCode Desktop / TUI 执行较长编码任务时，常见情况包括：

* Agent 完成当前任务后等待下一步指令；
* Agent 给出多个实现方案等待用户选择；
* Agent 调用 Question Tool 等待用户回答；
* Agent 请求 Permission；
* 模型调用、工具执行或网络发生异常；
* 用户暂时离开电脑，无法及时看到当前状态。

现有 OpenCode + Feishu 工具主要集中在两类：

### 类型 A：通知 Bridge

```text
OpenCode
   ↓
event
   ↓
Feishu
```

只能告诉用户发生了什么。

### 类型 B：完整 IM Bridge

```text
Feishu
   ↕
OpenCode
```

将飞书作为完整 OpenCode 客户端，同步：

* 用户消息
* Assistant 回复
* Streaming Token
* Tool Call
* Thinking
* Interactive Card

这对于 OpenCode Handoff 的场景过重。

---

# 3. 产品定位

OpenCode Handoff 不做：

```text
OpenCode ↔ Feishu Chat Bridge
```

而做：

```text
OpenCode
   │
   │ 执行
   ▼
Running
   │
   │ 到达人工关注点
   ▼
Handoff Point
   │
   ▼
Feishu
   │
   │ 用户输入
   ▼
OpenCode 原 Session
   │
   ▼
Running
```

因此产品的核心概念不是 **Message Bridge**，而是：

# Handoff Point

即：

> Agent 将执行权暂时交还给人的节点。

---

# 4. 产品目标

V1 需要实现以下完整闭环：

```text
OpenCode Desktop / TUI

        ↓

用户发起任务

        ↓

OpenCode 正常执行

        ↓

┌───────────────────┐
│ Handoff Point     │
│                   │
│ completed / idle  │
│ question          │
│ permission        │
│ error             │
└─────────┬─────────┘
          ↓
     Handoff Service
          ↓
        Feishu
          ↓
     用户远程输入
          ↓
     Handoff Service
          ↓
     原 Session
          ↓
 OpenCode 继续执行
```

---

# 5. 核心设计原则

## 5.1 OpenCode 是主界面

OpenCode Desktop / TUI 始终是完整对话和执行过程的主界面。

飞书只是：

**通知入口 + 临时人工输入入口。**

因此不在飞书维护一套独立 Conversation。

---

## 5.2 Session 是唯一事实源

Handoff 不控制 Desktop，也不模拟键盘。

所有操作基于：

```text
session_id
```

例如：

```text
ses_abc123
```

飞书回复最终必须写入这个 Session。

因此：

```text
Desktop
TUI
Feishu Handoff
```

看到的是同一条 Session 历史。

---

## 5.3 非流式

OpenCode 执行过程中：

```text
Thinking
Read
Edit
Bash
Tool Call
Token Stream
```

均不发送飞书。

只有进入 Handoff Point 后才通知。

---

## 5.4 不创建新的 Agent Session

用户在飞书回复：

```text
选择第二个方案，继续。
```

必须进入：

```text
原 session_id
```

不能创建新的 Session。

---

# 6. 用户场景

## Scenario 1：任务正常完成

OpenCode：

```text
任务已经完成。

修改了：
- AgentSkillService
- SessionManager
- SkillInstaller

所有测试通过。
```

Handoff 发送：

```text
OpenCode · Task Finished

opsloop

最后输出：

任务已经完成。

修改了：
- AgentSkillService
- SessionManager
- SkillInstaller

所有测试通过。
```

用户无需回复。

---

## Scenario 2：需要用户决定

OpenCode：

```text
目前有两个方案：

A. Session 创建后安装 Skill
B. 首次调用时懒加载 Skill

我建议方案 A。

你希望采用哪个？
```

Handoff：

```text
OpenCode · Waiting

opsloop

目前有两个方案：

A. Session 创建后安装 Skill
B. 首次调用时懒加载 Skill

我建议方案 A。

你希望采用哪个？

回复本消息继续。
```

用户：

```text
A，继续。
```

Handoff：

```text
Feishu Reply
      ↓
resolve session_id
      ↓
OpenCode Session
      ↓
User Message:
"A，继续。"
```

Desktop 随即显示：

```text
You
A，继续。

OpenCode
好的，我采用方案 A……
```

---

## Scenario 3：用户追加任务

OpenCode：

```text
当前需求已经完成。
```

用户在飞书回复：

```text
继续帮我补一下单元测试。
```

Handoff 将其作为新的 User Message 写入原 Session。

OpenCode 自动开始下一轮执行。

---

## Scenario 4：Question

OpenCode：

```text
请选择部署环境：

1. Development
2. Staging
3. Production
```

Handoff 检测到 Pending Question。

飞书回复：

```text
2
```

此时不创建普通 Prompt。

而执行：

```text
Question Reply
```

解除 Agent Loop 的等待状态。

---

## Scenario 5：Permission

OpenCode 请求：

```text
执行：

kubectl rollout restart deployment/api
```

飞书：

```text
OpenCode · Permission Required

kubectl rollout restart deployment/api

允许此次操作？

[Allow]
[Deny]
```

用户选择后直接回复 OpenCode Permission Request。

---

## Scenario 6：异常

例如：

```text
LLM API error
429
context overflow
tool timeout
session error
```

Handoff 通知：

```text
OpenCode · Interrupted

opsloop

执行发生异常：

API request timeout

最后输出：

正在运行 integration test...
```

用户可以回复：

```text
再试一次。
```

继续原 Session。

---

# 7. Session 映射

Handoff 必须保存：

```text
Feishu Message
       ↓
OpenCode Session
```

核心记录：

```text
handoff_record

id
session_id
directory
project_name

feishu_chat_id
feishu_message_id

handoff_type

last_assistant_message_id
last_assistant_text

status

created_at
resolved_at
```

`status`：

```text
OPEN
RESUMED
CLOSED
EXPIRED
```

---

# 8. Handoff 类型

统一定义：

```text
FINISHED
QUESTION
PERMISSION
ERROR
STALLED
```

### FINISHED

Session 从 Busy 进入 Idle。

### QUESTION

存在 Pending Question。

### PERMISSION

存在 Pending Permission。

### ERROR

Session 发生不可恢复错误。

### STALLED

长期无进展。

V1 可以暂不启用 STALLED。

---

# 9. 状态机

```text
                    ┌─────────┐
                    │ RUNNING │
                    └────┬────┘
                         │
        ┌────────────────┼────────────────┐
        │                │                │
      idle            question          error
        │                │                │
        ▼                ▼                ▼
   FINISHED         WAIT_INPUT          ERROR
        │                │                │
        └────────────────┼────────────────┘
                         │
                         ▼
                     HANDOFF
                         │
                         ▼
                      FEISHU
                         │
                   User Reply
                         │
          ┌──────────────┼──────────────┐
          │              │              │
       Prompt         Question      Permission
          │            Reply           Reply
          │              │              │
          └──────────────┼──────────────┘
                         │
                         ▼
                      RUNNING
```

---

# 10. OpenCode 接入

Handoff 作为独立 Sidecar Service。

```text
┌──────────────────────────────┐
│        OpenCode Server       │
│                              │
│ Session                      │
│ Message                      │
│ Event                        │
│ Question                     │
│ Permission                   │
└──────────────┬───────────────┘
               │
          HTTP / SSE
               │
               ▼
┌──────────────────────────────┐
│       OpenCode Handoff       │
│                              │
│ Session Watcher              │
│ Handoff Engine               │
│ Resume Dispatcher            │
│ Channel Adapter              │
│ Storage                      │
└──────────────┬───────────────┘
               │
        Feishu WebSocket
               │
               ▼
             Feishu
```

---

# 11. OpenCode Adapter

设计统一：

```text
OpenCodeAdapter
```

负责屏蔽 OpenCode API 版本差异。

能力：

```text
watchEvents()

listSessions()

getSessionStatus()

getMessages(sessionID)

sendPrompt(sessionID, text)

listQuestions()

replyQuestion(questionID, answer)

listPermissions()

replyPermission(permissionID, decision)
```

上层 Handoff Engine 不感知：

```text
Desktop
TUI
Web
serve
attach
```

---

# 12. Desktop / TUI 兼容

Handoff 不依赖 UI。

推荐部署：

```text
             ┌─ Desktop
             │
             │
OpenCode ────┼─ TUI
Server       │
             │
             └─ Handoff
```

因此支持：

| 客户端               | 支持  |
| ----------------- | --- |
| OpenCode Desktop  | Yes |
| OpenCode TUI      | Yes |
| `opencode serve`  | Yes |
| `opencode attach` | Yes |
| OpenCode Web      | 可扩展 |

---

# 13. OpenCode Watcher

首选：

```text
SSE Event
```

监听 Session 状态变化。

同时提供：

```text
Polling Fallback
```

例如：

```text
3 seconds
```

用于检测：

```text
session status
pending question
pending permission
```

避免因为 SSE 重连或版本差异造成通知遗漏。

---

# 14. Handoff Engine

核心逻辑：

```text
onSessionStopped(sessionID)
        ↓
检查 Pending Question
        │
        ├── Yes → QUESTION
        │
        ↓ No
检查 Pending Permission
        │
        ├── Yes → PERMISSION
        │
        ↓ No
检查 Error
        │
        ├── Yes → ERROR
        │
        ↓
FINISHED
```

然后生成：

```text
Handoff Context
```

包括：

```text
session_id
project
cwd
type
last_output
question
permission
error
```

---

# 15. 最后输出提取

不发送完整 Conversation。

默认只取：

```text
最后一条 assistant message
```

限制建议：

```text
max_chars = 3000
```

超出：

```text
...
<last 3000 chars>
```

保证用户能够快速看到：

* OpenCode 做到了什么；
* OpenCode 最后说了什么；
* 是否正在询问用户；
* 是否需要继续。

---

# 16. Feishu Adapter

V1 使用：

```text
Feishu WebSocket Long Connection
```

原因：

```text
无需公网 IP
无需部署公网 Webhook
适合个人开发机
```

提供：

```text
sendHandoff()

receiveReply()

sendQuestion()

sendPermission()

updateHandoffStatus()
```

---

# 17. 回复路由

用户必须：

```text
Reply 原 Handoff Message
```

例如：

```text
OpenCode · Waiting

请选择 A / B
```

用户引用回复：

```text
A
```

通过：

```text
reply_message_id
```

找到：

```text
session_id
```

从而避免：

```text
Project A
Project B
Project C
```

同时执行时发生 Session 串线。

---

# 18. 消息注入

普通场景：

```text
Feishu Reply
     ↓
Handoff Record
     ↓
session_id
     ↓
session.prompt
```

最终 OpenCode Session 增加真实：

```text
role=user
```

消息。

因此 Desktop / TUI 中可以正常看到飞书输入。

---

# 19. 去重

需要防止：

```text
idle
idle
idle
```

重复产生通知。

唯一键建议：

```text
session_id
+
last_assistant_message_id
+
handoff_type
```

相同组合只允许生成一个 Handoff。

---

# 20. 多 Session 支持

必须支持：

```text
OpenCode

├── ses_A → opsloop
├── ses_B → hubble
└── ses_C → other
```

每次通知独立保存：

```text
Feishu Message A → ses_A
Feishu Message B → ses_B
Feishu Message C → ses_C
```

用户回复哪条通知，就继续哪条 Session。

---

# 21. 安全

V1 至少支持：

```text
Feishu User Allowlist
```

仅允许指定：

```text
open_id / user_id
```

控制 OpenCode。

OpenCode Server 默认仅监听：

```text
127.0.0.1
```

禁止直接向公网暴露。

Feishu App Secret 等敏感配置必须通过：

```text
environment variables
```

或本地配置文件提供。

---

# 22. 配置

示例：

```yaml
opencode:
  base_url: http://127.0.0.1:4096

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

security:
  allowed_users:
    - ou_xxxxx
```

---

# 23. 技术架构

推荐：

```text
Go
```

目录：

```text
opencode-handoff/

cmd/
  handoff/

internal/

  opencode/
    client.go
    events.go
    session.go
    question.go
    permission.go

  handoff/
    engine.go
    state.go
    dispatcher.go

  channel/
    channel.go

    feishu/
      client.go
      sender.go
      receiver.go

  store/
    store.go
    sqlite.go

  config/
    config.go
```

---

# 24. Channel 抽象

虽然 V1 只支持 Feishu，但核心不要出现：

```text
FeishuBridge
```

而统一定义：

```go
type Channel interface {
    SendHandoff(ctx context.Context, h Handoff) (MessageRef, error)

    Receive(ctx context.Context) (<-chan UserReply, error)
}
```

因此后续可以扩展：

```text
Feishu
Slack
Teams
Telegram
Discord
```

而产品依然叫：

```text
OpenCode Handoff
```

不需要改名字。

---

# 25. MVP 范围

## V1.0

实现：

```text
Session idle detection
Session error detection

Last assistant message extraction

Feishu notification

Feishu reply

Message → Session mapping

Resume original Session

Desktop visible continuation

TUI visible continuation

SQLite persistence

SSE + polling fallback
```

---

# 26. V1.1

增加：

```text
Question Tool

Permission Approval

Interactive Feishu Card
```

---

# 27. V1.2

增加：

```text
Stall detection

Session timeout

Multiple OpenCode instances

Desktop machine identification

Handoff history
```

---

# 28. 非目标

OpenCode Handoff 不计划成为：

```text
OpenCode Feishu Client
```

V1 不做：

```text
Streaming output

Thinking synchronization

Tool-call synchronization

Full conversation synchronization

Remote model selection

Remote agent creation

Remote session creation

File browser

Remote terminal

IDE replacement
```

避免产品演化成另一个 OpenCode 前端。

---

# 29. 产品差异

### Message Bridge

关注：

```text
Message ↔ Message
```

### OpenCode Handoff

关注：

```text
Agent State
    ↓
Human Attention
    ↓
Human Decision
    ↓
Agent Resume
```

因此它实际上更接近：

```text
Human-in-the-Loop Runtime Companion
```

而不是：

```text
IM Bridge
```

---

# 30. 产品一句话

**OpenCode Handoff lets OpenCode work autonomously until it needs you.**

中文：

> **让 OpenCode 自己干活，只有真正需要你时再找你。**

---

# 31. 成功标准

用户可以：

```text
在 Desktop 创建任务
        ↓
离开电脑
        ↓
OpenCode 自己执行
        ↓
任务结束 / 等待输入
        ↓
手机收到飞书通知
        ↓
看到 OpenCode 最后现场
        ↓
通过飞书回复
        ↓
原 Session 自动继续
        ↓
回到 Desktop
        ↓
看到完整连续对话
```

整个过程中：

**不创建第二套 Conversation，不要求用户始终打开电脑，不把飞书变成 OpenCode 的完整客户端。**

这就是 OpenCode Handoff 的核心产品边界。
