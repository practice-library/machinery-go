# machinery-go: low-level design

Status: **signed by the founder on 2026-11-02** ("approved all start going through your recommendations"), with the rulings recorded in `docs/hld.md` §7. Building waits for machinery's stdio binding and its contract tag (convergence plan, phase 3).

Pins used below, all read on 2026-11-02:
- machinery `gen/go/v0.1.0` = commit `d60246522d7390c850d2e821d7bfb08a959ff2c2`. machinery-python pins the same commit.
- `gen/go/go.mod`: `go 1.26.0`, `connectrpc.com/connect/v2 v2.0.2`, `google.golang.org/protobuf v1.36.12`.
- machinery `main` at `65b3d2c`: `conformance/connect/` holds 66 cases (describe 17, plan 6, run-unit-safety 15, run-unit-design-review 12, merge 16).

## 1. Layout

```
go.mod                      module github.com/practice-library/machinery-go; go 1.26.0; toolchain go1.26.x
go.sum                      pins machinery/gen/go@v0.1.0, connect v2.0.2, protobuf v1.36.12
runtime/
  serve.go                  Serve(plugin, opts): listener (127.0.0.1:0 or a unix socket), prints the URL first
  plugin.go                 the Plugin interfaces: Describer, Planner, UnitRunner, Merger, Runner; Answer; Refusal
  codec.go                  strict JSON decode; encode with every field, integers as integers
  rules.go                  the rules proto cannot state (caps bounds, input, network, host pattern, replay)
  failure.go                Connect error + Failure detail; error body fitted to outputBytes
  deadline.go               min(caps.timeSeconds, Connect-Timeout-Ms)
  *_test.go                 unit tests, one per rule and per Python bug already found (§4)
examples/minimal/main.go    the minimal plugin: fact-refresh's schemas, refreshes nothing, one outcome per call
conformance/fixtures/       the runtime's fixtures (see §4 and open point 1)
tests/guards.go             each guard row removes one check; its test must then fail
.github/workflows/checks.yml
README.md                   states contract v1alpha1 at machinery d6024652; v0 not served
```

`runtime` is a library so a real Go plugin (the quote matcher) imports it. The example is the only `main`.

## 2. Contracts and interfaces

**The plugin interface.** One small interface per call. The runtime answers `unimplemented` for any the plugin lacks, as machinery-python does:

```go
type Describer  interface { Describe(ctx context.Context, caps Caps) (Answer, error) }
type Planner    interface { Plan(ctx context.Context, input map[string]any, caps Caps) (Answer, error) }
type UnitRunner interface { RunUnit(ctx context.Context, input map[string]any, net Network, caps Caps) (Answer, error) }
type Merger     interface { Merge(ctx context.Context, input map[string]any, caps Caps) (Answer, error) }
type Runner     interface { Run(ctx context.Context, input map[string]any, net Network, caps Caps) (Answer, error) }

type Answer  struct { Data any; NotRead []NotRead; Warnings []string }
type Refusal struct { Code connect.Code; Warnings []string } // returned as error
```

- `input` arrives as plain Go values with integral numbers as `int64`, not the `float64` a `Struct` holds.
- `ctx` carries the deadline. Unlike Python's thread (machinery-python#17), a Go plugin can be cancelled. The runtime still answers `deadline_exceeded` on time whether or not the plugin honours `ctx`.
- Any error that is not a `Refusal`, or an `Answer` that breaks the envelope bounds (warnings 1 to 500 characters, notRead fields 1 to 300), is `internal` with a Failure detail, never a success.

**The wire.** Exactly the v1alpha1 contract (`contract/README.md` of machinery, "Contract v1alpha1"):

| Outcome | Connect code | HTTP | Failure detail |
|---|---|---|---|
| success | n/a | 200, the envelope | n/a |
| failed on a valid request | `failed_precondition` | 400 | yes |
| answer over `outputBytes` | `resource_exhausted` | 429 | yes |
| invalid request, broken rule, bad JSON | `invalid_argument` | 400 | yes |
| package not served, unknown call | `unimplemented` | 404 | path not served: no |
| temporary: retry later | `unavailable` | 503 | yes (callers retry only with it) |
| over the deadline | `deadline_exceeded` | 504 | yes |
| plugin bug | `internal` | 500 | yes |

**Front ends.** `127.0.0.1` TCP is the default, never `0.0.0.0` or `::`; a test asserts the bound address. A second front end waits for the transport ruling (HLD §7 A; machinery `docs/hld.md` §6.1, family recommendation stdio): `-stdio` (one body in, one body out, same core), or `-unix <path>` (a different listener) if the founder rules for the socket. The core (`codec.go`, `rules.go`, `failure.go`, `deadline.go`) takes bytes and returns bytes plus a code, so either front end is a thin wrapper.

## 3. Where the generated code is used and where it is not

- **Used:** the message types (`machinerypluginv1alpha1`) to build the binary `Failure` detail and to check field lists; `pluginv1alpha1connect` procedure names for routing.
- **Not trusted alone:** protojson's decoding. The runtime reads the body, runs the strict decoder (`codec.go`), then unmarshals. The raw-body cases of `conformance/connect/describe.json` (not JSON, a key given twice, NaN, a lone surrogate) are the test; the first PR records which of them protojson alone would pass.
- **Encoding:** written by the runtime, not protojson's default, so every envelope field is present (`notRead: []`, `warnings: []`, `data: null`) and integral doubles print as integers. `gen/README.md` says Go already writes `3`, not `3.0`; a fixture still checks it.
- If connect-go's handler cannot be made to refuse what the contract refuses, the runtime serves the five paths with `net/http` directly and keeps connect-go for its error and detail encoding only. The first PR states which it did and why.

## 4. Tests and conformance

- **Unit tests,** one per rule in `rules.go` and per codec edge. They include a test for each bug machinery-python already hit, so Go does not rediscover them:
  - a body that does not decode still gets an error that fits the smallest legal `outputBytes` (python#5, #6);
  - a nested number that overflows to infinity is `invalid_argument` with its Failure (python#7, #8);
  - the largest integer literal that rounds to a finite double (python#9, #10);
  - status checked before decoding the Failure (python#13 to #16);
  - the end-to-end and deadline tests machinery-python#17 still lacks: a plugin that ignores `ctx` gets `deadline_exceeded` on time.
- **Guards:** `tests/guards.go` removes each check in turn; its test must fail. Same pattern as both other repositories.
- **Conformance:** CI serves `examples/minimal` and runs machinery's `conformance/connect_run.py` at the pinned commit, with `--validator jsonschema` (sourcemeta jsonschema 17.2.1, pinned by sha256), against `conformance/fixtures/`.

**What "passing machinery's conformance fixtures" can mean today.** `conformance/connect/` tests the fact-refresh *job*: quotes found on pages, robots.txt, merges. A runtime's minimal example cannot pass those without being a fact-refresh plugin. machinery-python therefore keeps its own `conformance/fixtures/` (56 cases) for what the runtime decides, and runs machinery's runner on them. Two paths:

1. **Proposed:** the runtime fixtures move to machinery (for example `conformance/runtime/`), so Python and Go run one set instead of two copies that drift. machinery-go pins them. Until then, machinery-go uses machinery-python's set, pinned by commit and sha256, and says so.
2. The job fixtures (`conformance/connect/`) are passed by a Go plugin later: the quote matcher would carry `run-unit-design-review.json`.

Open point 1 is for practice-machinery-92 (machinery's controller).

## 5. CI

`.github/workflows/checks.yml`, every job on **ubuntu-24.04 and ubuntu-24.04-arm** as a matrix:
- `unit`: `go vet`, `go test -race ./...`, `go run ./tests/guards.go`.
- `conformance`: build the example, serve it, run machinery's runner as above.
- `build`: `CGO_ENABLED=0 go build -trimpath` twice, compare sha256, print it.
- `govulncheck` at a pinned version over the module.
- The organisation's `leak-scan` and four review guards, and `gate / gate`, as today. The pin moves from .github v2.1.0 to whatever release is current (machinery is on v2.4.0; .github#96 is v3.0.0) in its own PR, not mixed with the runtime.
- Go is installed with `actions/setup-go` pinned by full commit, reading the `toolchain` line, with module download from the proxy checked against `go.sum`.

**Timing.** The PR body gives the wall-clock of the `checks` run against machinery-python's: its last `main` run took 2.7 min (13:12:33 to 13:15:15 UTC on 2026-11-01, x64 only). The comparison is like for like only on x64; arm64 is reported on its own.

## 6. Release steps

1. The first PR (#1 rewritten) lands the runtime, the example, the fixtures pin, CI, and corrects the README (it says "Implements contract v0" today).
2. At a commit on `main`, a maintainer tags `vX.Y.Z`, then attaches `minimal-linux-amd64` and `minimal-linux-arm64` with their sha256 to the release. Anyone can rebuild them byte for byte at the tag.
3. A consumer pins the module by `go.sum`, or a binary by its sha256 in a registry row it owns.

Nothing here needs a credential or a package registry account.

## 7. Size and order

One PR in flight at a time. The first PR is near the size guard's ~600 non-fixture lines (machinery-python#4 is the comparison), so if it goes over, it splits: (a) codec, rules and failure with unit tests; (b) serve, deadline, example, conformance job.

## 8. Dry run: the worked example (paper only)

The same example as HLD §8: slice 1 of the vendor graph under `vendors/stablyai/` (graphs#68, held by graphs#91). What each step would exercise in this repository's code, if it were built:

1. **graphs holds the facts.** No code here reads graphs. `go.mod` gains no graphs dependency, and a test (`go list -deps`) keeps it that way.
2. **fact-refresh re-reads the pinned tag.** Still Python. The only thing this repository takes from that step is machinery's runtime fixtures at a pinned commit and sha256 (§4 path 1).
3. **Served as a plugin.** A conformance run of `examples/minimal` over each front end:
   - TCP: `connect_run.py --url http://127.0.0.1:<port>`, as today;
   - stdio, if ruled: a stdio runner from machinery, the same fixtures, the same expectations except that the deadline is the caller's kill, as in contract v0;
   - unix socket, if ruled: `connect_run.py` with a socket path (a small runner change in machinery).
   The design-review cases (12, from graphs quotes) become the quote matcher's fixtures when that plugin exists; they are not this runtime's.
4. **Guard input for the consumer.** The consumer's bridge tests its client against a recorded exchange. The proposed artefact from here: a `testdata/` directory of request and response bodies the Go runtime actually produced for each fixture, published with each release and pinned by sha256, so the consumer pins bytes from a real runtime instead of hand-written ones.
5. **Decisions and sign-offs.** Not code. The first PR body names the founder's signatures on this HLD and LLD and the transport ruling it was built on.

Missing for the example, in order: the transport ruling; the runtime fixtures moved into machinery; graphs#91 then #68; machinery#49 and #47.
