# OperatorLM

[English](README.md) | [Español](README.es.md) | [Português](README.pt.md) | [简体中文](README.zh.md)

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go](https://img.shields.io/badge/Go-1.22%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![Platforms](https://img.shields.io/badge/platforms-Windows%20%7C%20macOS%20%7C%20Linux-blue)](#build-from-source)
[![Single binary](https://img.shields.io/badge/binary-~11MB-success)](#)
[![No Docker](https://img.shields.io/badge/no%20Docker-required-brightgreen)](#)
[![Keys: OS Keyring](https://img.shields.io/badge/keys-OS%20keyring-informational)](#configuração--segredos)

> **Um proxy local, compatível com OpenAI, com failover real, aliasing multi-conta e zero segredos em disco.**
> Um único binário fica entre o seu IDE/SDK e cada provider LLM que você usa — OpenAI, OpenRouter, Groq, Google Gemini, Azure OpenAI, e até a sua assinatura **ChatGPT Plus/Pro**.

![OperatorLM Banner](images/banner.png)

> [!IMPORTANT]
> OperatorLM escuta em `127.0.0.1:11434` por padrão — a mesma porta do Ollama. Qualquer ferramenta já apontada para o Ollama funciona sem mexer em nada. Se rodar os dois, troque uma em `~/.operatorlm/config.toml`.

## Quick Start

**Só quer rodar?** Baixe o último binário — sem Go toolchain, sem Docker.

### 1. Download

Vá até a [página de Releases](https://github.com/aralde/operatorlm/releases/latest) e baixe o asset do seu sistema operacional:

| OS                    | Asset                              |
| --------------------- | ---------------------------------- |
| Windows (x64)         | `OperatorLM-windows-amd64.exe`     |
| macOS (Apple silicon) | `OperatorLM-darwin-arm64`          |
| macOS (Intel)         | `OperatorLM-darwin-amd64`          |
| Linux (x64)           | `OperatorLM-linux-amd64`           |

> [!NOTE]
> No macOS/Linux, dê permissão de execução: `chmod +x OperatorLM-*`.

### 2. Executar

- **Windows**: duplo clique em `OperatorLM-windows-amd64.exe`. Aparece um ícone na bandeja (sem janela de console). Na primeira execução, o SmartScreen pode mostrar *"O Windows protegeu seu PC"* → clique em **Mais informações → Executar mesmo assim** (o binário não é assinado).
- **macOS desktop**: `./OperatorLM-darwin-*` — aparece o ícone na bandeja. Na primeira execução, o Gatekeeper vai bloquear → clique com o botão direito no binário no Finder → **Abrir** → **Abrir** de novo no diálogo (só precisa fazer uma vez).
- **Linux desktop**: `./OperatorLM-linux-amd64` — aparece o ícone na bandeja.
- **Linux headless**: `OPERATORLM_NO_TRAY=1 ./OperatorLM-linux-amd64`.

### 3. Abrir o admin UI

Acesse **<http://127.0.0.1:11434/admin/>** → adicione um provider → cole uma API key → dispare um request de teste pela aba **Try It**.

### 4. Aponte suas ferramentas para o proxy

Qualquer cliente compatível com OpenAI funciona — defina a base URL como `http://127.0.0.1:11434/v1` e qualquer `api_key` não vazia (o OperatorLM injeta a real). Exemplos prontos pra copiar-colar em `curl` / Python / JavaScript estão em [Use a partir de qualquer coisa que fale OpenAI](#4-use-a-partir-de-qualquer-coisa-que-fale-openai) mais abaixo.

> Prefere buildar do código-fonte? Veja [Build from source](#build-from-source).

---

## Demo

![OperatorLM Demo](images/OperatorLM-demo.gif)

## Sumário

- [Quick Start](#quick-start)
- [Por que esse projeto?](#por-que-esse-projeto)
- [Casos de uso](#casos-de-uso)
- [Como funciona](#como-funciona)
- [A killer feature: aliases multi-conta](#-a-killer-feature-aliases-multi-conta)
- [Failover que realmente faz failover](#%EF%B8%8F-failover-que-realmente-faz-failover)
- [ChatGPT Plus/Pro como backend (experimental)](#-chatgpt-pluspro-como-backend-experimental)
- [Como o OperatorLM se compara](#como-o-operatorlm-se-compara)
- [Build from source](#build-from-source)
- [Endpoints suportados](#endpoints-suportados)
- [Providers suportados](#providers-suportados)
- [Configuração & segredos](#configuração--segredos)
- [Modelo de segurança](#modelo-de-segurança)
- [Estrutura do repositório](#estrutura-do-repositório)
- [Status e contribuição](#status-e-contribuição)

## Por que esse projeto?

- 🔀 **Aliasing multi-conta** — empilhe 3 API keys da OpenAI, 2 contas do OpenRouter, e um Groq grátis como backup atrás de um único nome de modelo. O OperatorLM percorre a lista até uma funcionar.
- 🛡️ **Failover de nível produção** — **circuit breaker** por target (3 estados), retries com exponential backoff + jitter, **RPM limiter** com sliding window. Cooldowns diferentes para 429 / 5xx / erros de rede.
- 🔐 **Zero segredos em disco** — As API keys ficam no **Windows Credential Manager / macOS Keychain / Linux Secret Service**. O arquivo TOML só guarda *referências*, nunca as keys.
- 🖥️ **Admin UI embutida e ao vivo** — Gerencie providers, keys, aliases, ajustes de reliability, e veja o audit log em streaming — tudo a partir de `http://127.0.0.1:11434/admin/`. Zero instalação: está embutida no binário com `go:embed`.
- 🧪 **Audit log em JSONL** — Cada request: modelo, attempt, URL upstream, status, duração. Headers `Authorization` redatados por padrão. Writer não-bloqueante.
- 🤖 **ChatGPT Plus/Pro como backend** — Logue uma vez via OAuth (PKCE) e tenha a sua cota Plus/Pro plugada na mesma API compatível com OpenAI que as suas ferramentas já falam. *(Experimental — leia o disclaimer abaixo.)*
- 🪶 **Um binário de ~11 MB, ~50 MB de RAM em idle** — Go nativo. Sem `node_modules`, sem Python, sem Docker. Sobe em milissegundos.
- 🛰️ **Modo headless** — `OPERATORLM_NO_TRAY=1` para rodar numa máquina Linux sem sessão de desktop.
- 🪟 **Sem flash de console no Windows** — buildado com `-H=windowsgui`. É uma tray app de verdade.

---

## Casos de uso

- **Esticar tiers grátis / baratos** — encadeie Groq → OpenRouter → OpenAI atrás de um único alias. O dia a dia bate no free tier e só transborda para o pago quando o grátis é rate-limited.
- **Várias contas pessoais/de trabalho sob um único nome de modelo** — mantenha as keys `openai_personal`, `openai_work` e `openai_side` separadas no OS keyring, exponha para o Cursor/Continue como um único modelo `gpt-4o`, e deixe o router percorrer todas em caso de 429.
- **ChatGPT Plus/Pro em vez de créditos de API** — logue uma vez via OAuth e roteie chamadas para modelos Codex / GPT-5.x pela sua cota Plus/Pro existente, sem billing de API *(experimental — leia o [disclaimer](#-chatgpt-pluspro-como-backend-experimental))*.
- **Substituto drop-in para Ollama** — o OperatorLM escuta em `127.0.0.1:11434`, então qualquer coisa já apontada para o Ollama (Continue, Cline, Open WebUI, Zed, …) continua funcionando sem mudanças mas agora alcança OpenAI / OpenRouter / Gemini / Azure / Bedrock / etc.
- **Tráfego LLM auditável numa máquina de dev ou estação compartilhada** — todo request cai em JSONL redatado, as keys ficam no OS keyring (nunca em disco), e o admin UI é loopback-only com validação de host-header e uma API key local opcional.
- **Gateway self-hosted em modo headless** — rode numa VM Linux / NAS / home server com `OPERATORLM_NO_TRAY=1`, alcance `127.0.0.1:11434` via WireGuard ou Tailscale do seu laptop, e centralize as suas keys e o audit log num único lugar.

---

## Como funciona

![Fluxo de requests do OperatorLM](images/OperatorLM-flow.png)

1. **Recebe** um request no formato OpenAI em `127.0.0.1:11434`.
2. **Resolve** o campo `model` → ou por match de prefixo (`openai/gpt-4o`, `groq/llama-3.3-70b-versatile`), ou por um **alias** definido pelo usuário que se espalha entre várias contas/providers.
3. **Injeta** a API key correta do OS keyring a cada attempt.
4. **Tenta, faz retry e abre** — exponential backoff entre tentativas; abre o circuit breaker do target em falhas repetidas; respeita `Retry-After`.
5. **Audita** cada attempt num log JSONL redatado.

---

## ✨ A killer feature: aliases multi-conta

A maioria dos proxies locais roteia um nome de modelo para um único upstream. O OperatorLM deixa um nome de modelo se espalhar para **N upstreams em ordem de prioridade**, com rate limits por target e failover automático.

### Exemplo: três contas OpenAI atrás de um único nome de modelo

```toml
# ~/.operatorlm/config.toml

[[providers]]
name        = "openai"
type        = "openai"
base_url    = "https://api.openai.com/v1"
prefix      = "openai/"
api_key_ref = "operatorlm:openai_personal"   # key padrão

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
  key            = "default"          # key pessoal primeiro
  upstream_model = "gpt-4o"
  order          = 1
  rpm            = 60

  [[aliases.targets]]
  provider       = "openai"
  key            = "work"             # cai para a key do trabalho em falha / 429
  upstream_model = "gpt-4o"
  order          = 2
  rpm            = 60

  [[aliases.targets]]
  provider       = "openai"
  key            = "side-project"     # último recurso
  upstream_model = "gpt-4o"
  order          = 3
```

Agora o seu IDE só diz `model: "gpt-4o"` e o OperatorLM percorre as keys até uma funcionar. Bateu num 429 na key #1? O circuit breaker abre por 15 s e a key #2 assume na hora.

### Exemplo: failover cross-provider (otimização de custo)

```toml
[[aliases]]
name     = "fast-llama"
strategy = "order"

  [[aliases.targets]]
  provider       = "groq"             # grátis + mais rápido, tente primeiro
  upstream_model = "llama-3.3-70b-versatile"
  order          = 1
  rpm            = 30                  # respeita o RPM do free tier do Groq

  [[aliases.targets]]
  provider       = "openrouter"       # fallback pago se o Groq estiver rate-limited
  upstream_model = "meta-llama/llama-3.3-70b-instruct"
  order          = 2
```

Mande `model: "fast-llama"` — pega Groq quando disponível, OpenRouter quando não. **Zero mudanças no client.**

---

## 🛡️ Failover que realmente faz failover

| Mecanismo            | O que faz                                                                                       | Default              |
| -------------------- | ----------------------------------------------------------------------------------------------- | -------------------- |
| Retry + jitter       | Retries por target com exponential backoff, full jitter, respeita `Retry-After`                 | 2 retries, 500 ms base, 10 s cap |
| Circuit breaker      | Abre por target após N falhas consecutivas; closed → open → half-open                           | 3 falhas             |
| Cooldown 429         | Cooldown quando o upstream rate-limitar                                                         | 15 s                 |
| Cooldown 5xx         | Cooldown em erros de servidor upstream                                                          | 60 s                 |
| Cooldown de rede     | Cooldown em falhas de DNS / TCP / timeout                                                       | 90 s                 |
| RPM limiter          | Sliding window de 60 segundos por target — skip em vez de bloquear                              | configurável por target |
| Timeout por attempt  | Cap duro por chamada upstream                                                                   | 60 s (180 s total)   |
| Stream idle timeout  | Aborta um stream SSE morto                                                                      | 30 s                 |

Tudo isso é ajustável ao vivo pela aba **Reliability** do admin UI — sem restart.

---

## 🤖 ChatGPT Plus/Pro como backend (experimental)

<details>
<summary><strong>⚠️ Leia esse disclaimer antes de habilitar o provider <code>chatgpt-codex</code></strong></summary>

> [!WARNING]
> O provider `chatgpt-codex` não é oficial nem endossado pela OpenAI. Ele reusa o client ID público de OAuth do Codex CLI oficial da OpenAI (`app_EMoamEEZ73f0CkXaXp7hrann`).
>
> - A OpenAI pode rotacionar ou revogar esse ID a qualquer momento e quebrar esse provider.
> - O uso pode violar os Termos de Serviço da OpenAI.
> - Só `/v1/responses` é suportado (sem chat/completions, sem imagens).
> - **Use por sua conta e risco.** Para um caminho suportado, use o provider `openai` com a sua própria API key.

</details>

Se você aceita o risco: abra o admin UI, adicione um provider `chatgpt-codex`, clique em **Login with ChatGPT** — um navegador abre, você loga, e os tokens são guardados no seu OS keyring com refresh automático. A partir daí, modelos Codex / GPT-5.x ficam acessíveis pelo mesmo endpoint `/v1/responses` de qualquer outro provider.

---

## Como o OperatorLM se compara

| Feature                                  | OperatorLM | LiteLLM proxy | OmniRoute |
| ---------------------------------------- | :--------: | :-----------: | :-------: |
| Binário único, sem runtime               | ✅         | ❌ (Python)    | ❌        |
| Multi-conta / rotação de keys por provider | ✅       | ✅             | ✅        |
| Circuit breaker + retry + RPM limiter    | ✅         | parcial        | parcial   |
| Keys no OS keyring (sem plaintext)       | ✅         | ❌             | ❌        |
| Admin UI embutida                        | ✅         | ✅             | ✅        |
| Tray app nativa                          | ✅         | ❌             | ❌        |
| Audit log (JSONL, redatado)              | ✅         | ✅             | ✅        |
| ChatGPT Plus/Pro como backend            | ✅ (exp.)  | ❌             | ❌        |

**Escolha o OperatorLM se** quer um proxy desktop-first, de um único binário, que faz failover e routing multi-conta como um serviço de produção — sem rodar um serviço Python nem mandar as suas keys pela cloud de outra pessoa.

---

## Build from source

### 1. Buildar

```powershell
# Windows
.\build.ps1
```

```bash
# Linux / macOS
./build.sh
```

> [!NOTE]
> CGO é necessário (para system tray + OS keyring).
> **Linux**: instale `gcc libgtk-3-dev libayatana-appindicator3-dev` (Debian/Ubuntu) ou os equivalentes de Fedora/RHEL listados em `build.sh`.

### 2. Executar

```bash
# Windows
.\OperatorLM.exe

# Linux / macOS (desktop)
./OperatorLM

# Linux (servidor headless, sem tray)
OPERATORLM_NO_TRAY=1 ./OperatorLM
```

Aparece um ícone na bandeja. O admin UI fica em **<http://127.0.0.1:11434/admin/>**.

### 3. Configurar (admin UI)

1. Abra o admin UI.
2. **Providers** → adicione um provider, escolha o tipo (`openai`, `openrouter`, `groq`, `gemini`, `azure-openai`, `chatgpt-codex`, `custom`).
3. **Keys** → cole a sua API key. Ela é gravada no OS keyring; o TOML só guarda a referência.
4. **Aliases** *(opcional)* → monte failover multi-conta / multi-provider.
5. **Try It** → dispare um request inline para verificar.

### 4. Use a partir de qualquer coisa que fale OpenAI

```bash
curl http://127.0.0.1:11434/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"groq/llama-3.3-70b-versatile","messages":[{"role":"user","content":"hi"}]}'
```

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:11434/v1",
    api_key="not-needed",          # OperatorLM injeta a key real
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

Aponte Cursor / Continue / qualquer cliente compatível com OpenAI para `http://127.0.0.1:11434/v1` e pronto.

---

## Endpoints suportados

| Endpoint                       | Status                                  |
| ------------------------------ | --------------------------------------- |
| `POST /v1/chat/completions`    | ✅ Completo, com streaming               |
| `POST /v1/images/generations`  | ✅ (sem streaming)                       |
| `POST /v1/responses`           | ✅ (usado pelo `chatgpt-codex`)          |
| `GET  /v1/models`              | ✅ Agregado entre os providers configurados |

## Providers suportados

`openai` · `openrouter` · `groq` · `gemini` · `azure-openai` · `mistral` · `nvidia-nim` · `bedrock` · `opencode-zen` · `chatgpt-codex` · `custom` (qualquer upstream compatível com OpenAI).

---

## Configuração & segredos

### Localização dos arquivos

- **Config**: `~/.operatorlm/config.toml`
- **Logs**: `~/.operatorlm/operatorlm.log`
- **Audit Log**: `~/.operatorlm/audit.log` (JSONL, redatado)

### Onde as suas API keys realmente moram

| OS          | Backend                    | Onde inspecionar                                  |
| ----------- | -------------------------- | ------------------------------------------------- |
| **Windows** | Credential Manager         | *Painel de Controle → Gerenciador de Credenciais* |
| **macOS**   | Keychain                   | *App Acesso às Chaves (Keychain Access)*          |
| **Linux**   | Secret Service (D-Bus)     | `seahorse` (GNOME) ou `kwalletmanager` (KDE)      |

O TOML referencia as keys por nome (`operatorlm:openai_work`) — nunca o segredo em si.

> [!NOTE]
> **Linux headless**: requer um daemon de Secret Service rodando (ex.: `gnome-keyring-daemon --components=secrets`) e uma sessão D-Bus válida.

---

## Modelo de segurança

- **Só loopback** — escuta em `127.0.0.1` por padrão.
- **Validação de host header** na admin API (defesa contra DNS rebinding).
- **Header custom obrigatório** em endpoints admin mutantes (`X-OperatorLM-Admin`).
- **Sem CORS** por padrão.
- **Auth local opcional** — ative uma API key local pelo admin UI para restringir acesso em máquinas compartilhadas.
- **Redação no audit** — `Authorization` e outros headers sensíveis são sempre redatados antes de irem para o audit log.

---

## Estrutura do repositório

```
internal/
  config/      # TOML + integração com OS keyring
  providers/   # openai · openrouter · groq · gemini · azure · chatgpt-codex · custom
  router/      # alias resolver · retry · circuit breaker · rate limiter
  server/      # handlers HTTP + admin UI embutido (web/)
  audit/       # audit logger JSONL não-bloqueante
  tray/        # system tray cross-platform
main.go        # entrypoint
```

A codebase é pequena o bastante para ser auditada numa tarde. Esse é o ponto.

---

## Status e contribuição

Projeto pessoal, liberado como está — mas ativamente usado e mantido.

Se o OperatorLM te economiza tempo ou facilita o seu setup, **uma ⭐ no GitHub é o "obrigado" mais gentil** e ajuda outros devs a encontrarem o projeto.

Bug reports, pull requests e integrações de providers são muito bem-vindos.

**Licença**: [MIT](LICENSE)
