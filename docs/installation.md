# 安装指南

OpenCode Handoff 当前主要面向 Windows x64。桌面端产品名为 **Agent Handoff**，它连接用户已经启动的 OpenCode Server，不会自行启动或停止 OpenCode。

## 系统要求

- Windows 10 / Windows Server 2016 或更高版本
- OpenCode，可运行 `opencode serve`
- Feishu / Lark 企业自建应用
- Microsoft WebView2 Runtime；NSIS 安装包会通过 Wails bootstrapper 检查并安装所需 Runtime

从源码开发或构建还需要 Go、Node.js、Wails CLI 和 NSIS，见[开发指南](development.md)与[构建和发布](build-and-release.md)。

## 安装桌面端

Windows x64 提供两种产物：

- `agent-handoff-amd64-installer.exe`：安装版，创建开始菜单、桌面快捷方式和卸载入口。
- `Agent-Handoff-0.1.0-windows-amd64-portable.zip`：便携版，解压后运行 `agent-handoff.exe`。

安装包当前按用户安装到 `%LOCALAPPDATA%\Programs\Agent Handoff`，无需管理员权限。

关闭主窗口只会把应用隐藏到系统托盘，不会停止 Handoff 服务。需要彻底停止时，从托盘菜单选择“退出”，或在设置页点击“退出应用”。

## 启动 OpenCode Server

Desktop / TUI 与 Handoff 应连接同一个 OpenCode Server。建议为 Server 设置密码并只监听 loopback：

```powershell
$env:OPENCODE_SERVER_PASSWORD = "你的本机密码"
opencode serve --hostname 127.0.0.1 --port 4096
```

启动后可先确认 `http://127.0.0.1:4096` 能够访问。Handoff 默认拒绝非 loopback 地址；可信内网场景的显式放行方式见[配置参考](configuration.md#opencode-连接与安全)。

## 首次启动

1. 启动 Agent Handoff。
2. 打开“设置”，填写 OpenCode 服务地址、用户名和密码，以及 Feishu / Lark App ID 与 App Secret。
3. 保存后，应用会重新加载 Handoff 引擎，但不会重启 OpenCode。
4. 按[飞书 / Lark 配置](feishu-setup.md)完成 `/bind`。
5. 在 OpenCode 中打开目标项目，回到 Agent Handoff 点击“刷新 OpenCode 项目”。
6. 在“项目接入”中打开需要通知的项目路由。

所有项目默认都是“未接入”，包括旧 CLI 导入的项目和之后新发现的项目。手动选择会持久保存，刷新或重启不会改变；只有已接入项目的新事件会转发到 Feishu / Lark。

## 数据目录

Windows 桌面端的配置、SQLite 数据库和日志默认位于用户目录：

```text
%USERPROFILE%\.agent-handoff
```

此目录不受启动器的 AppData 虚拟化影响，从普通 PowerShell、Codex 或安装程序启动均使用同一份数据。可通过绝对路径环境变量 `AGENT_HANDOFF_DATA_DIR` 显式指定其他数据目录；不同启动方式需要使用同一个值。

常用文件包括：

```text
%USERPROFILE%\.agent-handoff\config.yaml
%USERPROFILE%\.agent-handoff\opencode-handoff.db
%USERPROFILE%\.agent-handoff\logs\agent-handoff.log
```

配置仍是本机明文 YAML；设置页会掩码显示密钥，并标识被环境变量覆盖的字段。不要把配置文件或 SQLite 数据库加入版本控制或 Release。

## 从旧版迁移

桌面端首次启动会尝试从以下位置导入旧 `config.yaml` 和 SQLite 数据库：

- 原 `%LOCALAPPDATA%\Agent Handoff`、`%LOCALAPPDATA%\OpenCode Handoff` 及 Windows 应用隔离目录中的历史数据（优先）
- 可执行文件附近
- 当前工作目录

导入时通过 SQLite 一致性快照保留 WAL 中已经提交的数据，并保留 `.imported.bak` 备份。已有新目录配置不会被旧文件覆盖。若发现多个不同的历史数据库，会停止自动导入并提示源路径，需先备份、核对并合并，避免默默选择一套空库。已经完成飞书绑定的旧用户通常不需要重新 `/bind`。

桌面端与旧 CLI 使用单实例保护。检测到旧 CLI 正在运行时，桌面端会提示冲突，但不会强制结束 CLI；先停止旧 CLI，再在桌面端点击重试。

旧 CLI 的独立说明见[旧版 CLI](legacy-cli.md)。

## 常见问题

### 窗口关闭后仍在运行

这是预期行为。请从托盘菜单或设置页显式退出。

### 看不到 OpenCode 项目

确认 OpenCode Server 已启动且地址、用户名和密码正确，然后点击“刷新 OpenCode 项目”。固定配置了 `opencode.directory` 时，只会显示该目录。

### 没有收到通知

依次检查桌面端中的 OpenCode 与 Feishu 连接状态、飞书应用是否已发布、是否完成 `/bind`，以及目标项目的路由开关是否已打开。
