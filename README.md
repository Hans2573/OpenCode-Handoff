<div align="center">

# OpenCode Handoff

### Stay away from your desk. Stay in the loop.

**English** | [简体中文](./README.zh-CN.md)

Get notified when OpenCode needs you, respond from Feishu / Lark,
and continue the **same OpenCode session** remotely.

[![Latest Release](https://img.shields.io/github/v/release/Hans2573/OpenCode-Handoff?display_name=tag)](https://github.com/Hans2573/OpenCode-Handoff/releases/latest)
![Windows](https://img.shields.io/badge/platform-Windows-0078D4?logo=windows)
![Go 1.25](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go)
![Wails v3](https://img.shields.io/badge/Wails-v3.0.0--beta.15-DF0000)

</div>

![OpenCode Handoff desktop dashboard](./docs/images/opencode-handoff-dashboard.png)

## What is OpenCode Handoff?

OpenCode Handoff lets you leave your desk without losing control of your OpenCode sessions.

When OpenCode finishes a task, asks a question, requests permission, or hits an error, Handoff notifies you through Feishu / Lark.

Reply from your phone, and your response goes back to the same OpenCode session. OpenCode remains your primary workspace and the source of truth for the complete conversation.

## How It Works

```text
OpenCode
   ↓
working...
   ↓
Finished / Question / Permission / Error
   ↓
OpenCode Handoff
   ↓
Feishu / Lark
   ↓
Reply / Approve
   ↓
Same OpenCode Session continues
```

OpenCode stays your primary workspace. Handoff only steps in when the agent needs you.

It is not a full OpenCode ↔ Feishu chat bridge. It does not continuously forward Thinking, token streams, tool calls, or every streamed Assistant response.

## Features

- **Remote Session Handoff** — Continue the exact same OpenCode session from Feishu / Lark

- **Remote Session Launch** — Create a session from a project card and choose its model and reasoning variant

- **Model Discovery & Switching** — Browse providers, search models, reuse recent choices, and switch the model for the next task

- **Live Status & Timing** — See running, retrying, waiting, and approval states with the current model, execution time, and time since your last input

- **Questions & Permissions** — Answer questions and approve once, always allow, or reject through interactive cards

- **Completion, Error & Stop Controls** — Receive attention-worthy notifications, continue after an error, or stop the mapped session remotely

- **Multi-Project Routing** — Choose which local projects can reach you while keeping every reply mapped to the correct session

- **Desktop & Local-First** — Manage projects, sessions, connections, settings, and event history while keeping operational data on your machine

Detailed setup and implementation notes live in the guides below.

## Quick Start

1. **Download OpenCode Handoff**

   Get the Windows installer or portable package from [GitHub Releases](https://github.com/Hans2573/OpenCode-Handoff/releases).

2. **Start the OpenCode Server**

   ```powershell
   $env:OPENCODE_SERVER_PASSWORD = "your-password"
   opencode serve --hostname 127.0.0.1 --port 4096
   ```

3. **Configure OpenCode Handoff**

   Open Settings and enter the OpenCode URL, credentials, and Feishu / Lark app credentials. See [Configuration](docs/configuration.md).

4. **Configure Feishu / Lark**

   Create and publish a custom app with bot, message, and card callback access. Follow the [Feishu / Lark Setup](docs/feishu-setup.md).

5. **Bind your chat**

   Send the `/bind <pairing-code>` command shown in the local application log to the bot.

6. **Enable a project**

   Refresh OpenCode projects, then enable Handoff for the projects that should notify you.

For installation details and upgrades, see the [Installation Guide](docs/installation.md).

## Documentation

| Guide | Description |
| --- | --- |
| [Installation](docs/installation.md) | Install, run, migrate, and upgrade OpenCode Handoff |
| [Configuration](docs/configuration.md) | Configure OpenCode, notifications, storage, and security |
| [Feishu / Lark Setup](docs/feishu-setup.md) | Configure the app, permissions, pairing, and bot commands |
| [Development](docs/development.md) | Run and validate the project from source |
| [Build & Release](docs/build-and-release.md) | Build the desktop app and package the Windows installer |
| [Architecture](docs/architecture.md) | Understand the product boundary, components, and data flow |
| [Legacy CLI](docs/legacy-cli.md) | Run or migrate from the headless CLI |

## Roadmap

- Stall detection and session timeout handling
- Multiple OpenCode instances and machine identification
- Better Handoff history, diagnostics, and notification routing
- More agent and messaging channel adapters without turning Handoff into a full chat bridge

## License

A license file has not been added to this repository yet.
