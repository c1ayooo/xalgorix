# Xalgorix — AI Autonomous Penetration Testing

**Self-hosted AI pentester that finds, exploits, and verifies real vulnerabilities.**

Most scanners *detect*. Xalgorix *proves* — an autonomous LLM agent works a full 22-phase methodology, then an independent verifier re-exploits every finding before it's reported, so you get proof with evidence, not a wall of maybes to triage.

This image is **batteries-included**: it's built on Kali Linux with hundreds of offensive-security tools preinstalled, and it keeps every package manager available so the agent can auto-install anything else it needs at runtime.

- 🧠 **An AI agent, not a template engine** — reasons about auth flows, business logic, IDOR/BOLA, and chained exploits signature scanners miss.
- ✅ **Exploit-verified findings** — a separate verifier reproduces each finding; inconclusive ones are flagged, never dressed up as confirmed.
- 🔒 **Self-hosted & private** — runs on your infrastructure with your own LLM key. No target data, keys, or findings leave your machine.
- 🧩 **Bring your own LLM** — OpenAI, Anthropic, DeepSeek, Gemini, Groq, Ollama, or MiniMax.
- 📄 **Audit-ready reports** — branded PDFs with CVSS scores, proof-of-concept, and remediation.

---

## Quick start

```bash
docker run --rm -p 9137:9137 \
  -e XALGORIX_LLM=openai/gpt-5.6 \
  -e XALGORIX_API_KEY=your_openai_api_key \
  -v xalgorix-data:/data \
  c1ayooo/xalgorix:latest
```

Then open **http://127.0.0.1:9137**.

For best autonomous scanning results, choose a current frontier model with strong reasoning and reliable tool calling. The example model can be replaced with any newer compatible provider model ID.

> Use Xalgorix only against systems you own or are explicitly authorized to test.

---

## Reviewing GitHub pull requests (free, no install)

Prefer security review straight on your PRs? Install the **[Xalgorix GitHub App](https://github.com/apps/xalgorix/installations/new)** — it comments a security review on each pull request's diff (injection, broken auth/IDOR, SSRF, secrets, unsafe patterns), updates in place on new commits, and re-runs when you comment `@xalgorix review`. No workflow file, API key, or account required. This container image is for the deeper, self-hosted exploit-verified pentest.

---

## What's inside

An extensive toolset ships preinstalled — `nmap`, `nuclei`, `httpx`, `subfinder`, `dnsx`, `naabu`, `katana`, `ffuf`, `gobuster`, `dalfox`, `feroxbuster`, `sqlmap`, `masscan`, `nikto`, `whatweb`, `hydra`, SecLists, and the broader Kali web/vulnerability/fuzzing/passwords metapackages — plus Chromium for browser-assisted DAST. The Go, Rust (cargo), Python (pipx/pip), and npm toolchains stay in the image, so the agent can install anything not baked in, on demand.

## Configuration

| Variable | Required | Description |
| --- | --- | --- |
| `XALGORIX_LLM` | ✅ | Model ID, usually with a provider prefix, e.g. `openai/gpt-5.6`. For best results use a current frontier model; MiniMax and local models remain supported. |
| `XALGORIX_API_KEY` | ✅ | API key for the configured LLM provider. |
| `XALGORIX_API_BASE` | — | Custom OpenAI-compatible base URL. |
| `XALGORIX_USERNAME` / `XALGORIX_PASSWORD` | — | Dashboard auth. **Required** before exposing the dashboard beyond localhost. |
| `XALGORIX_DATA_DIR` | — | Scan output / reports location. Defaults to `/data` (mount a volume). |

- **Port:** `9137` (dashboard + API). The container binds `0.0.0.0` internally; publish with `-p 9137:9137`.
- **Volume:** mount `/data` to persist scans and reports across restarts.

## Tags

| Tag | Meaning |
| --- | --- |
| `latest` | Most recent release (all-in-one). |
| `X.Y.Z` (e.g. `4.5.50`) | A specific, immutable release (all-in-one). |
| `X.Y` (e.g. `4.5`) | Latest patch of that minor line (all-in-one). |
| `X.Y.Z-allinone`, `X.Y-allinone` | Explicit alias of an all-in-one release. |
| `X.Y.Z-slim`, `X.Y-slim` | Control-plane-only variant of a release. |
| `slim` | Most recent release of the control-plane-only variant. |

**Platforms:** `linux/amd64` and `linux/arm64`. Docker automatically selects the native image for the host.

## Image variants

| Variant | Contents | Use it for |
| --- | --- | --- |
| **all-in-one** (`latest`, `X.Y.Z`, `X.Y`, `<version>-allinone`) | Platform + full Kali toolset + runtime toolchains | Single-container scans. This is the default `docker build` target and is unchanged. |
| **slim** (`slim`, `<version>-slim`) | Platform binary, CA certificates and timezone data only — no scanners, no package managers | Split deployments: the platform is the control plane and every scan's tools run in a per-project toolbox container (`XALGORIX_RUNTIME_BACKEND=docker`). |

`latest` and the semver tags keep pointing at all-in-one, so existing users are unaffected. Slim images are never privileged: they do not execute tools themselves.

### Split deployment (slim + toolbox containers)

Prerequisites:

1. **PostgreSQL** — required by the platform since v3.0.
2. **Digest-pinned toolbox image** — e.g. `ghcr.io/c1ayooo/xalgorix-toolbox:4.6.133-core@sha256:<64hex>`. Only `repo@sha256:...` is accepted; tag references are refused unless `XALGORIX_ALLOW_UNPINNED_TOOLBOX=1` (development only).
3. **Proxy URL reachable from the project network** — set `XALGORIX_TOOLBOX_PROXY_URL`. Docker-backend tools fail closed when proxy is required and this is unset; they never silently fall back to a direct connection.
4. **Host data directory at an identical absolute path** — e.g. `/srv/xalgorix/data` mounted the same way in the platform container and readable by the runner.

Build the pieces locally:

```bash
make image-platform PLATFORM_VARIANT=slim   # <repo>:<version>-slim
make image-toolbox  TOOLBOX_PROFILE=core    # <toolbox-repo>:<version>-core
make image-runnerd                          # host-side runtime service
```

Start the split stack (the all-in-one service has no profile, so name `app-split` to avoid starting it too):

```bash
XALGORIX_DATA_DIR=/srv/xalgorix/data \
XALGORIX_TOOLBOX_IMAGE=ghcr.io/c1ayooo/xalgorix-toolbox:4.6.133-core@sha256:<64hex> \
XALGORIX_TOOLBOX_PROXY_URL=http://<proxy-host>:<port> \
  docker compose -f deploy/docker-compose.yml --profile split up -d app-split
```

The dashboard is published on `127.0.0.1:9138` by default (`XALGORIX_SPLIT_PORT` overrides it); `allinone` remains the default shape on `9137` and `--profile allinone` is equivalent to it.

Current limits of the split shape: the platform CLI wiring for project quotas, max-life and host-path preflight is not finished yet, and `web`-profile browser automation's CDP endpoint is only reachable from a host-binary platform (no relay inside containers yet). The proxy must be provided by the operator — the platform's built-in traffic proxy listens on loopback.

## Security notes

- The container runs as **root by design** — the engine only enables runtime tool auto-install for uid 0, and `apt`/`go`/`cargo` installs need system write access. Treat the container as a disposable, network-isolated scanning sandbox.
- **Never expose the dashboard publicly without `XALGORIX_USERNAME`/`XALGORIX_PASSWORD`.** The server refuses external binding without auth.
- No scan data, API keys, or findings leave the container unless you configure outbound integrations (Discord/Telegram/webhooks).

## Links

- **Source & docs:** https://github.com/c1ayooo/xalgorix
- **Documentation:** https://docs.xalgorix.com
- **Hosted (no install):** https://www.xalgorix.com
- **One-line install (Linux or macOS, amd64/arm64):** `curl -sSL https://www.xalgorix.com/install | bash`

Released under the Apache License 2.0.
