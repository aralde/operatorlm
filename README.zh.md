# OperatorLM

[English](README.md) | [Español](README.es.md) | [Português](README.pt.md) | [简体中文](README.zh.md)

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go](https://img.shields.io/badge/Go-1.22%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![Platforms](https://img.shields.io/badge/platforms-Windows%20%7C%20macOS%20%7C%20Linux-blue)](#build-from-source)
[![Single binary](https://img.shields.io/badge/binary-~11MB-success)](#)
[![No Docker](https://img.shields.io/badge/no%20Docker-required-brightgreen)](#)
[![Keys: OS Keyring](https://img.shields.io/badge/keys-OS%20keyring-informational)](#配置与密钥)

> **一个本地的、兼容 OpenAI 的 proxy，支持真正的 failover、多账号 aliasing，并且磁盘上零密钥。**
> 一个小巧的二进制文件坐在你的 IDE / SDK 和每一个 LLM provider 之间 —— OpenAI、OpenRouter、Groq、Google Gemini、Azure OpenAI，甚至你的 **ChatGPT Plus/Pro** 订阅。

![OperatorLM Banner](images/banner.png)

> [!IMPORTANT]
> OperatorLM 默认监听 `127.0.0.1:11434` —— 和 Ollama 同一个端口。任何已经指向 Ollama 的工具都可以开箱即用。如果你两个都跑，在 `~/.operatorlm/config.toml` 里改其中一个的端口即可。

## Quick Start

**只想直接运行?** 下载最新的二进制文件 —— 不需要 Go toolchain，也不需要 Docker。

### 1. 下载

打开 [Releases 页面](https://github.com/aralde/operatorlm/releases/latest),根据你的操作系统下载对应的 asset:

| OS                    | Asset                              |
| --------------------- | ---------------------------------- |
| Windows (x64)         | `OperatorLM-windows-amd64.exe`     |
| macOS (Apple silicon) | `OperatorLM-darwin-arm64`          |
| macOS (Intel)         | `OperatorLM-darwin-amd64`          |
| Linux (x64)           | `OperatorLM-linux-amd64`           |

> [!NOTE]
> 在 macOS / Linux 上需要先给二进制加可执行权限: `chmod +x OperatorLM-*`。

### 2. 运行

- **Windows**: 双击 `OperatorLM-windows-amd64.exe`,任务栏出现托盘图标(没有控制台窗口)。首次启动时 SmartScreen 可能提示 *"Windows 已保护你的电脑"*,点 **更多信息 → 仍要运行**(二进制未签名)。
- **macOS desktop**: `./OperatorLM-darwin-*` —— 任务栏出现托盘图标。首次启动会被 Gatekeeper 拦截,在 Finder 里右键二进制 → **打开** → 在弹窗里再点一次 **打开**(只需一次)。
- **Linux desktop**: `./OperatorLM-linux-amd64` —— 任务栏出现托盘图标。
- **Linux headless**: `OPERATORLM_NO_TRAY=1 ./OperatorLM-linux-amd64`。

### 3. 打开 admin UI

浏览器打开 **<http://127.0.0.1:11434/admin/>** → 添加一个 provider → 粘贴 API key → 在 **Try It** 标签页发一个测试请求。

### 4. 把你的工具指向它

任何兼容 OpenAI 的客户端都可用 —— 把 base URL 设为 `http://127.0.0.1:11434/v1`,`api_key` 填任意非空字符串即可(OperatorLM 会注入真正的 key)。`curl` / Python / JavaScript 的可复制粘贴示例见下面 [从任何会说 OpenAI 的工具中使用](#4-从任何会说-openai-的工具中使用)。

> 想从源码编译? 见 [Build from source](#build-from-source)。

---

## Demo

![OperatorLM Demo](images/OperatorLM-demo.gif)

## 目录

- [Quick Start](#quick-start)
- [为什么有这个项目?](#为什么有这个项目)
- [典型场景](#典型场景)
- [工作原理](#工作原理)
- [杀手级特性:多账号 aliases](#-杀手级特性多账号-aliases)
- [真正能 failover 的 failover](#%EF%B8%8F-真正能-failover-的-failover)
- [ChatGPT Plus/Pro 作为后端(实验性)](#-chatgpt-pluspro-作为后端实验性)
- [OperatorLM 横向对比](#operatorlm-横向对比)
- [Build from source](#build-from-source)
- [支持的 endpoints](#支持的-endpoints)
- [支持的 providers](#支持的-providers)
- [配置与密钥](#配置与密钥)
- [安全模型](#安全模型)
- [仓库结构](#仓库结构)
- [项目状态与贡献](#项目状态与贡献)

## 为什么有这个项目?

- 🔀 **多账号 aliasing** —— 把 3 个 OpenAI key、2 个 OpenRouter 账号和一个免费的 Groq 兜底,统一放在一个模型名背后。OperatorLM 会依次尝试,直到有一个成功为止。
- 🛡️ **生产级 failover** —— 每个 target 都有 **circuit breaker**(3 状态),retry 使用 exponential backoff + jitter,**RPM limiter** 使用 sliding window。对 429 / 5xx / 网络错误使用不同的 cooldown。
- 🔐 **磁盘上零密钥** —— API key 存放在 **Windows Credential Manager / macOS Keychain / Linux Secret Service** 中。TOML 文件里只放 *引用*,永远不放密钥本身。
- 🖥️ **内嵌的实时 admin UI** —— 在 `http://127.0.0.1:11434/admin/` 中管理 providers、keys、aliases、reliability 配置,并实时观察 audit log 流。零安装:通过 `go:embed` 内嵌在二进制中。
- 🧪 **JSONL 格式的 audit log** —— 记录每个请求:模型、attempt、upstream URL、status、耗时。`Authorization` 头默认脱敏。Writer 非阻塞。
- 🤖 **ChatGPT Plus/Pro 作为后端** —— 通过 OAuth (PKCE) 登录一次,你的 Plus/Pro 配额就接入了和你的工具已经在用的同一个 OpenAI 兼容 API。*(实验性 —— 见下面的免责声明。)*
- 🪶 **一个约 11 MB 的二进制,idle 时约 50 MB RAM** —— 原生 Go。无 `node_modules`、无 Python、无 Docker。毫秒级启动。
- 🛰️ **Headless 模式** —— `OPERATORLM_NO_TRAY=1`,可以在没有桌面会话的 Linux 上运行。
- 🪟 **Windows 上无控制台闪窗** —— 编译时带 `-H=windowsgui`。它是一个真正的 tray app。

---

## 典型场景

- **把免费 / 低价 tier 用到极致** —— 把 Groq → OpenRouter → OpenAI 串成一个 alias。日常编码命中免费 tier,只有在免费 tier 被 rate-limited 时才溢出到付费档。
- **把多个个人 / 工作账号统一到一个模型名下** —— 在 OS keyring 里分别保存 `openai_personal`、`openai_work`、`openai_side`,对外暴露为一个 `gpt-4o` 模型给 Cursor / Continue 用,遇到 429 时让 router 自动轮换。
- **用 ChatGPT Plus/Pro 替代 API 额度** —— 通过 OAuth 登录一次,把 Codex / GPT-5.x 系列模型的调用走到你已有的 Plus/Pro 配额上,不再消耗 API 计费 *(实验性 —— 请阅读 [免责声明](#-chatgpt-pluspro-作为后端实验性))*。
- **Ollama 的 drop-in 替代** —— OperatorLM 监听 `127.0.0.1:11434`,任何已经指向 Ollama 的工具(Continue、Cline、Open WebUI、Zed 等)都不用改一行,就能直接访问 OpenAI / OpenRouter / Gemini / Azure / Bedrock 等。
- **开发机或共享工作站上的可审计 LLM 流量** —— 每个请求都落入脱敏后的 JSONL,key 始终留在 OS keyring 中(从不落盘),admin UI 仅监听 loopback、带 host-header 校验和可选的本地 API key。
- **Headless 自托管网关** —— 在 Linux VM / NAS / 家庭服务器上用 `OPERATORLM_NO_TRAY=1` 启动,从你的笔记本通过 WireGuard 或 Tailscale 访问 `127.0.0.1:11434`,把所有 key 和 audit log 集中在一处。

---

## 工作原理

![OperatorLM 请求流程](images/OperatorLM-flow.png)

1. **接收** 一个发往 `127.0.0.1:11434` 的 OpenAI 格式请求。
2. **解析** `model` 字段 → 要么按前缀匹配(`openai/gpt-4o`、`groq/llama-3.3-70b-versatile`),要么命中用户定义的 **alias**,后者会扇出到多个账号 / provider。
3. **注入** 对应 attempt 的 API key,从 OS keyring 中取出。
4. **尝试、重试、熔断** —— attempt 之间使用 exponential backoff;target 连续失败时打开 circuit breaker;遵守 `Retry-After`。
5. **审计** 每一次 attempt,写入脱敏后的 JSONL 日志。

---

## ✨ 杀手级特性:多账号 aliases

大多数本地 proxy 只把一个模型名路由到一个 upstream。OperatorLM 允许一个模型名按优先级扇出到 **N 个 upstream**,每个 target 有独立的 rate limit,并自动 failover。

### 示例:三个 OpenAI 账号挂在同一个模型名下

```toml
# ~/.operatorlm/config.toml

[[providers]]
name        = "openai"
type        = "openai"
base_url    = "https://api.openai.com/v1"
prefix      = "openai/"
api_key_ref = "operatorlm:openai_personal"   # 默认 key

  [[providers.keys]]
  name        = "work"
  api_key_ref = "operatorlm:openai_work"

  [[providers.keys]]
  name        = "side-project"
  api_key_ref = "operatorlm:openai_side"

[[aliases]]
name     = "gpt-4o"
strategy = "order"

  [[aliases.targets]]
  provider       = "openai"
  key            = "default"          # 首先用个人 key
  upstream_model = "gpt-4o"
  order          = 1
  rpm            = 60

  [[aliases.targets]]
  provider       = "openai"
  key            = "work"             # 失败 / 429 时回退到工作 key
  upstream_model = "gpt-4o"
  order          = 2
  rpm            = 60

  [[aliases.targets]]
  provider       = "openai"
  key            = "side-project"     # 最后的兜底
  upstream_model = "gpt-4o"
  order          = 3
```

现在你的 IDE 只需要写 `model: "gpt-4o"`,OperatorLM 会依次走完所有 key 直到有一个成功。key #1 撞上 429? circuit breaker 会把它熔断 15 秒,key #2 立刻顶上。

### 示例:跨 provider failover(成本优化)

```toml
[[aliases]]
name     = "fast-llama"
strategy = "order"

  [[aliases.targets]]
  provider       = "groq"             # 免费且最快,优先尝试
  upstream_model = "llama-3.3-70b-versatile"
  order          = 1
  rpm            = 30                  # 遵守 Groq 免费档的 RPM

  [[aliases.targets]]
  provider       = "openrouter"       # Groq 被 rate-limited 时的付费 fallback
  upstream_model = "meta-llama/llama-3.3-70b-instruct"
  order          = 2
```

发送 `model: "fast-llama"` —— Groq 可用时走 Groq,不可用时走 OpenRouter。**客户端永远不需要改动。**

---

## 🛡️ 真正能 failover 的 failover

| 机制                 | 作用                                                                                          | 默认值                |
| -------------------- | --------------------------------------------------------------------------------------------- | --------------------- |
| Retry + jitter       | 每个 target 用 exponential backoff + full jitter 重试,遵守 `Retry-After`                     | 2 次 retry,500 ms 起步,10 s 封顶 |
| Circuit breaker      | target 连续失败 N 次后熔断;closed → open → half-open                                          | 3 次失败              |
| 429 cooldown         | upstream rate-limit 时的冷却时间                                                              | 15 s                  |
| 5xx cooldown         | upstream 服务端错误时的冷却时间                                                               | 60 s                  |
| 网络 cooldown        | DNS / TCP / timeout 失败时的冷却时间                                                          | 90 s                  |
| RPM limiter          | 每个 target 的 60 秒 sliding window —— 超限时 skip 而不是阻塞                                  | 每个 target 可独立配置 |
| 单次 attempt 超时    | 单次 upstream 调用的硬上限                                                                    | 60 s(总共 180 s)    |
| Stream idle timeout  | 中断卡死的 SSE 流                                                                             | 30 s                  |

以上所有参数都可以在 admin UI 的 **Reliability** 标签页里实时调整 —— 不需要重启。

---

## 🤖 ChatGPT Plus/Pro 作为后端(实验性)

<details>
<summary><strong>⚠️ 启用 <code>chatgpt-codex</code> provider 前请阅读此免责声明</strong></summary>

> [!WARNING]
> `chatgpt-codex` provider 是非官方的,未经 OpenAI 认可。它复用了 OpenAI 官方 Codex CLI 公开的 OAuth client ID(`app_EMoamEEZ73f0CkXaXp7hrann`)。
>
> - OpenAI 随时可能轮换或吊销该 ID,从而让此 provider 失效。
> - 使用方式可能违反 OpenAI 的服务条款。
> - 仅支持 `/v1/responses`(不支持 chat/completions,也不支持图像)。
> - **使用风险自负。** 如果想要有官方支持的路径,请使用 `openai` provider 加上你自己的 API key。

</details>

如果你接受这些风险:打开 admin UI,新增一个 `chatgpt-codex` provider,点击 **Login with ChatGPT** —— 浏览器会自动打开,完成登录后,token 会保存到你的 OS keyring 中并自动刷新。从这一刻起,Codex / GPT-5.x 系列模型就能通过和其他 provider 一样的 `/v1/responses` endpoint 访问。

---

## OperatorLM 横向对比

| 特性                                  | OperatorLM | LiteLLM proxy | OmniRoute |
| ------------------------------------- | :--------: | :-----------: | :-------: |
| 单一二进制,无运行时依赖              | ✅         | ❌ (Python)   | ❌        |
| 单 provider 多账号 / key 轮换         | ✅         | ✅            | ✅        |
| Circuit breaker + retry + RPM limiter | ✅         | 部分          | 部分      |
| key 存放在 OS keyring(非明文)       | ✅         | ❌            | ❌        |
| 内嵌 admin UI                         | ✅         | ✅            | ✅        |
| 原生 tray app                         | ✅         | ❌            | ❌        |
| Audit log(JSONL,脱敏)              | ✅         | ✅            | ✅        |
| 把 ChatGPT Plus/Pro 当后端用          | ✅(实验性)| ❌            | ❌        |

**如果你希望** 拥有一个 desktop-first、单二进制的 proxy,像生产服务一样处理 failover 和多账号路由 —— 同时不想跑一个 Python 服务,也不想把 key 交给别人的云,**那就用 OperatorLM**。

---

## Build from source

### 1. 编译

```powershell
# Windows
.\build.ps1
```

```bash
# Linux / macOS
./build.sh
```

> [!NOTE]
> 需要 CGO(用于 system tray + OS keyring)。
> **Linux**: 安装 `gcc libgtk-3-dev libayatana-appindicator3-dev`(Debian/Ubuntu),或 `build.sh` 中列出的 Fedora/RHEL 等价包。

### 2. 运行

```bash
# Windows
.\OperatorLM.exe

# Linux / macOS (desktop)
./OperatorLM

# Linux(headless 服务器,无 tray)
OPERATORLM_NO_TRAY=1 ./OperatorLM
```

任务栏出现托盘图标。Admin UI 位于 **<http://127.0.0.1:11434/admin/>**。

### 3. 配置(admin UI)

1. 打开 admin UI。
2. **Providers** → 新增一个 provider,选择其类型(`openai`、`openrouter`、`groq`、`gemini`、`azure-openai`、`chatgpt-codex`、`custom`)。
3. **Keys** → 粘贴你的 API key。它会被写入 OS keyring;TOML 文件只保存引用。
4. **Aliases** *(可选)* → 配置多账号 / 多 provider 的 failover。
5. **Try It** → 内嵌发一个请求验证。

### 4. 从任何会说 OpenAI 的工具中使用

```bash
curl http://127.0.0.1:11434/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"groq/llama-3.3-70b-versatile","messages":[{"role":"user","content":"hi"}]}'
```

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:11434/v1",
    api_key="not-needed",          # OperatorLM 会注入真正的 key
)
print(client.chat.completions.create(
    model="groq/llama-3.3-70b-versatile",
    messages=[{"role": "user", "content": "hi"}],
).choices[0].message.content)
```

```javascript
import OpenAI from "openai";

const openai = new OpenAI({
  baseURL: "http://127.0.0.1:11434/v1",
  apiKey: "not-needed",
});

const chat = await openai.chat.completions.create({
  model: "groq/llama-3.3-70b-versatile",
  messages: [{ role: "user", content: "hi" }],
});
console.log(chat.choices[0].message.content);
```

把 Cursor / Continue / 任何兼容 OpenAI 的客户端指向 `http://127.0.0.1:11434/v1` 即可。

---

## 支持的 endpoints

| Endpoint                       | 状态                                    |
| ------------------------------ | --------------------------------------- |
| `POST /v1/chat/completions`    | ✅ 完整支持,含 streaming                |
| `POST /v1/images/generations`  | ✅(无 streaming)                        |
| `POST /v1/responses`           | ✅(由 `chatgpt-codex` 使用)             |
| `GET  /v1/models`              | ✅ 汇总所有已配置 provider 的模型        |

## 支持的 providers

`openai` · `openrouter` · `groq` · `gemini` · `azure-openai` · `mistral` · `nvidia-nim` · `bedrock` · `opencode-zen` · `chatgpt-codex` · `custom`(任何兼容 OpenAI 的 upstream)。

---

## 配置与密钥

### 文件位置

- **Config**: `~/.operatorlm/config.toml`
- **Logs**: `~/.operatorlm/operatorlm.log`
- **Audit Log**: `~/.operatorlm/audit.log`(JSONL,脱敏)

### 你的 API key 实际存在哪里

| OS          | Backend                    | 检查方式                                          |
| ----------- | -------------------------- | ------------------------------------------------- |
| **Windows** | Credential Manager         | *控制面板 → 凭据管理器*                            |
| **macOS**   | Keychain                   | *钥匙串访问(Keychain Access)*                    |
| **Linux**   | Secret Service (D-Bus)     | `seahorse`(GNOME)或 `kwalletmanager`(KDE)      |

TOML 文件按名字引用 key(`operatorlm:openai_work`)—— 从不保存密钥本身。

> [!NOTE]
> **Linux headless**: 需要有一个运行中的 Secret Service daemon(例如 `gnome-keyring-daemon --components=secrets`)以及一个有效的 D-Bus session。

---

## 安全模型

- **仅 loopback** —— 默认绑定 `127.0.0.1`。
- **Host header 校验** —— admin API 上启用(防御 DNS rebinding)。
- **自定义 header 门控** —— 在涉及修改的 admin endpoint 上要求 `X-OperatorLM-Admin`。
- **默认无 CORS**。
- **可选的本地认证** —— 可在 admin UI 中开启本地 API key,用于在共享机器上限制访问。
- **Audit 脱敏** —— `Authorization` 及其他敏感 header 在写入 audit log 之前始终被脱敏。

---

## 仓库结构

```
internal/
  config/      # TOML + OS keyring 集成
  providers/   # openai · openrouter · groq · gemini · azure · chatgpt-codex · custom
  router/      # alias resolver · retry · circuit breaker · rate limiter
  server/      # HTTP handlers + 内嵌 admin UI (web/)
  audit/       # 非阻塞的 JSONL audit logger
  tray/        # 跨平台 system tray
main.go        # 入口
```

整个代码库小到一个下午就能审完。这正是本意。

---

## 项目状态与贡献

个人项目,按现状发布 —— 但仍在持续使用与维护。

如果 OperatorLM 帮你节省了时间或简化了配置,**在 GitHub 上点一颗 ⭐ 是最友好的感谢**,也能让更多开发者发现它。

欢迎提交 bug 报告、pull request 以及新 provider 的接入。

**License**: [MIT](LICENSE)
