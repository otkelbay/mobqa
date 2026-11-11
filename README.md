<p align="center"><img src="docs/assets/logo.svg" width="96" alt="MobQA"></p>

# MobQA

MobQA writes and runs end-to-end tests for mobile apps on real phones. You
describe a test in plain language; an LLM agent drives a phone step by step,
records what it did as a replayable script, and remembers what it learned about
the app for next time. Runs replay those scripts on any phone attached to your
hubs, with retries, results and a video of every attempt.

This repository holds the Go backend in [`server/`](server) and the iOS screen
bridge in [`ios/`](ios/mobqa-ios-bridge).

## How it fits together

```
 browser ──HTTP──▶ mobqa-api ◀──WebSocket── mobqa-hub ──adb / devicectl──▶ phones
                      │  ▲                     │
                      ▼  │                     └─ SFU: live screens, attempt videos
                  PostgreSQL + pgvector
                      ▲
                 mobqa-worker ──▶ LLM (any OpenAI-compatible API)
```

- **`mobqa-api`** serves the REST API, holds the WebSocket links that hubs dial
  out on, relays live-screen signalling, and runs the job pools that drive
  devices: test runs and agent authoring.
- **`mobqa-worker`** runs the jobs that only need the database and the model:
  build analysis, test generation, knowledge indexing and review of finished
  authorings.
- **`mobqa-hub`** runs on a machine with phones plugged in. It needs no public
  address: it connects out to the API, discovers devices, executes commands and
  scripts, performs API calls that tests make, and hosts a WebRTC SFU that
  forwards a phone's screen to browsers and films each attempt.

## Inside the server

- **Strict layers.** `transport → usecase ← store`. Usecases own their types and
  declare the small interfaces they consume; only `store/postgres` contains SQL;
  only transport types carry `json` tags. `depguard` enforces the import
  directions. Rules for contributors and agents are in
  [`server/AGENTS.md`](server/AGENTS.md).
- **A job pool built on Postgres.** Every background workload is a leased job:
  claimed with `FOR UPDATE SKIP LOCKED`, kept alive by a heartbeat goroutine,
  woken by `LISTEN/NOTIFY` instead of polling, cancelled by `NOTIFY`, retried
  with capped backoff, drained on shutdown. Each pool owns its goroutines and
  returns only when they have exited.
- **RAG that the agent writes to.** Notes are chunked and embedded into
  pgvector with an HNSW index. Each authoring retrieves pinned notes and the
  chunks closest to the step at hand within a token budget; after it finishes,
  a review job distils up to three lessons back into the project's knowledge.
- **Secrets stay out of prompts.** Environment secrets are sealed with AES-GCM.
  The model only ever sees `{{VARIABLE}}` names; the hub substitutes the values
  on the device.
- **Tested at three levels.** Unit tests for every usecase with fakes at the
  boundaries, integration tests for every store method against a real Postgres
  (a fresh schema per test), and an e2e suite that drives every domain over
  HTTP and also runs the three binaries as processes against a stub LLM and a
  fake adb.

The design, its trade-offs and a dated decision log are in
[`docs/design.md`](docs/design.md).

## Quick start

```sh
cp .env.example .env    # fill in the two secrets and an LLM API key
docker compose up -d    # Postgres with pgvector, the API on :18080, the worker
```

Then run a hub on a machine with phones attached. Register it in a project to
get its token:

```sh
cd server
go build -o bin/ ./cmd/mobqa-hub
MOBQA_API_URL=http://localhost:18080 MOBQA_HUB_TOKEN=mqh_... ./bin/mobqa-hub
```

Android needs `adb`; iOS needs Xcode's `devicectl`; `ffmpeg` enables attempt
videos.

## Development

Go 1.25, PostgreSQL 16 with pgvector, `staticcheck` and `golangci-lint` v2.

```sh
cd server
make check    # gofmt, vet, staticcheck, golangci-lint, go test -race
MOBQA_TEST_DATABASE_URL=postgres://... make check    # plus integration and e2e
```

## License

[Apache 2.0](LICENSE)
