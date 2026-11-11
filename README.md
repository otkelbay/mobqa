<div align="center">

<img src="docs/assets/logo.svg" width="88" alt="MobQA logo">

# MobQA

**Describe a mobile test in plain language. An agent writes it on a real phone. Runs replay it.**

[![server](https://github.com/otkelbay/mobqa/actions/workflows/server.yml/badge.svg)](https://github.com/otkelbay/mobqa/actions/workflows/server.yml)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](server/go.mod)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16%20%2B%20pgvector-4169E1?logo=postgresql&logoColor=white)](server/internal/store/postgres/migrations)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue)](LICENSE)

[How it works](#how-it-works) · [Architecture](#architecture) · [Engineering](#engineering-highlights) · [Quick start](#quick-start) · [Configuration](#configuration) · [Development](#development)

</div>

---

MobQA writes and runs end-to-end tests for Android and iOS apps on real devices.
You write steps like *"sign in, add the first product to the cart, check the
total"*. An LLM agent drives a phone step by step, turns what it did into a
replayable script, and keeps notes on what it learned about the app for next
time. Runs replay those scripts on any phone attached to your hubs, with
retries, per-step results and a video of every attempt.

This repository contains the Go backend ([`server/`](server)) and the iOS
screen-streaming bridge ([`ios/`](ios/mobqa-ios-bridge)).

## How it works

| | |
| --- | --- |
| **1. Describe** | Write a test as plain-language steps, or let the model draft tests from a short brief about the app. Every edit creates a new version. |
| **2. Author** | The agent looks at the device's UI tree, picks one action, performs it through the hub and repeats. The result is a script of concrete operations with selectors. |
| **3. Learn** | Knowledge notes are retrieved into every prompt. After each authoring, a review job writes the lessons the agent learned back into the project's knowledge. |
| **4. Run** | Scripts replay on any matching device, with retries. A pass after a failure is marked flaky, and each attempt is filmed. |
| **5. Watch** | Live device screens stream to the browser over WebRTC. Engineers can tap and type on a remote phone. |

## Architecture

```mermaid
flowchart LR
    browser([Browser])
    llm([LLM provider<br/>OpenAI-compatible])

    subgraph cloud [Server]
        api[mobqa-api<br/>REST · hub links · run & authoring jobs]
        worker[mobqa-worker<br/>analysis · generation · indexing · review]
        db[(PostgreSQL<br/>+ pgvector)]
    end

    subgraph office [Any machine with phones]
        hub[mobqa-hub<br/>device agent · WebRTC SFU]
        phones[[Android · iOS]]
    end

    browser -- HTTP --> api
    hub -- outbound WebSocket --> api
    hub -- adb · devicectl --> phones
    phones -. H.264 .-> hub
    hub -. WebRTC .-> browser
    api --- db
    worker --- db
    api --> llm
    worker --> llm
```

| Binary | Runs | Responsibilities |
| --- | --- | --- |
| `mobqa-api` | Server | REST API, the WebSocket links hubs dial out on, live-screen signalling, and the job pools that drive devices (runs and authoring) |
| `mobqa-worker` | Server | Jobs that need only the database and the model: build analysis, test generation, knowledge indexing, authoring review |
| `mobqa-hub` | Next to the phones | Device discovery, commands and script replay, the HTTP calls tests make, a pion SFU that forwards phone screens and records attempts |

Hubs need no public address: they connect out to the API and reconnect with
jittered backoff, so a laptop on office Wi-Fi works as a device farm.

## Engineering highlights

**Strict layering.** Dependencies point inward: `transport → usecase ← store`.
Usecases own their types and declare the small interfaces they consume. SQL
lives only in `store/postgres` and `json` tags only in transport. `depguard`
enforces the import directions, and the house rules are in
[`server/AGENTS.md`](server/AGENTS.md).

**A job pool on Postgres alone.** Every background workload is a leased job:
- claimed with `FOR UPDATE SKIP LOCKED`, so no two workers hold the same job;
- kept alive by a heartbeat goroutine, so a crashed worker's job is picked up
  again when its lease lapses;
- woken by `LISTEN/NOTIFY` instead of polling, and cancelled the same way;
- retried with capped exponential backoff, and drained on shutdown.

Each pool owns its goroutines and returns only after they have exited.

**RAG the agent writes to.** Notes are chunked and embedded into pgvector with
an HNSW index. Each authoring gets the pinned notes plus the chunks nearest to
the current step, within a token budget. If the embedding model is unavailable,
retrieval falls back to pinned notes instead of failing. A review job then
writes up to three lessons back. It may rewrite or retire only the agent's own
notes, never what people wrote.

**Secrets never reach the model.** Environment secrets are sealed with
AES-GCM. Prompts name `{{VARIABLES}}`, and the hub substitutes the values on the
device. Values that API calls extract during a test are redacted from the model too.

**Multi-tenant from the start.** Everything is scoped to a project and checked
against the caller's role (viewer, engineer, admin, owner). Hub tokens and
stream keys are stored only as hashes. Lessons are not shared across projects,
so one tenant's details cannot leak into another's prompts.

**Tested at three levels.**

| Level | What it covers |
| --- | --- |
| Unit | Every usecase through its public methods, with fakes only at the boundaries (store, hub link, LLM, clock) |
| Integration | Every store method against a real Postgres, in a fresh schema per test |
| End-to-end | Every domain over HTTP; plus the three real binaries started as processes against a stub LLM and a fake `adb`, each required to exit cleanly on `SIGTERM` |

The design, its trade-offs and a dated decision log are in
[`docs/design.md`](docs/design.md).

## Quick start

**Server** (Docker):

```sh
cp .env.example .env    # set the two secrets and an LLM API key
docker compose up -d    # Postgres + pgvector, the API on :18080, the worker
```

**Hub** (on a machine with phones attached). Register a hub in a project to get
its token, then:

```sh
cd server && go build -o bin/ ./cmd/mobqa-hub
MOBQA_API_URL=http://localhost:18080 MOBQA_HUB_TOKEN=mqh_... ./bin/mobqa-hub
```

The hub needs `adb` for Android and Xcode's `devicectl` for iOS. With `ffmpeg`
installed it also records attempt videos.

## Configuration

All settings come from environment variables. The main ones:

| Variable | Used by | Default | Purpose |
| --- | --- | --- | --- |
| `DATABASE_URL` | api, worker | — | PostgreSQL with the `vector` extension available |
| `MOBQA_TOKEN_SECRET` | api | — | Signs access and refresh tokens (≥ 32 characters) |
| `MOBQA_SECRET_KEY` | api | — | Seals environment secrets (32 bytes, base64) |
| `MOBQA_LLM_API_KEY` | api, worker | — | Key for the model provider |
| `MOBQA_LLM_BASE_URL` | api, worker | OpenRouter | Any OpenAI-compatible endpoint |
| `MOBQA_LLM_MODEL` | api, worker | `anthropic/claude-sonnet-5` | Chat model for authoring, generation, review |
| `MOBQA_EMBEDDING_MODEL` | api, worker | `openai/text-embedding-3-small` | 1536-dimension embeddings for knowledge |
| `MOBQA_HTTP_ADDRESS` | api | `:18080` | Listen address |
| `MOBQA_BLOB_DIR` | api, worker | `data/blobs` | Builds and attempt videos |
| `MOBQA_ICE_SERVERS` | api | public STUN | STUN/TURN URLs for browsers watching live screens |
| `MOBQA_API_URL` | hub | — | Base URL of the API |
| `MOBQA_HUB_TOKEN` | hub | — | Token issued when the hub was registered |

Worker counts, leases, timeouts and paths of external tools can be tuned as
well; [`internal/config`](server/internal/config/config.go) lists every setting
with its default and validation.

## Development

You need Go 1.25, PostgreSQL 16 with pgvector, `staticcheck` and
`golangci-lint` v2.

```sh
cd server
make check                                         # gofmt, vet, staticcheck, golangci-lint, go test -race
MOBQA_TEST_DATABASE_URL=postgres://... make check  # plus integration and end-to-end tests
```

```
server/
├── cmd/            mobqa-api · mobqa-worker · mobqa-hub
├── e2e/            end-to-end suite
└── internal/
    ├── app/        wiring and lifecycle, no logic
    ├── transport/  http handlers and DTOs · hubws hub protocol
    ├── usecase/    business rules, one package per domain
    ├── store/      postgres (all SQL, embedded migrations) · blob
    ├── worker/     lease-based job pool
    ├── llm/        chat and embeddings over langchaingo
    ├── device/     adb and devicectl adapters
    └── sfu/        pion SFU and mp4 recorder
```

## Status

The backend is complete and covered by tests. A web app built on this API and
the iOS bridge's move to WHIP-style publishing are in progress.

## License

[Apache 2.0](LICENSE)
