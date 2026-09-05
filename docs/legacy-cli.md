# 旧版 CLI

仓库保留 `cmd/handoff` 无界面 CLI，用于兼容已有部署和作为 Handoff sidecar 运行。新用户优先使用 Windows 桌面端；CLI 与桌面端复用相同的配置、Runtime、OpenCode Adapter、Feishu Channel 和 SQLite Store。

## 发布包

Windows x64 用户可以从 [GitHub Releases](https://github.com/Hans2573/OpenCode-Handoff/releases) 下载旧版 `OpenCode-Handoff-v1.1.1-windows-amd64.zip`。历史发布包结构为：

```text
OpenCode-Handoff-v1.1.1-windows-amd64/
├─ opencode-handoff.exe
├─ start-handoff.bat
├─ config.example.yaml
└─ README.md
```

将 `config.example.yaml` 复制为 `config.yaml`，填写 OpenCode 与 Feishu 配置。首次运行会根据 `store.path` 自动创建 SQLite 数据库和表结构。

也可以双击 `start-handoff.bat`。脚本会把自身目录作为工作目录；缺少 `config.yaml` 时会从示例创建并打开记事本，保存配置后再次运行。程序异常退出时，脚本会保留窗口，便于查看错误。

## 从源码运行

在仓库根目录准备 `config.yaml` 后，先执行纯配置检查：

```powershell
go run ./cmd/handoff -config ./config.yaml -check
```

检查不会连接 OpenCode 或 Feishu。运行 CLI：

```powershell
go run ./cmd/handoff -config ./config.yaml
```

完整字段、环境变量和安全限制见[配置参考](configuration.md)，飞书权限与绑定见[飞书 / Lark 配置](feishu-setup.md)。

## 构建 CLI

历史 V1.1.1 二进制可以这样构建：

```powershell
go build -trimpath -ldflags "-s -w -X main.version=1.1.1" -o bin/opencode-handoff.exe ./cmd/handoff
```

该命令只构建无界面 CLI，不使用 Wails、Vite 或 NSIS。桌面端构建见[构建与发布](build-and-release.md)。

## 运行闭环

普通 Handoff：

```text
OpenCode busy → idle / error → Feishu 通知
                                 ↓ 引用回复
原 Session ← prompt_async ← Handoff CLI
```

Question：

```text
question.asked → Feishu 卡片 → /question/{id}/reply 或 /reject → 原 Session
```

Permission：

```text
permission.asked → Feishu 卡片 → /permission/{id}/reply
                                      once / always / reject
```

CLI 支持 `/project` 显式创建 OpenCode Session、`/models` 浏览模型、`/running` 查看运行状态、引用通知后 `/stop` 中断 Session，以及 `/help`。详细交互见[飞书 / Lark 配置](feishu-setup.md#机器人命令)。

## 去重与 Session 路由

- 每条 Feishu Handoff 消息都会持久映射到原 `session_id`。
- 引用回复始终恢复对应 Session，不会创建新的普通 Agent Session。
- SSE 与轮询同时发现相同事件时，SQLite 唯一记录阻止重复通知。
- 飞书入站 `message_id` 和卡片回调会持久化去重。
- 启动时已经 idle 的历史 Session 不会发送完成通知；仍 pending 的 Question 与 Permission 会由轮询发现。
- 带 `parentID` 的子 Agent Session 不会通知。

## 升级与桌面端迁移

CLI 的 `config.yaml` 包含本机凭据，SQLite 包含绑定身份、Session 和消息映射；两者都不应放入 Release。

升级旧 CLI 时，解压新版程序并替换 `opencode-handoff.exe`，同时保留原 `config.yaml` 和 `opencode-handoff.db`。

迁移到桌面端时，首次启动会尝试从可执行文件附近、当前工作目录和 `%LOCALAPPDATA%\OpenCode Handoff` 导入旧配置与数据库，通过 SQLite 快照导入到 `%USERPROFILE%\.agent-handoff`，并保留 `.imported.bak`。发现多套数据库时会停止自动导入，详情见[数据目录与迁移](installation.md#从旧版迁移)。已经绑定的用户通常无需重新配对。

不要同时运行桌面端 Handoff 引擎和旧 CLI。实例锁检测到冲突时，桌面端会提示先关闭 CLI，不会强制结束进程。
