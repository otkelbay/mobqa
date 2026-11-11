# Hub architecture — remote device farm over an outbound WS agent

Status: design, agreed direction (2026-07-08). Owner: @otkelbay.

## Problem

Today the **worker** and all device-facing tooling run on the **same machine** as the
devices. `executor.Service` shells out to `adb` / `mobilecli` / `xcrun` / `opencode`
/ `node run_test.mjs` and assumes adb is local and fast
(`internal/executor/service.go`, `device_discovery.go:36`, `remote_control.go:115`).

We want to split this:

- **Cloud / office server** — API + Postgres + a run *scheduler*. Public, has an IP.
- **Hub** — a Mac in the office with **1–20 iOS/Android devices** attached. It has
  **no public IP**; it dials the cloud outbound (WS) and executes device work
  locally. There can be **N hubs** (office A, office B, a personal Mac).

## Decision

1. **Boundary:** everything that shells out to `adb/mobilecli/xcrun/opencode/node`
   runs **on the hub, locally**. The wire carries *coarse* commands (run this test,
   capture a frame, inject a tap) — never per-syscall adb. Rationale: one test =
   thousands of adb calls; relaying each over WAN is unusable.

2. **The `executor.Service` moves to the hub unchanged.** It depends only on the
   `repository.*` interfaces (`internal/repository/interfaces.go`). We implement the
   subset it needs as **WS-RPC proxies** on the hub → every `s.Runs.CreateResult` /
   `s.Tests.GetByID` becomes a small request over the socket; **Postgres stays in the
   cloud**. This avoids rewriting the battle-tested engine. Tradeoff: chattier WS
   traffic (many small DB RPCs per run) — acceptable, runs are latency-tolerant.

3. **Transport:** one persistent **WebSocket, hub → cloud**, with a multiplexed
   mini-RPC on top (corr-id). NAT-friendly (outbound only), serves both coarse jobs
   and low-latency remote-control frames on a single connection. Reconnect w/ backoff.

4. **Bulk bytes bypass the socket.** Builds (APK/IPA) are pulled by the hub over
   plain **HTTPS GET** from cloud storage; artifacts (screenshots/video) are pushed
   by the hub over **HTTPS PUT/POST**. Only control + job lifecycle + small frames go
   over the WS, keeping it responsive.

```
[cloud]  API  Postgres  scheduler  object-store          [hub = office Mac]
   ▲        │        │       │            ▲                    │
   │        └── repo-RPC ◀───┼──── wss://backend/hub/connect ──┤ hub-agent (executor.Service)
   │ browser   job dispatch ─┼──────────────────────────────▶ │ adb/mobilecli/xcrun/opencode
   │  /screen /input ────────┘   build GET / artifact PUT ◀───▶│ └─ 1–20 devices iOS/Android
```

## WebSocket protocol

### Connect & auth

- Hub dials `wss://<backend>/api/hub/connect` with `Authorization: Bearer <hub_token>`.
- Token is minted per hub, stored **hashed** in `hubs.token_hash`.
- First frame hub→cloud: `hello { hub_id, version, platforms:["android","ios"], devices:[…] }`.
- Cloud replies `hello_ok {}` or closes with a coded error.
- Heartbeat: `ping`/`pong` every ~15s; missed → cloud marks hub `offline`, requeues
  its in-flight runs.

### Envelope

Every message is JSON:

```json
{ "type": "job.run", "id": "<uuid>", "reply_to": "<uuid|null>", "payload": { … } }
```

- `id` — unique per message. Requests carry `id`; responses set `reply_to = id`.
- Multiplexing: many in-flight requests over one socket, matched by corr-id.
- Binary WS frames are used for screen PNGs (header line + raw bytes) to avoid
  base64 bloat.

### Message types

Cloud → hub (requests):

| type | payload | purpose |
|---|---|---|
| `job.run` | `{ run_id }` | tell the hub to execute this run; hub then drives it via `executor.Service`, pulling data through repo-RPC |
| `job.cancel` | `{ run_id }` | cooperative cancel (maps to `RequestCancel`) |
| `control.screen` | `{ device_id }` | one live PNG frame (→ binary reply) |
| `control.input` | `{ device_id, event }` | inject one `executor.InputEvent` |
| `repo.reply` | `{ result }` | response to a hub-initiated repo-RPC |

Hub → cloud (requests/events):

| type | payload | purpose |
|---|---|---|
| `hello` | inventory | on connect |
| `device.inventory` | `[]LocalDevice` | periodic/triggered; feeds device status+heartbeat+`hub_id` in cloud |
| `repo.call` | `{ method, args }` | proxied repository call (see below) |
| `job.done` | `{ run_id, status }` | run finished (cloud already has results via repo-RPC) |
| `pong` | — | heartbeat |

### Repo-RPC (the mechanism that lets `executor.Service` run unchanged)

The hub holds a `repository.*` implementation whose every method serializes
`{method, args}` into a `repo.call`, awaits `repo.reply`, and deserializes. The cloud
side has a **hub session** that receives `repo.call`, dispatches to the *real*
Postgres repos — **scoped to that hub** (a hub may only touch runs/devices assigned
to it) — and returns the result.

Hot-path methods actually used by a run (the Stage-1 surface, from
`interfaces.go` filtered to what `executor.Service` calls):

- `RunRepo`: `ClaimNextPending`* , `GetByID`, `Complete`, `Cancel`, `IsCancelRequested`,
  `ListCancelRequested`, `CreateResult`, `SaveStepResults`, `AppendAILog`, `GetRunSummary`
- `BuildRepo`: `GetByID`, `List`
- `TestRepo`: `GetByID`, `ReplaceSteps`, `Create`, `ListExtractedTests`
- `StaticTestRepo`: `GetByID`, `ListModules`
- `DeviceRepo`: `GetByID`, `List`, `UpdateStatus`, `UpdateHeartbeat`
- `EnvironmentRepo`: `GetByID`
- `MarketplaceRepo`: `ListPersonas`, `ListTestHooks`
- `NotificationRepo`: `Create` (via `RecordRun*Event`)
- `ProjectRepo`: `GetByID`
- `MemoryRepo`: authoring/discovery subset (Stage 3)

\* `ClaimNextPending` is **not** hub-driven. The **cloud scheduler** claims pending
runs and routes each to the hub owning the run's devices (`device.hub_id`), then
sends `job.run`. The hub never claims arbitrary runs.

## Data model

New migration:

```sql
CREATE TABLE hubs (
  id           text PRIMARY KEY,
  name         text NOT NULL,
  token_hash   text NOT NULL,
  status       text NOT NULL DEFAULT 'offline',   -- online | offline
  version      text,
  capabilities jsonb NOT NULL DEFAULT '{}',
  last_seen_at timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE devices ADD COLUMN hub_id text REFERENCES hubs(id);
```

- A device belongs to the hub that reports it (set on `device.inventory`).
- **A run targets exactly one device** (product rule, decided 2026-07-08) → exactly
  one hub. The scheduler routes a run to `device.hub_id` with no splitting or
  cross-hub aggregation. API/UI validate `len(DeviceIDs) == 1` at run creation; the
  existing device loop in `service.go:307` simply runs length 1.
- UI shows hub online/offline + its devices (reuses the existing devices page).

## Builds & artifacts plane

Today builds are `file://` local paths (`service.go:729`); artifacts are read off
local disk by the run page. In the split world (decided 2026-07-08):

- **Builds live on the cloud backend.** The build file is stored server-side (backend
  + remote **S3**). When a run is dispatched, the backend hands the hub a **link**
  (presigned S3 URL, or `GET /api/hub/builds/{id}` that redirects to one); the hub
  downloads it and installs. The existing per-device sha marker logic
  (`service.go:750`) keeps working — it just gates a download+install instead of a
  local-file install.
- **Artifacts:** the hub uploads screenshots/video to `POST /api/hub/artifacts` (or a
  presigned PUT) and references the returned URL in results. The run page reads from
  cloud storage.

This is the largest hidden cost of the split and is **in scope for MVP** (full runs).

## iOS

`RemoteControl` is Android-only today (`remote_control.go:20`). The hub *is* a Mac,
so iOS runs (WDA/go-ios, xcrun) are feasible there later; iOS screen/input is a
follow-up, not MVP.

## Delivery stages

- **Stage 0 — protocol skeleton.** `internal/hub` shared package: envelope, corr-id
  mux, WS client (hub) + WS session (cloud). `cmd/hub` binary that connects, sends
  `hello`, streams `device.inventory`. Cloud `GET /api/hub/connect` handler + `hubs`
  table + token auth. **Outcome:** office Mac's devices appear online in the cloud UI.
- **Stage 1 — repo-RPC + remote control.** Repo-RPC proxy (hub) + hub-session
  dispatcher (cloud) for the `DeviceRepo` + control surface. Route `GET
  /devices/{id}/screen` & `POST /devices/{id}/input` through the hub owning the
  device. **Outcome:** interactive control of a remote device from the browser.
- **Stage 2 — full run dispatch.** Scheduler claims pending runs, routes by
  `hub_id`, sends `job.run`; hub runs `executor.Service` via repo-RPC; results land
  in cloud Postgres. Builds plane (S3 + hub pull) + artifact upload. Cancellation
  end-to-end. **Outcome:** a run executes on the office Mac, visible in the cloud UI.
- **Stage 3 — authoring/discovery + multi-hub polish.** opencode authoring, smoke
  crawl, hub health/requeue on disconnect, N-hub routing UI.

## Decisions & open questions

Decided 2026-07-08:

- **No colocated/localhost mode.** The current single-machine setup (`cmd/worker`
  running `executor.Service` next to the devices) is **dropped**. Execution *always*
  goes cloud-scheduler → remote hub. `cmd/worker` is superseded by the cloud
  scheduler + `cmd/hub`; dev/test always runs against a connected hub. We host no
  apps ourselves.
- **Builds live in the cloud (backend + remote S3);** the hub downloads via a link.
  See the builds plane above.
- **A run targets exactly one device**, so every run maps to exactly one hub — no
  multi-hub run to split or aggregate. Enforced by `len(DeviceIDs) == 1` validation
  at run creation.

Open: none blocking. (Follow-ups: iOS screen/input via WDA; hub health/requeue on
disconnect — both Stage 3.)
