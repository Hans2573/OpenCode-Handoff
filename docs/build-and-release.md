# 构建与发布

本文依据当前仓库中的 `go.mod`、`frontend/package.json`、`Taskfile.yml`、`build/config.yml`、`build/windows/Taskfile.yml` 和 `build/windows/nsis/project.nsi` 编写。命令均从仓库根目录执行，不依赖作者本机硬编码路径。

## 当前构建基线

| 项目 | 仓库配置 |
| --- | --- |
| Go | `go 1.25.0` |
| Wails Go module / runtime | `v3.0.0-beta.15` |
| Wails CLI | 应安装 `v3.0.0-beta.15` |
| React | `19.2.8` |
| Vite | `8.2.2` |
| npm package version | `0.1.0` |
| 产品版本 | `build/config.yml` 中的 `0.1.0` |
| Windows 打包器 | NSIS |

建议使用 Node.js 22 和 npm 10。Windows 安装包构建还要求 `makensis` 可从 `PATH` 调用。

## 安装构建依赖

```powershell
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.15

Set-Location frontend
npm install
Set-Location ..
```

安装 NSIS 后验证：

```powershell
wails3 version
makensis /VERSION
```

如果使用便携 NSIS，把其安装目录加入当前 PowerShell 会话的 `PATH` 即可；不要把个人绝对路径写进仓库文档或脚本。

## 构建前检查

构建前先从系统托盘完全退出正在运行的 Agent Handoff，避免目标 EXE 或临时文件被占用。

```powershell
Set-Location frontend
npm run build
Set-Location ..

go test ./...
go vet ./...
```

`npm run build` 实际执行 `tsc && vite build --mode production`。Wails 的生产构建也会安装前端依赖、生成 TypeScript bindings、构建 `frontend/dist`、生成平台图标，并执行 `go mod tidy`。

## 构建 Windows EXE

```powershell
wails3 build
```

顶层 `Taskfile.yml` 将 `build` 分派到 `windows:build`。Windows 原生任务默认设置：

```text
GOOS=windows
CGO_ENABLED=0
GOARCH=<当前架构，通常为 amd64>
```

生产 Go build flags 包含 `production` build tag、`-trimpath`、`-buildvcs=false`，以及用于去除符号并隐藏控制台窗口的 linker flags。

默认输出：

```text
bin\agent-handoff.exe
```

## 生成 NSIS 安装包

```powershell
wails3 package
```

`package` 默认分派到 `windows:create:nsis:installer`。该任务会：

1. 先执行完整 Windows build。
2. 生成或复用 WebView2 bootstrapper。
3. 从 `build/windows/nsis` 调用 `makensis`。
4. 把 `bin/agent-handoff.exe` 嵌入安装包。

amd64 默认输出：

```text
bin\agent-handoff-amd64-installer.exe
```

当前 `project.nsi` 的实际行为：

- `Unicode true`
- 简体中文安装界面
- 检查 Windows 10 / Server 2016 或更高版本
- 强制 `WAILS_INSTALL_SCOPE` 为 `user`
- 安装到 `%LOCALAPPDATA%\Programs\Agent Handoff`
- 不请求管理员权限
- 创建开始菜单和桌面快捷方式
- 注册卸载项并提供 `uninstall.exe`

虽然 `build/windows/Taskfile.yml` 的 `package` 任务变量默认值是 `INSTALL_SCOPE=machine`，`project.nsi` 当前在 include 前显式定义了 user scope，因此最终安装包仍按用户安装。若以后要支持 machine scope，需要先统一这两处配置，再更新本文。

## 便携版

仓库当前没有单独的便携 ZIP Task。现有发布约定是把以下文件放入版本目录后压缩：

```text
agent-handoff.exe
config.example.yaml
README.md
```

产物命名示例：

```text
Agent-Handoff-0.1.0-windows-amd64-portable.zip
```

不要把本机 `config.yaml`、SQLite 数据库或日志放入便携包。

## 版本与构建元数据

发布前至少核对：

- `build/config.yml` 的 `info.version`、产品名、标识符和版权信息
- `frontend/package.json` 的 `version`
- 文档中的便携包版本示例

修改 `build/config.yml` 中的 `info` 或文件关联后，按该文件注释运行：

```powershell
wails3 task common:update:build-assets
```

该命令会重新生成 build assets，并可能覆盖生成文件中的手工修改。执行前应先检查工作区，执行后审阅 diff。

## 签名

Windows Taskfile 声明了 `windows:sign` 和 `windows:sign:installer`，使用 `wails3 tool sign`，证书可以来自 Wails 全局配置，也可以通过 `SIGN_CERTIFICATE` 或 `SIGN_THUMBPRINT` 指定，密码由系统 keychain 管理。

当前配置中，NSIS 的实际输出是 `bin/agent-handoff-<arch>-installer.exe`，但 `windows:sign:installer` 声明的输入路径是 `build/windows/nsis/agent-handoff-installer.exe`。两者不一致；在修正并验证该 Task 前，不应把 `windows:sign:installer` 当作可直接使用的发布命令。可执行文件签名任务 `windows:sign` 的目标与 `bin/agent-handoff.exe` 一致。

## 发布检查清单

1. 确认工作区没有无关改动，尤其不要覆盖用户已有的 `frontend/dist` 产物。
2. 核对 Go、Wails、前端和产品版本。
3. 执行前端构建、`go test ./...` 和 `go vet ./...`。
4. 执行 `wails3 build`，验证 `bin/agent-handoff.exe`。
5. 执行 `wails3 package`，在干净 Windows 环境验证安装、启动、托盘退出和卸载。
6. 如需签名，先解决并验证安装包签名路径差异。
7. 生成便携 ZIP，并确认不含凭据、数据库或日志。
8. 检查 README 和 docs 链接，再审阅 `git diff --stat` 与 `git diff`。

如果构建提示临时 `a.out.exe` 或目标 EXE 被占用，确认 Agent Handoff 已从托盘完全退出，然后重试原命令。
