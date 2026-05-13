# OperatorLM

[English](README.md) | [Español](README.es.md) | [Português](README.pt.md) | [简体中文](README.zh.md)

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go](https://img.shields.io/badge/Go-1.22%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![Platforms](https://img.shields.io/badge/platforms-Windows%20%7C%20macOS%20%7C%20Linux-blue)](#build-from-source)
[![Single binary](https://img.shields.io/badge/binary-~11MB-success)](#)
[![No Docker](https://img.shields.io/badge/no%20Docker-required-brightgreen)](#)
[![Keys: OS Keyring](https://img.shields.io/badge/keys-OS%20keyring-informational)](#configuration--secrets)

> **A local, OpenAI-compatible proxy with real failover, multi-account aliasing, and zero secrets on disk.**
> One tiny binary sits between your IDE/SDK and every LLM provider you use — OpenAI, OpenRouter, Groq, Google Gemini, Azure OpenAI, and even your **ChatGPT Plus/Pro** subscription.

![OperatorLM Banner](images/banner.png)

> [!IMPORTANT]
> OperatorLM listens on `127.0.0.1:11434` by default — the same port as Ollama. Any tool already pointing at Ollama works out of the box. If you run both, change one in `~/.operatorlm/config.toml`.

## Quick Start

**Just want to run it?** Grab the latest binary — no Go toolchain, no Docker.

### 1. Download

Head to the [Releases page](https://github.com/aralde/operatorlm/releases/latest) and download the asset for your OS:

| OS                    | Asset                              |
| --------------------- | ---------------------------------- |
| Windows (x64)         | `OperatorLM-windows-amd64.exe`     |
| macOS (Apple silicon) | `OperatorLM-darwin-arm64`          |
| macOS (Intel)         | `OperatorLM-darwin-amd64`          |
| Linux (x64)           | `OperatorLM-linux-amd64`           |

> [!NOTE]
> On macOS/Linux, mark the binary executable: `chmod +x OperatorLM-*`.

### 2. Run

- **Windows**: double-click `OperatorLM-windows-amd64.exe`. A tray icon appears (no console window). On first launch, SmartScreen may show *"Windows protected your PC"* → click **More info → Run anyway** (the binary is unsigned).
- **macOS desktop**: `./OperatorLM-darwin-*` — tray icon appears. On first launch, Gatekeeper will block it → right-click the binary in Finder → **Open** → **Open** again in the dialog (only needed once).
- **Linux desktop**: `./OperatorLM-linux-amd64` — tray icon appears.
- **Linux headless**: `OPERATORLM_NO_TRAY=1 ./OperatorLM-linux-amd64`.

### 3. Open the admin UI

Browse to **<http://127.0.0.1:11434/admin/>** → add a provider → paste an API key → send a test request from the **Try It** tab.

### 4. Point your tools at it

Any OpenAI-compatible client works — set the base URL to `http://127.0.0.1:11434/v1` and any non-empty `api_key` (OperatorLM injects the real one). Copy-paste samples for `curl` / Python / JavaScript live in [Use it from anything that speaks OpenAI](#4-use-it-from-anything-that-speaks-openai) below.

> Prefer to build from source? See [Build from source](#build-from-source).

---

## Demo

![OperatorLM Demo](images/demo.gif)

## Table of Contents

- [Quick Start](#quick-start)
- [Why this project?](#why-this-project)
- [Use Cases](#use-cases)
- [How it works](#how-it-works)
- [The killer feature: multi-account aliases](#-the-killer-feature-multi-account-aliases)
- [Failover that actually fails over](#%EF%B8%8F-failover-that-actually-fails-over)
- [ChatGPT Plus/Pro as a backend (experimental)](#-chatgpt-pluspro-as-a-backend-experimental)
- [How OperatorLM compares](#how-operatorlm-compares)
- [Build from source](#build-from-source)
- [Supported endpoints](#supported-endpoints)
- [Supported providers](#supported-providers)
- [Configuration & secrets](#configuration--secrets)
- [Security model](#security-model)
- [Repository layout](#repository-layout)
- [Status & contributing](#status--contributing)

## Why this project?

- 🔀 **Multi-account aliasing** — Stack 3 OpenAI keys, 2 OpenRouter accounts, and a free Groq backup behind a single model name. OperatorLM walks the list until one succeeds.
- 🛡️ **Production-grade failover** — Per-target **circuit breaker** (3-state), retries with exponential backoff + jitter, sliding-window **RPM limiter**. Different cooldowns for 429 / 5xx / network errors.
- 🔐 **Zero secrets on disk** — API keys live in **Windows Credential Manager / macOS Keychain / Linux Secret Service**. The TOML file only holds *references*, never the keys themselves.
- 🖥️ **Embedded live admin UI** — Manage providers, keys, aliases, reliability settings, and watch the audit log stream — all from `http://127.0.0.1:11434/admin/`. Zero install: it's `go:embed`ded in the binary.
- 🧪 **Audit log as JSONL** — Every request: model, attempt, upstream URL, status, duration. Authorization headers redacted by default. Non-blocking writer.
- 🤖 **ChatGPT Plus/Pro as a backend** — Sign in once via OAuth (PKCE), get your Plus/Pro quota wired into the same OpenAI-compatible API your tools already speak. *(Experimental — see disclaimer below.)*
- 🪶 **One ~11 MB binary, ~50 MB RAM at idle** — Native Go. No `node_modules`, no Python, no Docker. Starts in milliseconds.
- 🛰️ **Headless mode** — `OPERATORLM_NO_TRAY=1` to run on a Linux box without a desktop session.
- 🪟 **No console flash on Windows** — Built with `-H=windowsgui`. It's a real tray app.

---

## Use Cases

- **Stretch free / cheap tiers** — chain Groq → OpenRouter → OpenAI behind one alias. Day-to-day coding hits the free tier, and only spills to paid when the free tier is rate-limited.
- **Multiple personal/work accounts under one model name** — keep `openai_personal`, `openai_work`, and `openai_side` keys separate in the OS keyring, expose them to Cursor/Continue as a single `gpt-4o` model, and let the router walk them on 429s.
- **ChatGPT Plus/Pro instead of API credits** — sign in once via OAuth and route Codex-class GPT-5.x calls through your existing Plus/Pro quota, no API billing involved *(experimental — see [disclaimer](#-chatgpt-pluspro-as-a-backend-experimental))*.
- **Drop-in Ollama replacement** — OperatorLM binds on `127.0.0.1:11434`, so anything already pointed at Ollama (Continue, Cline, Open WebUI, Zed, …) keeps working unchanged but now reaches OpenAI / OpenRouter / Gemini / Azure / Bedrock / etc.
- **Auditable LLM traffic on a dev box or shared workstation** — every request lands in redacted JSONL, keys stay in the OS keyring (never on disk), and the admin UI is loopback-only with host-header validation and an optional local API key.
- **Headless self-hosted gateway** — run on a Linux VM / NAS / home server with `OPERATORLM_NO_TRAY=1`, reach `127.0.0.1:11434` over WireGuard or Tailscale from your laptop, and centralize your keys and audit log in one place.

---

## How it works

![OperatorLM request flow](images/flow.png)

1. **Receive** an OpenAI-format request on `127.0.0.1:11434`.
2. **Resolve** the `model` field → either a prefix match (`openai/gpt-4o`, `groq/llama-3.3-70b-versatile`) or a user-defined **alias** that fans out across multiple accounts/providers.
3. **Inject** the right API key from the OS keyring per attempt.
4. **Try, retry, and break** — exponential backoff between attempts; circuit-break the target on repeated failures; honor `Retry-After`.
5. **Audit** every attempt to a redacted JSONL log.

---

## ✨ The killer feature: multi-account aliases

Most local proxies route one model name to one upstream. OperatorLM lets one model name fan out to **N upstreams in priority order**, with per-target rate limits and automatic failover.

### Example: three OpenAI accounts behind one model name

```toml
# ~/.operatorlm/config.toml

[[providers]]
name        = "openai"
type        = "openai"
base_url    = "https://api.openai.com/v1"
prefix      = "openai/"
api_key_ref = "operatorlm:openai_personal"   # default key

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
  key            = "default"          # personal key first
  upstream_model = "gpt-4o"
  order          = 1
  rpm            = 60

  [[aliases.targets]]
  provider       = "openai"
  key            = "work"             # fall back to work key on failure / 429
  upstream_model = "gpt-4o"
  order          = 2
  rpm            = 60

  [[aliases.targets]]
  provider       = "openai"
  key            = "side-project"     # last resort
  upstream_model = "gpt-4o"
  order          = 3
```

Now your IDE just says `model: "gpt-4o"` and OperatorLM walks the keys until one succeeds. Hit the 429 wall on key #1? It's circuit-broken for 15 s and key #2 takes over immediately.

### Example: cross-provider fallback (cost optimization)

```toml
[[aliases]]
name     = "fast-llama"
strategy = "order"

  [[aliases.targets]]
  provider       = "groq"             # free + fastest, try first
  upstream_model = "llama-3.3-70b-versatile"
  order          = 1
  rpm            = 30                  # respect Groq's free-tier RPM

  [[aliases.targets]]
  provider       = "openrouter"       # paid fallback if Groq is rate-limited
  upstream_model = "meta-llama/llama-3.3-70b-instruct"
  order          = 2
```

Send `model: "fast-llama"` — get Groq when available, OpenRouter when it isn't. **No client-side changes ever.**

---

## 🛡️ Failover that actually fails over

| Mechanism            | What it does                                                                                  | Default              |
| -------------------- | --------------------------------------------------------------------------------------------- | -------------------- |
| Retry + jitter       | Per-target retries with exponential backoff, full jitter, honors `Retry-After`                | 2 retries, 500 ms base, 10 s cap |
| Circuit breaker      | Trips per target after N consecutive failures; closed → open → half-open                      | 3 failures           |
| 429 cooldown         | Cooldown when upstream rate-limits                                                            | 15 s                 |
| 5xx cooldown         | Cooldown on upstream server errors                                                            | 60 s                 |
| Network cooldown     | Cooldown on DNS / TCP / timeout failures                                                      | 90 s                 |
| RPM limiter          | Sliding 60-second window per target — skip rather than block                                  | configurable per target |
| Per-attempt timeout  | Hard cap per upstream call                                                                    | 60 s (180 s total)   |
| Stream idle timeout  | Aborts a dead SSE stream                                                                      | 30 s                 |

All of these are tunable live from the **Reliability** tab in the admin UI — no restart needed.

---

## 🤖 ChatGPT Plus/Pro as a backend (experimental)

<details>
<summary><strong>⚠️ Read this disclaimer before enabling the <code>chatgpt-codex</code> provider</strong></summary>

> [!WARNING]
> The `chatgpt-codex` provider is unofficial and not endorsed by OpenAI. It reuses the public OAuth client ID from OpenAI's official Codex CLI (`app_EMoamEEZ73f0CkXaXp7hrann`).
>
> - OpenAI can rotate or revoke the ID at any time, breaking this provider.
> - Usage may violate OpenAI's Terms of Service.
> - Only `/v1/responses` is supported (no chat/completions, no images).
> - **Use at your own risk.** For a supported path, use the `openai` provider with your own API key.

</details>

If you accept the risks: open the admin UI, add a `chatgpt-codex` provider, click **Login with ChatGPT** — a browser opens, you sign in, and tokens are stored in your OS keyring with automatic refresh. From then on, Codex-class GPT-5.x models are reachable through the same `/v1/responses` endpoint as any other provider.

---

## How OperatorLM compares

| Feature                                  | OperatorLM | LiteLLM proxy | OmniRoute |
| ---------------------------------------- | :--------: | :-----------: | :-------: |
| Single binary, no runtime                | ✅         | ❌ (Python)    | ❌        |
| Multi-account / key rotation per provider| ✅         | ✅             | ✅        |
| Circuit breaker + retry + RPM limiter    | ✅         | partial        | partial   |
| Keys in OS keyring (no plaintext)        | ✅         | ❌             | ❌        |
| Embedded admin UI                        | ✅         | ✅             | ✅        |
| Native tray app                          | ✅         | ❌             | ❌        |
| Audit log (JSONL, redacted)              | ✅         | ✅             | ✅        |
| ChatGPT Plus/Pro subscription as backend | ✅ (exp.)  | ❌             | ❌        |

**Pick OperatorLM if** you want a desktop-first, single-binary proxy that handles failover and multi-account routing like a production service — without running a Python service or sending your keys through someone else's cloud.

---

## Build from source

### 1. Build

```powershell
# Windows
.\build.ps1
```

```bash
# Linux / macOS
./build.sh
```

> [!NOTE]
> CGO is required (for system tray + OS keyring).
> **Linux**: install `gcc libgtk-3-dev libayatana-appindicator3-dev` (Debian/Ubuntu) or the Fedora/RHEL equivalents listed in `build.sh`.

### 2. Run

```bash
# Windows
.\OperatorLM.exe

# Linux / macOS (desktop)
./OperatorLM

# Linux (headless server, no tray)
OPERATORLM_NO_TRAY=1 ./OperatorLM
```

A tray icon appears. The admin UI is at **<http://127.0.0.1:11434/admin/>**.

### 3. Configure (admin UI)

1. Open the admin UI.
2. **Providers** → add a provider, pick its type (`openai`, `openrouter`, `groq`, `gemini`, `azure-openai`, `chatgpt-codex`, `custom`).
3. **Keys** → paste your API key. It's written to the OS keyring; the TOML file only stores the reference.
4. **Aliases** *(optional)* → set up multi-account / multi-provider failover.
5. **Try It** → fire a request inline to verify.

### 4. Use it from anything that speaks OpenAI

```bash
curl http://127.0.0.1:11434/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"groq/llama-3.3-70b-versatile","messages":[{"role":"user","content":"hi"}]}'
```

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:11434/v1",
    api_key="not-needed",          # OperatorLM injects the real key
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

Point Cursor / Continue / any OpenAI-compatible client at `http://127.0.0.1:11434/v1` and you're done.

---

## Supported endpoints

| Endpoint                       | Status                                  |
| ------------------------------ | --------------------------------------- |
| `POST /v1/chat/completions`    | ✅ Full, streaming                       |
| `POST /v1/images/generations`  | ✅ (no streaming)                        |
| `POST /v1/responses`           | ✅ (used by `chatgpt-codex`)             |
| `GET  /v1/models`              | ✅ Aggregated across configured providers|

## Supported providers

`openai` · `openrouter` · `groq` · `gemini` · `azure-openai` · `mistral` · `nvidia-nim` · `bedrock` · `opencode-zen` · `chatgpt-codex` · `custom` (any OpenAI-compatible upstream).

---

## Configuration & secrets

### File locations

- **Config**: `~/.operatorlm/config.toml`
- **Logs**: `~/.operatorlm/operatorlm.log`
- **Audit Log**: `~/.operatorlm/audit.log` (JSONL, redacted)

### Where your API keys actually live

| OS          | Backend                    | Where to inspect                                  |
| ----------- | -------------------------- | ------------------------------------------------- |
| **Windows** | Credential Manager         | *Control Panel → Credential Manager*              |
| **macOS**   | Keychain                   | *Keychain Access app*                             |
| **Linux**   | Secret Service (D-Bus)     | `seahorse` (GNOME) or `kwalletmanager` (KDE)      |

The TOML file references keys by name (`operatorlm:openai_work`) — never the secret itself.

> [!NOTE]
> **Linux headless**: requires a running Secret Service daemon (e.g. `gnome-keyring-daemon --components=secrets`) and a valid D-Bus session.

---

## Security model

- **Loopback only** — binds to `127.0.0.1` by default.
- **Host-header validation** on the admin API (defends against DNS rebinding).
- **Custom header gate** on mutating admin endpoints (`X-OperatorLM-Admin`).
- **No CORS** by default.
- **Optional local auth** — turn on a local API key from the admin UI to restrict access on shared machines.
- **Audit redaction** — `Authorization` and other sensitive headers are always redacted before being written to the audit log.

---

## Repository layout

```
internal/
  config/      # TOML + OS keyring integration
  providers/   # openai · openrouter · groq · gemini · azure · chatgpt-codex · custom
  router/      # alias resolver · retry · circuit breaker · rate limiter
  server/      # HTTP handlers + embedded admin UI (web/)
  audit/       # non-blocking JSONL audit logger
  tray/        # cross-platform system tray
main.go        # entrypoint
```

Codebase is small enough to audit in an afternoon. That's the point.

---

## Status & contributing

Personal project, released as-is — but actively used and maintained.

If OperatorLM saves you time or helps your setup, **a ⭐ on GitHub is the kindest thank-you** and helps other developers find it.

Bug reports, pull requests, and provider integrations are very welcome.

**License**: [MIT](LICENSE)
