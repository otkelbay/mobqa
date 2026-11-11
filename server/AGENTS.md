# Backend rules

These apply to every change in this module, by people and by agents. `make check` enforces what a
linter can; review checks the rest against this file.

## Layers

```
transport (http, hubws, sse)  ──▶  usecase  ◀──  store (postgres, blob), device, runner, llm
                                     ▲
                                    app  (wires everything, holds no logic)
```

- **transport** decodes a request into its own DTO, validates it, maps it field by field to a usecase
  input, calls exactly one usecase method and maps the result or error back. Only transport types carry
  `json` tags. It never touches storage.
- **usecase** holds all business rules. Each package declares the small interfaces it consumes next to
  the code that uses them and owns its input and result types; every input has `Validate() error`. It
  imports no transport, storage, driver or `net/http`.
- **store**, **device**, **runner**, **llm** implement usecase interfaces. They keep their own row and
  wire types internally and map to usecase types at their edge. All SQL lives in `store/postgres`.
- **app** builds every component explicitly and owns its lifecycle. No DI frameworks, no globals, no
  `init()`.
- `fault` is the shared vocabulary of error kinds. Usecases wrap a kind with context
  (`fmt.Errorf("project %s: %w", id, fault.ErrNotFound)`); transport maps each kind to one status and
  a fixed message. Nothing user-supplied or SQL reaches a response or a log.

`depguard` in `.golangci.yml` enforces the import directions.

## Limits

- Functions do one thing: aim for 40 lines, more than 60 needs a reason.
- At most four parameters including `ctx`; more inputs go into a named struct. Optional dependencies of
  a constructor are `Option` values.
- Nesting at most three levels. Handle errors and special cases first and return early. No `else` after
  a `return`.
- Lines at most 120 characters. Imports in three groups: standard library, third party, this module.
- No magic values: name a constant and say why it has that value; operator-tunable values go to config.
- Always use field names in struct literals.

## Comments

The code says what it does. A comment only says what the code cannot: why a value or an order was
chosen, an invariant, a contract the caller must know, what a package deliberately does not do, who owns
a goroutine and how it stops. Every package has a package comment. No commented-out code, no narration,
no section labels.

## Naming

Full words; only the domain's own abbreviations (ID, URL, HTTP, API, JSON, SQL, iOS, APK, IPA, OTP,
LLM, RAG, SDP, ICE). Packages are one lowercase singular word. Do not repeat the package name in its
identifiers (`project.Service`, not `project.ProjectService`). Tables are plural, structs singular, JSON
fields snake_case.

## Errors, context, time

- Check every error; wrap it with what the code was trying to do. Compare with `errors.Is`/`errors.As`.
- Every blocking call takes a `context.Context`. Store calls are bounded by `operationTimeout`. Cleanup
  that must outlive a cancelled request uses `context.WithoutCancel` and says so.
- Time comes from the injected `clock.Clock`, in UTC at microsecond precision. SQL never reads the
  database clock; timestamps are bound parameters. Tests move a fake clock instead of sleeping.

## Concurrency

Whoever starts a goroutine stops it, and its owner is named in a comment. Long-lived components expose
`Run(ctx) error`, track their goroutines on one `sync.WaitGroup` and return only after all of them have
exited. Background work runs through `internal/worker` jobs, never through ad-hoc goroutines in
handlers.

## Logging

One injected `*slog.Logger`; children via `With("component", ...)`. Static messages, facts as
attributes from the fixed vocabulary: `component, request_id, user_id, project_id, test_id, run_id,
job_id, job_kind, device_id, hub_id, attempt, duration_ms, code, method, route, status, address, error`.
A new key is a vocabulary change. `error` carries internal errors only, never user input. Log once, in the
layer that decides the outcome.

## Tests

- Standard `testing` only: no assertion or mocking libraries.
- Usecases are tested through their public methods. Fakes stand in only at boundaries: storage,
  devices, runners, LLMs, the clock and randomness. Fakes are configured with data, not functions.
- Table-driven tests describe *what* in the table and *how* in a flat loop; a case that needs its own
  setup becomes its own test function.
- Expected values are literals or fixtures, never recomputed the way the code computes them.
- Store tests are `_integration_test.go` files with the `integration` build tag and use `dbtest.New`,
  which gives each test its own migrated schema. End-to-end tests carry the `e2e` tag and drive the
  wired API over HTTP.
- A bug fix starts with a test that fails without it.

## Schema

Forward-only migrations in `internal/store/postgres/migrations/NNNN_slug.sql`, applied by the embedded
runner at startup. UUIDv7 keys, `timestamptz` columns.

## Done means

`make check` is green (gofmt, vet, staticcheck, golangci-lint, `go test -race`, integration and e2e when
`MOBQA_TEST_DATABASE_URL` is set), and the diff has been read once against this file.
