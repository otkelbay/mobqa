# server: design

The MobQA backend lives in `server/` as a strictly layered Go module. It replaced
an earlier prototype, which is referred to below as "the old backend". Code rules
are in `server/AGENTS.md`.

## Scope

Auth and users; projects with members and roles, environments with sealed
secrets and a sign-in profile; builds (APK/IPA, iOS provisioning, change
analysis); hubs and devices; tests with versioned plain-language steps; test
generation; agentic authoring that turns steps into replayable scripts; runs
with retries, results, attempts and videos; knowledge (RAG) that the agent
reads and writes; API actions; remote control and live screens.

Left out of the rewrite on purpose: notifications, integrations and
subscriptions, static scripted tests, the video analyzer, the admin API.

## Binaries

| Binary | Role |
| --- | --- |
| `cmd/mobqa-api` | HTTP API, hub WebSocket endpoint, live-screen signalling, and the job pools that drive devices (runs, authoring), because only the API holds the hub links |
| `cmd/mobqa-worker` | Job pools that only need the database and the model: build analysis, test generation, knowledge indexing, authoring review |
| `cmd/mobqa-hub` | Device agent on a machine with phones attached; dials the API, drives adb and `devicectl`, hosts the SFU and films attempts |

Each `main` parses config, opens dependencies and calls `app.RunAPI` /
`app.RunWorker` / `app.RunHub`, which wire everything by hand and own shutdown.

## Layout

```
server/
  cmd/{mobqa-api,mobqa-worker,mobqa-hub}/
  e2e/                end-to-end suite: in-process API and the three binaries as processes
  internal/
    app/              wiring and lifecycle, no logic
    config/           typed config from the environment, Validate()
    fault/            error kinds shared by every layer
    transport/
      http/           handlers, request/response DTOs (the only json tags), middleware
      hubws/          hub protocol: message types, API-side server, hub-side client
    usecase/
      auth/ user/ project/ environment/ build/ hub/ device/ test/ run/
      authoring/ agent/ knowledge/ action/ chat/ ui/
    store/
      postgres/       all SQL, file per area, own row types; embedded migrations
      blob/           build and video files on disk
    worker/           generic lease-based job pool
    llm/              chat and embeddings over langchaingo (OpenAI-compatible)
    device/           adb and devicectl adapters used by the hub
    sfu/              pion SFU and mp4 recorder on the hub
    secret/           AES-GCM sealing of environment secrets
    httpcall/         the HTTP client hubs use to perform API actions
    clock/            injected clock
    dbtest/           a fresh schema per integration test
```

### Layer rules (enforced by depguard in `.golangci.yml`)

- `transport/*` imports `usecase/*` only (plus stdlib). It decodes, validates
  the DTO, maps it field by field to a usecase input, calls one usecase method,
  maps the result and errors back.
- `usecase/*` declares the small interfaces it consumes (`type testStore
  interface{...}`) next to the code that uses them. It never imports `store`,
  `transport`, `device`, `llm` or `sfu` concretely. Inputs and results are
  usecase-owned structs without tags; each input has `Validate() error`.
- `store/*`, `device`, `llm`, `sfu`, `httpcall` implement those interfaces. The
  interfaces speak usecase types, so adapters import the usecase package for
  those types only and map from their own row and wire types at their edge
  (dependencies point inward: `transport → usecase ← store`).
- `app` is the only package that sees everything.

### Errors

The leaf package `fault` defines the error kinds (`ErrNotFound`, `ErrConflict`,
`ErrInvalidInput`, `ErrUnauthorized`, `ErrForbidden`, `ErrUnavailable`).
Usecases wrap them with context. Store adapters translate `pgx.ErrNoRows` and
unique violations into `fault` kinds and wrap everything else. `transport/http` has one table mapping
sentinels to status + fixed message; nothing user-supplied or SQL reaches a
response or log.

### Logging

One `*slog.Logger` injected everywhere, children via `With("component", ...)`,
static messages, fixed attribute vocabulary: `component, request_id, user_id,
project_id, test_id, run_id, job_id, job_kind, device_id, hub_id, attempt,
duration_ms, code, method, route, status, address, error`. Log once, where the outcome is decided.

## Concurrency model

### Job pool (`internal/worker`)

Every background workload is a job in `jobs` (`kind, payload, state,
lease_owner, lease_until, attempts, run_after`). A `worker.Pool` has a fixed
number of goroutines per kind:

- **Claim** with `UPDATE ... WHERE id = (SELECT ... FOR UPDATE SKIP LOCKED)`
  setting a lease; no two workers ever hold one job.
- **Heartbeat**: a per-job goroutine extends the lease at a third of its length;
  a crashed worker's job becomes claimable when the lease lapses.
- **Wake-up**: `LISTEN jobs_<kind>` replaces polling; once a subscription is in
  place (first time and after a reconnect) the pool claims once, so a job
  enqueued before it is not left for the slow safety-net ticker.
- **Cancellation**: `NOTIFY job_cancel` carries the job id; the pool cancels
  that job's context. No cancel-flag polling.
- **Shutdown**: `Pool.Run(ctx)` owns every goroutine it starts on one
  `sync.WaitGroup`. On `ctx.Done()` it stops claiming, gives in-flight jobs a
  drain deadline, then cancels them and releases their leases. `Run` returns
  only when all goroutines have exited.
- **Retries**: failures record `attempts` and `run_after` with capped
  exponential backoff; a job handler returns `worker.Permanent(err)` to stop.

Handlers are usecase methods behind `worker.Handler` (`Handle(ctx, Job)
error`); the pool knows nothing about runs or authoring.

### Device concurrency

A device is a single-writer resource. `device.Locks` hands out one slot per
device (a run attempt, an authoring session or a remote-control command at a
time), and each process orders only the devices it drives: the API for the hub
links it holds, a hub for its own phones.

### Hub link

The hub dials `/hub/connect` and keeps one WebSocket with a reader goroutine, a
writer goroutine fed by a channel, and request/response correlation by message
id with per-request timeouts. Reconnect uses jittered exponential backoff. All
goroutines are owned by `hubws.Client.Run` and stop with its context.

### Progress

Runs and authorings write their state as they go; clients poll the run and
authoring endpoints. Cancellation goes through `NOTIFY job_cancel` to the pool
that holds the job.

## Agent and RAG

- **`llm`**: `Chat` and `Embed` over langchaingo's OpenAI-compatible client,
  pointed at OpenRouter by default. Usecases depend on small interfaces; tests
  use scripted fakes.
- **`usecase/knowledge`**: notes (note, flow, gotcha, selector) written by people
  or by the agent. An `index` job chunks and embeds each note into
  `knowledge_chunks(embedding vector(1536))` with an HNSW index. `Retrieve` puts
  pinned notes first, then the chunks nearest in meaning to the step at hand,
  within a token budget; an unavailable embedding model degrades to pinned
  notes only.
- **`usecase/authoring`**: an agent loop over the hub link. Each turn it observes
  the screen (UI dump), asks the model for one action, sends it to the device
  and records it as a script op. Environment values never reach the model: the
  prompt names `{{VARIABLES}}` and the hub substitutes them.
- **Review**: after each authoring a `review` job reads its log and writes up to
  three lessons as agent notes, and may rewrite or retire only the agent's own
  notes, so the knowledge base improves without touching what people wrote.
- **`usecase/run`**: replays the scripts on hub devices, with retries; a pass
  after a failure is `flaky`.

## Data

A fresh schema in embedded, numbered migrations that the API and the worker apply
on start under an advisory lock. UUIDv7 primary keys, `timestamptz` everywhere,
times bound from the injected clock, never `now()` in SQL.

## API

REST on `net/http` `ServeMux` patterns, everything scoped under
`/projects/{projectID}` and checked against the caller's role. Lists are pages
of at most 100. Auth: short-lived JWT access tokens and rotating refresh tokens.

## Tests

- Unit: every usecase through its public methods with fakes only at boundaries
  (store, hub link, llm, clock). Table-driven, no funcs in tables.
- Integration (`//go:build integration`): every store method against a real
  Postgres (`MOBQA_TEST_DATABASE_URL`), each test in its own schema.
- E2E (`//go:build e2e`): the API wired in-process on `httptest.Server` with
  real Postgres and fake device/llm adapters, driving user flows over HTTP.
  One test builds `mobqa-api`, `mobqa-worker` and `mobqa-hub` and runs them as
  processes configured only by environment, against an empty schema, an
  OpenAI-compatible stub and a shell-script adb, and requires each to exit 0
  on SIGTERM.
- `make check`: gofmt, vet, staticcheck, golangci-lint, `go test -race`, then
  integration and e2e when a database is available.

## Progress

- [x] Scaffold: module, AGENTS.md, config, clock, slog, app lifecycle, HTTP
      server with middleware, Postgres pool, migration runner, dbtest harness,
      Makefile `check`, `.golangci.yml` with depguard layer rules
- [x] worker pool with lease, heartbeat, LISTEN/NOTIFY, cancel, drain (+ tests)
- [x] auth + users
- [x] projects, environments, auth profiles
- [x] builds + blob store + iOS provisioning
- [x] devices + hubs + hub protocol
- [x] tests, steps, versions
- [x] test generation + `llm` chat adapter
- [x] runs, results, attempts; run job handler
- [x] knowledge (RAG) + embed jobs; agent-edit chat
- [x] authoring: observe/act over the hub link, agent loop, scripts
- [x] runs replay scripts; build download for remote hubs
- [x] analysis jobs that write agent knowledge notes
- [x] agent memory, project memory
- [x] API actions library (CRUD)
- [x] `call` steps: hubs perform API actions for authoring and runs
- [x] remote control: screenshots and commands over the hub link
- [x] live screens: WHIP/WHEP-style signalling through the hub link, hub SFU
- [x] session recordings: the hub films attempts from the SFU and uploads mp4s
- [x] e2e suite: every domain over HTTP, plus the three binaries as processes
- [x] switch: the old backend removed, root README, docker compose quickstart, CI
- [ ] web app: port to this API (published separately once it is done)
- [ ] iOS bridge: publish with the stream key over WHIP-style `POST`

## Decision log

- 2026-11-10: layer-first layout; stdlib `ServeMux` instead of chi; one generic
  job table instead of per-domain claim queries; LISTEN/NOTIFY instead of
  polling for wake-up and cancellation; langchaingo only behind `llm`.
- 2026-11-11: dependencies point inward (`transport → usecase ← store`); store
  imports usecase types instead of the reverse, because a consumer-defined
  interface must be expressed in the consumer's types. Error kinds moved to a
  shared leaf package `fault` so transport maps them in one place instead of
  per usecase package.
- 2026-11-11: integration and e2e tests run against `MOBQA_TEST_DATABASE_URL`
  (locally `pgvector/pgvector:pg16` container `mobqa-next-test` on
  127.0.0.1:25432); each test gets its own schema with `search_path` set, and
  the pgvector extension lives in `public`.
- 2026-11-11: linters pinned: staticcheck 2025.1.1, golangci-lint v2.1.6.
- 2026-11-11: worker pool. Heartbeats also report a pending cancellation, so a
  cancel `NOTIFY` lost during a listener reconnect still reaches the job within a
  third of a lease; a job reclaimed after its owner died carries the flag and is
  cancelled without running. Shutdown releases interrupted jobs with the attempt
  refunded. Panics in handlers fail the job permanently (a bug does not improve
  on retry). Backoff is capped exponential without jitter: claims are already
  spread by `SKIP LOCKED`, and jitter would need a randomness seam for no gain.
  `clock.Clock` stamps stored times; pool timers use real durations from config,
  so pool unit tests use millisecond leases and synchronise on events, not
  sleeps. Enqueue takes a delay rather than an absolute time so the store's
  clock is the only one involved. Log vocabulary gained `job_kind` and `error`.
- 2026-11-11: auth. Registration is one step (email, password, optional name)
  and returns a session; the legacy SMS step and the fake card step are dropped
  because a self-hosted tool has no SMS provider and no billing. Refresh tokens
  are stored as SHA-256 hashes, rotate on every use and belong to a family per
  sign-in; replaying a used token revokes the family (OAuth 2.0 BCP). Login
  compares against a decoy bcrypt hash for unknown emails so timing does not
  reveal accounts. `fault.WithDetail` carries constant, client-safe messages
  for validation and conflicts; every other error becomes a logged 500. pgx
  scans `timestamptz` in UTC so stored and clock times compare equal. E2E
  tests live in `server/e2e` and drive `app.NewAPI` over `httptest`.
- 2026-11-11: projects. Membership has roles (owner > admin > engineer >
  viewer); the legacy API let any signed-in user read any project's
  environments, so every project-scoped usecase now goes through
  `project.Access.Require`. Outsiders get 404 "project not found" rather than
  403 so ids do not reveal projects. Ownership comes only from creating a
  project and cannot be reassigned or removed; the owner archives instead
  (soft delete, `archived_at`). `PATCH /projects/{id}` replaces the legacy
  `/appearance` and `/settings` endpoints. Per-project statistics (test and run
  counts) move to the tests/runs step, where the data lives. Malformed path ids
  are treated as missing rows in the store (404), not as database errors.
- 2026-11-11: environments. Variables are typed (string, number, secret, phone,
  email, url) and validated per type; the legacy free-form `{key: value}` map is
  dropped, and the frontend adapts later. Secret values are sealed with
  AES-256-GCM (`internal/secret`, key `MOBQA_SECRET_KEY`, base64) inside the
  store, bound to `environmentID/name` as associated data so a ciphertext copied
  to another row or variable fails to open; non-secret values stay readable for
  debugging. Responses mask secrets as `********`; writing the mask or an empty
  value back keeps the stored secret, so the UI can round-trip an environment
  without ever holding secrets. `Resolve` is the only unmasked read and serves
  runs. Names are unique per project (409) and a partial unique index keeps one
  default environment.
- 2026-11-11: builds. Packages stream through the API (multipart, fields
  before the file part) straight into blob storage while hashed and size-capped;
  nothing is buffered. The legacy presigned upload URL is dropped: one path is
  simpler, and hubs download through `GET .../download`, which supports Range
  requests, so a remote hub can fetch a build the API stored (the legacy gap
  where a remote hub could not get the APK). Blob storage is a directory behind
  `os.Root` (atomic temp-file + rename); an S3 adapter waits for the recordings
  step, the first place it pays off. `os.Root.MkdirAll`/`Rename` raised the
  module to Go 1.25. The usecase picks the build id (UUIDv7) because the blob key
  and the analysis job need it before the row exists, and the store inserts the
  build and enqueues its `build_analysis` job in one transaction. Change analysis
  (version, build number, size, file-level zip diff) moved from a
  fire-and-forget goroutine to a retried worker job, run by the new
  `mobqa-worker` binary. A package must be a zip with `AndroidManifest.xml`
  (APK) or `Payload/*.app/` (IPA); for iOS the embedded profile's name, team,
  bundle id and expiry are read, and an expired build cannot become current
  (409). One current build per platform (partial unique index). Delete removes
  the row before the blob: an orphaned blob costs space, a row without bytes
  breaks installs.
- 2026-11-11: hubs and devices. Hubs belong to a project (there is no global
  admin role, and project roles already answer "who may register a hub"):
  admins register, rotate tokens and delete; viewers list hubs and devices. A
  hub token (`mqh_` prefix for secret scanners) is shown once and stored as a
  SHA-256 hash. Presence is a stored window (`online_until`) that heartbeats
  extend by three intervals and a clean disconnect closes, so every API replica
  agrees on who is online without sharing memory. Device status (online,
  unauthorized, offline) is derived on read from the reported state and the
  hub's window, never stored, so it cannot go stale. Inventory reconciliation
  upserts devices by `(hub_id, serial)` in one `unnest` statement, keeping ids
  stable across reconnects, and marks missing devices disconnected; this
  replaces the legacy behaviour where devices had to be inserted by hand
  before a hub could report them.
- 2026-11-11: hub link. `transport/hubws` holds both ends over
  `github.com/coder/websocket` (context-aware; already used by the legacy
  backend). A `peer` owns one connection: reader and writer goroutines, one
  goroutine per served request capped at 32 (excess is refused, not queued),
  and request/response correlation by id. The writer sends a heartbeat after
  15 s of silence, so both sides see traffic and a 45 s read timeout only fires
  on a dead link; heartbeats from the hub extend its presence. Reads and writes
  use `context.WithoutCancel` timeouts because cancelling a coder/websocket
  read drops the connection without a close frame; `run` closes with a status
  derived from the cause instead (policy violation for a bad inventory, going
  away for shutdown or replacement). The first hub frame must be its inventory.
  A reconnecting hub replaces its old connection without being marked offline.
  Hijacked connections are invisible to `http.Server.Shutdown`, so the HTTP
  server shuts the hub link down explicitly. `Server.Call` routes to a hub
  connected to this replica only; cross-replica routing (LISTEN/NOTIFY or a
  sticky load balancer) is deferred until a second replica exists. The hub side
  is `cmd/mobqa-hub` → `usecase/agent` (merges discoverers and fails whole, since
  a partial inventory would disconnect devices) with `device/adb` (`adb
  devices -l` + `getprop`) and `device/devicectl` (Xcode's JSON listing; paired
  but unreachable devices are left out). The client reconnects with jittered
  exponential backoff and stops for good on 401, since only a new token helps.
- 2026-11-11: tests. Steps live only in immutable `test_versions` snapshots
  (jsonb); `tests` points at its current version, and reads join the two, so
  there is no copy of the content to drift. Title, description and steps are
  versioned; priority and tags are metadata and change in place. A content
  change needs `base_version` and the store checks it under `FOR UPDATE`, so two
  editors cannot silently overwrite each other (409). Restore records the old
  content as a new version instead of rewinding, keeping the history append-only.
  A test targets one platform; "both" is two tests (owner decision 2026-07-09).
  The step action set is closed so runs and authoring can rely on it. Step
  `screen_id` and version artifacts wait for the authoring item. Generation moved
  to its own item because it needs the LLM adapter.
- 2026-11-11: test generation is a job, not a long HTTP call: `POST
  /projects/{id}/test-generations` records a pending generation and queues
  `test_generation` in one transaction and answers 202; the client polls `GET
  …/test-generations/{id}`. (A separate path because `/tests/generations/{id}`
  would conflict with `/tests/{testID}/versions` in `ServeMux`.) The worker's
  `test.Generator` asks the model for strict JSON, gets one repair turn on a
  malformed reply, keeps only valid tests and saves them with the generation's
  completion in one transaction under `FOR UPDATE`, so a repeated job creates
  nothing twice. A truncated reply (finish reason `length`) is treated as
  unusable instead of being half-parsed, which the legacy analyzer got wrong.
  Unavailable models are retried by the job; a missing API key or unusable
  replies fail the generation at once with a fixed reason. Shared model
  vocabulary lives in `usecase/chat` (like `fault`), each usecase declares its own
  `model` interface, and `internal/llm` implements it over langchaingo's
  OpenAI-compatible client (OpenRouter by default, `max_tokens` field because
  OpenRouter documents it). The worker starts without a key and reports the gap
  per generation. Photo/video generation stays dropped (the legacy API already
  answered 410). Agent-edit chat moves to the knowledge item, where it gets
  retrieval context.
- 2026-11-11: runs. A run fans out into one result per test and device and one
  `run_device` job per device; the device job works through its results in order
  and retries each up to the run's budget (0–2). Results are snapshots: the test
  version's steps are copied and device/hub ids carry no foreign key, so deleting
  a device or editing a test never rewrites history. The run pool is hosted in
  the API process because only the API holds hub connections; with one replica
  that is exact, and routing jobs to the replica that holds a hub is deferred
  until there is more than one. Shutdown drains run jobs before the HTTP server
  and hub link stop, so in-flight attempts can still reach their hubs. Locks are
  always taken run first, then results (cancel, start and finish share
  `lockRunOf`), so cancelling cannot deadlock with a finishing result; cancel
  closes the run, its unfinished results and their jobs in one transaction. An
  unreachable hub fails the job (retried with backoff); on the last job attempt
  the device's remaining results close as errors instead of hanging. A hub that
  received an execution but refused it (`*run.HubError`, e.g. no runner) is an
  errored attempt, not a job failure, so retries count against the result. The
  hub protocol gained a generic `cancel` event carrying the request id, so an
  abandoned call (cancelled run, timeout) stops on the hub too. Build delivery to
  a remote hub is still open: an execution carries the build id and sha only;
  the download endpoint comes with the real hub runner in the authoring item.
- 2026-11-11: the worker pool lost a wake-up when a job was enqueued between its
  first claim and its `LISTEN`; it showed as a deterministic integration
  failure once timings shifted. `worker.Receiver` now has `Subscribed()`, which
  the Postgres listener calls after `LISTEN` and the pool answers with a claim.
- 2026-11-11: knowledge. The legacy project memory (one hand-curated summary
  injected whole into every prompt) becomes `knowledge_notes`: typed notes
  (note, flow, gotcha, selector) written by members or, later, by the agent.
  Notes are chunked (at most 1000 runes, split at paragraphs, then words),
  embedded by `knowledge_index` jobs and stored in pgvector behind an HNSW cosine
  index, searched with `hnsw.iterative_scan = strict_order` so a project filter
  does not starve the result. Indexing is idempotent by revision: every text
  change bumps `revision` and enqueues a job, the job saves chunks only if the
  revision it embedded is still current (`FOR UPDATE`), and a stale or deleted
  note is a no-op. An embedding model that is not configured, or returns vectors
  that are not 1536 wide, is a permanent job error, so notes stay visibly
  unindexed instead of retrying forever. `Retrieve` puts pinned notes first,
  then the closest chunks above a similarity floor, within a rune budget; when
  the model is unavailable it degrades to pinned notes and says so (`Partial`).
  pgvector-go v0.1.1 is used because it needs no further modules; vectors go
  over the wire as text. The default embedding model is
  `openai/text-embedding-3-small` (`MOBQA_EMBEDDING_MODEL`), the index pool size
  is `MOBQA_INDEX_WORKERS`, and the API now reads `MOBQA_LLM_*` too, for search
  and agent-edit. `worker.IsPermanent` is exported for handlers that need to
  tell outcomes apart.
- 2026-11-11: agent-edit chat
  (`POST /projects/{id}/tests/{testID}/agent-edit`) is synchronous: it persists
  nothing, so there is no job to recover. It sends the test, the retrieved
  knowledge, the client-kept history (at most 20 turns) and the instruction, and
  returns a reply plus, when the agent changed something, the complete new
  steps with the `base_version` they were based on. The client saves them
  through the ordinary update, so a test edited meanwhile is a conflict rather
  than a silent overwrite. One repair turn covers malformed replies or invalid
  steps; a truncated reply is rejected rather than applied in part; the whole
  turn runs under a 90 s timeout.
- 2026-11-11: authoring replaces the opencode `test-author` and the map-first
  fast path with one in-process agent loop. Manual steps are free text
  ("input Phone" carries no value) and cannot be replayed, so an authoring
  compiles them into a script of `ui.Op`s with selectors: each turn observes
  the accessibility tree through the hub (`device.observe`, adb `uiautomator
  dump`), sends the test, the step, recent turns, retrieved knowledge,
  variable names with their hints and at most 150 numbered elements to the
  model, and performs the one JSON action it chooses (`device.act`). Selectors
  prefer the resource ID, then text, then label, then all of them plus an index;
  `type` and `clear` record a tap with the selector and then the input without
  one. Variable values never reach the model: they are typed on the device from
  `{{NAME}}` and redacted back to `{{NAME}}` on every screen (values of at least
  four runes, longest first). Missteps the agent can fix (a malformed reply, an
  unknown variable, a bad direction, a command the device refused) are logged and
  shown to it on the next turn; limits are 12 turns per step, 150 in all,
  20 minutes and 500 tokens per turn. An unreachable hub or model is retried
  until the job's last attempt; an unconfigured model fails at once. One active
  authoring per test is enforced by a partial unique index. Authoring jobs run
  in the API, which alone holds the hub links (single replica, as for runs).
  Runs and authorings share an in-memory `device.Locks`, valid for that
  single-replica deployment; the hub agent orders its own commands with its own
  locks. adb `input text` cannot type non-ASCII text, so such typing is refused as
  a misstep instead of being mistyped. The app is assumed to be installed during
  authoring; installing the run's build is part of script replay.
- **Script replay and build install.** A run pins, per test, the script of the
  latest succeeded authoring of the test's current version; a test without one
  is refused with a conflict, so nothing runs unscripted. The hub's player waits
  for each op's element (re-reading a screen whose dump failed), acts, and lets
  the app settle; ops of step 0 prepare the app. Whatever the device could not
  do is a failed step, not an error, because hub-side controllers return plain
  errors (only the API maps a refusal to `*ui.DeviceError`); a stopped execution
  stays an error. The run's build reaches a remote hub over HTTP:
  `GET /hub/builds/{id}` authenticates the hub token and serves only builds of
  the hub's project. The hub caches packages named by SHA-256 and platform
  (installers require `.apk`/`.ipa`), verifies size and digest before an atomic
  rename, keeps the three most recently used, and installs a build on a device
  only when it is not the one this hub installed there last. A package that
  cannot be installed is an error outcome, as no step was tried. iOS install is
  not implemented yet: such a run reports that the hub cannot install iOS builds.
- **Authoring reviews.** Finishing an authoring that took at least one turn
  queues an `authoring_review` job in the same transaction; an authoring that
  never started (no model, deleted environment, app that would not launch)
  taught nothing and gets none. Reviews run in the worker binary, since they need
  the model but no hub link (`MOBQA_REVIEW_WORKERS`, default 1). The model sees
  the test, the outcome, every turn, the recorded selectors and the agent's 30
  newest notes, and returns up to three `flow`/`gotcha`/`selector` lessons; one
  that names a known note rewrites it instead of adding a near-duplicate. Lessons
  are agent notes in `knowledge_notes`, so they are retrieved, indexed, edited and
  deleted like any other note; there is no separate lesson store. Saving the
  lessons and setting `authorings.reviewed_at` is one transaction, which makes a
  repeated job a no-op, and the upsert only ever rewrites the project's own agent
  notes, never a note people wrote. A malformed reply gets one repair turn, as in
  generation.
- **Agent memory.** Project memory is the knowledge notes; agent memory is the
  agent's own notes in them, kept honest by feedback instead of counters. Each
  authoring records the notes its prompt carried (`authorings.knowledge_ids`,
  no foreign key, since notes can be deleted), and its review sees which known
  lessons the agent acted on. The review can rewrite such a lesson or retire it
  (delete it, in the review's transaction) when the session proved it wrong;
  notes people wrote are never touched. The planned `helpful_count`/staleness
  re-ranking was dropped: retrieval already ranks by meaning, and a usefulness
  score would need tuning constants no test could justify, while retiring a
  wrong lesson removes the harm directly. Legacy global lessons, shared across
  projects, were not ported: in a multi-tenant service they would carry one
  customer's app details into another customer's prompts.
- **API actions.** A project keeps a library of HTTP request templates
  (`/projects/{id}/actions`, full-replace `PUT`) whose `{{NAME}}` placeholders
  are filled from the run's variables, so tokens stay in sealed environments;
  each can check the status and extract up to five variables with one-group
  regular expressions (the OTP case). The API only stores and validates them:
  hubs will perform the requests, because they sit next to the app's staging
  backend and because an API that sends user-defined requests is an SSRF hole
  into its own network. URLs must be absolute http(s) or start with a variable,
  header values are single-line. Legacy persona and media fixtures were not
  ported (they belonged to the dropped marketplace), and teardown hooks are not
  modelled: a test that needs cleanup ends with a `call` step.
- **Call steps.** A `call` step names an API action as its target and compiles
  to a script-only `call` op, like `assert`. Authoring performs it without the
  model (nothing on the screen to decide) through a new `action.perform` request
  on the hub link; runs carry the actions their scripts call, by name, in the
  execution, resolved when the run starts, so editing an action changes every
  test that calls it. Extracted variables join the run's for the steps after
  the call; in authoring they are listed to the agent by name and redacted from
  screens like environment values. A failed call (status, no match, unreachable
  backend, deleted action) fails its step, not the attempt: the app's backend is
  part of what the test checks. Values filled into a URL are percent-encoded,
  except a leading placeholder that holds the base URL, so a phone number keeps
  its `+` in a query. HTTP lives in the `httpcall` adapter on the hub (30 s
  timeout, replies read up to 1 MiB). Generation and agent-edit are told to use
  `call` only for an action the brief or the colleague names, since they do not
  see the library.
- **Remote control.** People watch a device as JPEG screenshots
  (`GET .../devices/{id}/screen`, viewers) and drive it with the same commands
  the agent uses (`POST .../devices/{id}/commands`, engineers), sent over the
  hub link as `device.capture` and `device.act`. Screenshots are re-encoded to
  JPEG on the hub because a PNG of a 1080p screen comes close to the link's
  1 MiB message limit. Every look and command takes the device's lock in the
  API, the one runs and authorings hold, and waits at most two seconds for it:
  a person must never tap in the middle of a run's step, and a 409 that says the
  device is busy is clearer than a request that hangs for a whole run. This is
  the fallback that works through any proxy; live video stays a separate
  WebRTC path.
- **Live screens.** The phone publishes its screen as H.264 over WebRTC to an
  SFU on its hub (`internal/sfu`, pion), which copies the packets to every
  browser watching without decoding them; the API only passes offers and
  answers along the hub link (`stream.publish`, `stream.watch`) and never sees
  the video. Legacy relayed trickle ICE over two extra WebSockets with a
  minted ticket; now each peer sends one offer with every candidate gathered
  and gets one answer, as in WHIP and WHEP: the phone posts `application/sdp`
  to `POST /streams/publish`, a person to `POST .../devices/{id}/stream`. One
  request per peer fits the hub link's request/reply model, needs no ticket
  store, and the extra second of gathering is paid once per session. These
  are not full WHIP/WHEP: there is no session resource to `PATCH` or `DELETE`;
  a peer ends by closing its connection, and the SFU drops peers that fail.
  Phones authenticate with a per-device stream key (`mqs_`, admin rotates it,
  only its SHA-256 is stored) instead of legacy's one bridge token shared by
  every device of every customer. The SFU keeps one stream per device that
  outlives its peers, so viewers who arrive before the phone or stay through a
  republish keep watching; viewers' keyframe requests are passed on to the
  phone. STUN/TURN URLs are configured on both the API (handed to browsers by
  `GET /ice-servers`) and the hub, defaulting to a public STUN server; TURN
  credentials minted per session are not modelled yet, so a TURN server is
  configured with static credentials in its URL list. The iOS broadcast bridge
  and the web player must move from the legacy WebSocket signalling to these
  two requests.
- **Session recordings.** Legacy recorded between `record.start` and
  `record.stop` messages the API sent around a run and served the files from
  the hub's own `/recordings/{name}`, so a video lived on one office machine
  and vanished with it. Now the hub's runner is wrapped in `agent.Film`: when
  the device is streaming, the SFU tees the phone's packets into an H.264
  writer for the attempt, ffmpeg wraps them in an mp4 without transcoding
  (`-c copy`, the frame rate taken from frames over elapsed time, since raw
  H.264 carries no timestamps), and the hub `PUT`s the file to
  `/hub/videos/{result}/{attempt}` with its hub token before it replies with
  the outcome. Uploading first means the API only takes a video for the
  attempt a result is running on that same hub, and the outcome then records
  the video's duration, or why there is none, in the attempt. A video that
  cannot be made or sent never changes the attempt's result. Videos sit in
  blob storage under the result and attempt and are served with ranges to
  the project's viewers. A hub without ffmpeg does not film; devices that do
  not stream (Android without a broadcaster) are not filmed yet: `adb shell
  screenrecord` caps at three minutes and is left for later.
- **Binaries in e2e.** The in-process e2e tests call `app.NewAPI` and
  `app.NewPools` directly, so they never exercised `cmd/*`, config loading
  from the environment, `app.RunHub`'s wiring, migrations on an empty
  database or shutdown on a signal. `TestBinariesWorkTogether` covers that
  seam with real processes. It stays one journey rather than a suite of its
  own: the domains are already covered in-process, where a failure points at
  a line rather than at a log file. The hub's PATH holds only the fake adb so
  a developer's Xcode, real iPhone or ffmpeg can never leak into the run.
- **Sign-in brief instead of MW_* variables.** The auth profile used to be
  exported into `MW_*` environment variables for Mobilewright scripts, a
  contract that left with static tests. It now becomes a short "Signing in"
  section of the authoring agent's system prompt: the method, the variables
  that hold the login, password or account (named as `{{NAME}}`, never their
  values), a fixed OTP test code stated literally, the API action that reads
  a sent code and the profile's notes. Runs no longer read the profile at all;
  the recorded script already carries the sign-in steps. Leftovers of dropped
  features went with it: `otp.persona_id` (marketplace personas),
  `biometric` (no hub command can trigger a fingerprint or face match),
  `otp.pattern` (an API action's extraction already pulls the code out of a
  response) and `cross_platform_screens` (the screens map was not ported).
  Migration 0004 was edited in place rather than followed by a drop-column
  migration because the repository has never been released.
- **Switch.** The module moved from `backend-next/` to `server/` and the old
  backend was removed. Migrations stay a single fresh baseline. The compose
  quickstart runs Postgres with pgvector, the API and the worker from one
  image; hubs run on machines with phones and are not containerised, since
  they need USB access to the devices. CI runs `make check` against a
  pgvector service, so integration and e2e tests run on every push.
