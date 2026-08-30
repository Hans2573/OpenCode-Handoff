# 开发指南

## 技术栈

- Go 1.25
- Wails v3.0.0-beta.15
- React 19
- Vite 8
- TypeScript
- SQLite（pure Go `modernc.org/sqlite`）
- Feishu / Lark Go SDK
- NSIS（Windows 安装包）

版本以仓库的 `go.mod` 和 `frontend/package.json` 为准。当前开发建议使用 Node.js 22 与 npm 10。

## 准备环境

在仓库根目录执行：

```powershell
go version
node --version
npm --version
```

安装项目锁定的 Wails CLI：

```powershell
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.15
wails3 version
```

确保 Go 的 bin 目录已经通过标准 Go 安装配置加入 `PATH`。文档不依赖任何作者本机路径。

安装前端依赖：

```powershell
Set-Location frontend
npm install
Set-Location ..
```

Wails Taskfile 也会在需要时执行 `npm install`。

## 开发模式

先启动 OpenCode Server，再从仓库根目录运行：

```powershell
wails3 dev
```

顶层 `Taskfile.yml` 实际会使用：

```text
wails3 dev -config ./build/config.yml -port 9245
```

开发模式会监听 Go 文件变化、生成 Wails TypeScript bindings、启动 Vite，并运行桌面应用。`build/config.yml` 排除了 `.git`、`node_modules`、`frontend`、`bin` 和 `*_test.go` 等无需由 Go watcher 处理的内容。

若只需要调试前端，可在 `frontend` 目录运行：

```powershell
npm run dev
```

这不会单独提供桌面端绑定服务；完整联调仍使用 `wails3 dev`。

## 验证

前端生产构建：

```powershell
Set-Location frontend
npm run build
Set-Location ..
```

Go 测试与静态检查：

```powershell
go test ./...
go vet ./...
```

测试使用 `httptest`、临时 SQLite 和 fake Channel，不要求连接真实 OpenCode 或 Feishu。

完整桌面构建与 NSIS 打包见[构建与发布](build-and-release.md)。

## 代码结构

```text
.
├─ main.go                         # Wails 桌面入口、窗口、托盘、单实例
├─ appservice.go                   # 暴露给 React 的桌面服务
├─ cmd/handoff/                    # 旧版无界面 CLI
├─ frontend/src/                   # React UI
├─ internal/config/                # YAML、环境变量与配置校验
├─ internal/desktop/               # 桌面 Manager、路由和 ViewModel
├─ internal/runtime/               # Handoff 服务组装与实例锁
├─ internal/handoff/               # Watcher、Engine 与回复调度
├─ internal/opencode/              # OpenCode HTTP / SSE Adapter
├─ internal/channel/feishu/        # Feishu 长连接与卡片实现
├─ internal/store/                 # SQLite 持久化
└─ build/                          # Wails 和平台打包配置
```

组件关系和运行时数据流见[架构说明](architecture.md)。

## 开发注意事项

- OpenCode 是 Session 历史的事实源；不要在 Channel 层维护第二套对话。
- 普通回复必须解析到已有 `session_id`；不要隐式创建 Session。
- Question 与 Permission 必须调用对应 API，不能转为普通 Prompt。
- 不要把 Thinking、Tool Call 或 Token Stream 加入通知流。
- SSE 与轮询可能同时发现同一事件，所有新增 Handoff 类型都必须考虑去重和幂等。
- 带 `parentID` 的子 Agent Session 不应产生面向用户的 Handoff。
- `config.yaml`、SQLite 数据库和日志可能包含凭据或用户身份，不得提交。
