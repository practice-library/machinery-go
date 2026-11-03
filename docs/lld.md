# machinery-go: low-level design

Status: **signed by the founder on 2026-11-02** ("approved all start going through your recommendations"), with the rulings recorded in `docs/hld.md` §7. Building waits for machinery's stdio binding and its contract tag (convergence plan, phase 3).

Pins used below, all read on 2026-11-02:
- machinery `gen/go/v0.1.0` = commit `d60246522d7390c850d2e821d7bfb08a959ff2c2`. machinery-python pins the same commit. The Go module stays on it.
- machinery `contract/v1alpha1.0` (planned; not yet published on 2026-11-03, so these values are machinery's stated ones, to be checked against the release when it exists) = commit `65b3d2ce34c67c7887a2564b3f694239aebb642d`, asset `machinery-contract-v1alpha1.0.tar`, sha256 `f9c513a94e735ec850f1ba09d8a13f919342b34c2e4aad1c8eaa4a774cffad4f`. Its `proto/` and `gen/` are byte-identical to `gen/go/v0.1.0`.
- `gen/go/go.mod`: `go 1.26.0`, `connectrpc.com/connect/v2 v2.0.2`, `google.golang.org/protobuf v1.36.12`.
- machinery `main` at `65b3d2c`: `conformance/connect/` holds 66 cases (describe 17, plan 6, run-unit-safety 15, run-unit-design-review 12, merge 16).

## 1. Layout

```
go.mod                      module github.com/practice-library/machinery-go; go 1.26.0; toolchain go1.26.x
go.sum                      pins machinery/gen/go@v0.1.0, connect v2.0.2, protobuf v1.36.12
runtime/
  serve.go                  Serve(plugin, opts): 127.0.0.1:0 listener (prints the URL first), or -stdio: one call on stdin and stdout
  plugin.go                 the Plugin interfaces: Describer, Planner, UnitRunner, Merger, Runner; Answer; Refusal
  codec.go                  strict JSON decode; encode with every field, integers as integers
  rules.go                  the rules proto cannot state (caps bounds, input, network, host pattern, replay)
  failure.go                Connect error + Failure detail; error body fitted to outputBytes
  deadline.go               min(caps.timeSeconds, Connect-Timeout-Ms)
  *_test.go                 unit tests, one per rule and per Python bug already found (§4)
examples/minimal/main.go    the minimal plugin: fact-refresh's schemas, refreshes nothing, one outcome per call
CONFORMANCE.json            the contract pin (tag, commit, asset, sha256); CI fetches conformance/runtime/ from that asset (§4)
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

- **Numbers are the contract's doubles.** `input` arrives as plain Go values in which every JSON number is a finite `float64`, as `Struct` holds it: exact up to 2^53, and a literal beyond that rounds to the nearest double as proto's JSON does. The rule, the same one machinery-python's `codec.py` applies (`MAX_EXACT = 2 ** 53`), so both runtimes agree on every value the contract fixes:
  - *Decoding:* a number literal that does not round to a finite double (more than about 309 digits, or `1e400`) is `invalid_argument` with its Failure (machinery-python#7, #8). `NaN` and `Infinity` are refused by the strict pre-decode.
  - *To the plugin:* a value that is integral with `|v| <= 2^53` is handed over as `int64`, which always holds it exactly; every other value stays `float64`. So `int64` is a view of some doubles, never a wider type: a plugin never receives an integer the contract could not carry.
  - *Encoding:* every integral finite double is written in integer form, whatever its size, as `plugin.proto` and `contract/README.md` require ("written by every runtime as integers when they are"): digits only, no exponent, no `.0` (`strconv.FormatFloat(v, 'f', -1, 64)`; `-0` is written `0`). So 2^53 + 2 is `9007199254740994` and 1e20 is `100000000000000000000`. A non-integral double is written in shortest round-trip form. A plugin's `int64` outside ±2^53 is converted to the nearest double first, as the wire must carry it.
  - *Measured gap in the other runtime (2026-11-02):* machinery-python's `codec.py` turns integral doubles into integers only up to 2^53 (`MAX_EXACT`), so above it Python writes `9007199254740994.0` and `1e+20`. That breaks the same rule. It is raised with machinery as a shared runtime fixture (the edges below) that both runtimes must pass, not settled here.
  - The edges are fixtures, not prose: 2^53, 2^53 + 1 written as a literal, the largest finite literal, the first one that overflows, and `-0` (written `0`). They belong in machinery's runtime fixtures, so every runtime meets the same values.
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

**Front ends.** `127.0.0.1` TCP is the reference binding, never `0.0.0.0` or `::`; a test asserts the bound address. The second is `-stdio` (D1, signed), framed exactly as machinery `docs/lld.md` §2.4 defines it: one process per call; stdin is one JSON line `{"path": ..., "timeoutMs": N}` then the Connect-JSON request body; stdout is one line `{"status": <code or "ok">}` then the response or Connect error body; exit 0 on ok and 1 on any error, the Connect code being the authority; the host kills at `timeoutMs` plus a grace period. Both front ends call the same core (`codec.go`, `rules.go`, `failure.go`, `deadline.go`), which takes bytes and returns bytes plus a code. `Describe` does not declare bindings: `describe.schema.json` has `additionalProperties: false`, and an answer gains a field only when the request asks for it (machinery `docs/lld.md` §2.4). The bindings a release serves are named in `README.md` and the release notes instead. No unix socket.

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
- **Conformance:** CI serves `examples/minimal` and runs machinery's `conformance/connect_run.py` at the pinned commit, with `--validator jsonschema` (sourcemeta jsonschema 17.2.1, pinned by sha256), against `conformance/runtime/` from the pinned contract release asset (path 1 below). Nothing else is selected: the job fixtures in `conformance/connect/` test fact-refresh, not a runtime.

**What "passing machinery's conformance fixtures" can mean today.** `conformance/connect/` tests the fact-refresh *job*: quotes found on pages, robots.txt, merges. A runtime's minimal example cannot pass those without being a fact-refresh plugin. machinery-python therefore keeps its own `conformance/fixtures/` (56 cases) for what the runtime decides, and runs machinery's runner on them. Two paths:

1. **The runtime fixtures are machinery's** (`conformance/runtime/`, moved from machinery-python's 56, arriving in `contract/v1alpha1.1`). machinery-go pins them by contract tag plus the release asset's sha256, never by a bare commit, and checks them as machinery `docs/lld.md` §6.6 does: download the asset and `SHA256SUMS`, compare the sha256 with the pin, check that the tag names the pinned commit, unpack only `conformance/`. No interim copy of machinery-python's fixtures is taken; building waits for that tag.
2. The job fixtures (`conformance/connect/`) are passed by a Go plugin later: the quote matcher would carry `run-unit-design-review.json`.

The move is machinery's to make (its D8 and its runtime-fixtures item).

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

Every public write is the practice-library bot's, through the organisation's app wrapper. No person's account tags, pushes or uploads.

1. **The first runtime PR** (#1 rewritten) lands the runtime, the example, the fixtures pin, CI, and corrects the README (it says "Implements contract v0" today). It is built against machinery's first contract tag with the stdio binding, and names it.
2. **The release PR**, authored by the bot, sets `RELEASE.json` to the release's pins: the runtime version; the contract pin in machinery's four values (tag, commit, asset, asset sha256); the `gen/go` tag that contract release names and its `go.sum` lines. CI checks each pin as machinery `docs/lld.md` §6.6 does, then runs conformance on both arches over both bindings. It merges after one review pass at its exact head.
3. **The tag.** The bot pushes `vA.B.C` at the merge commit. The tag must match `RELEASE.json` at that commit; the release workflow refuses otherwise.
4. **Verify and build, read-only** (`.github/workflows/release.yml`, on the tag push, public Actions, $0). Every job has `permissions: contents: read` and publishes nothing:
   - `verify`: conformance (`conformance/runtime/` of the pinned contract asset, over 127.0.0.1 and stdio) and unit tests on ubuntu-24.04 and ubuntu-24.04-arm;
   - `build` (needs `verify`): the two builds per arch, compared byte for byte, uploaded as workflow artifacts with `SHA256SUMS` and `RELEASE.json`.
5. **Publish, by the bot only** (machinery `docs/lld.md` §6.1: no workflow token publishes anything), on the maintainer's machine through the organisation's app wrapper, and only after both jobs passed:
   - check that the release workflow's run for the tag concluded `success` at exactly the tagged commit;
   - download its artifacts, check each against `SHA256SUMS`;
   - `gh release create vA.B.C --verify-tag`, then upload `minimal-linux-amd64`, `minimal-linux-arm64`, `SHA256SUMS` and `RELEASE.json`.

   A failed run publishes nothing; the fix is a new patch version, never a moved tag. No secret is stored in the repository.
6. **A consumer** pins the module by `go.sum`, or a binary by its sha256 in a registry row it owns. Contract test data is adopted from machinery's contract release asset, not from here (HLD §5; machinery `docs/lld.md` §6.6).

No package registry account is needed. The Go module proxy fetches the public tag on first request.

## 7. Size and order

One PR in flight at a time. The first PR is near the size guard's ~600 non-fixture lines (machinery-python#4 is the comparison), so if it goes over, it splits: (a) codec, rules and failure with unit tests; (b) serve, deadline, example, conformance job.

## 8. Dry run: the worked example (paper only)

The same example as HLD §8: the vendor graph `vendors/<example>` (graphs#68 and #91). What each step would exercise in this repository's code, if it were built:

1. **graphs holds the facts.** No code here reads graphs. `go.mod` gains no graphs dependency, and a test (`go list -deps`) keeps it that way.
2. **fact-refresh re-reads the pinned tag.** Still Python. The only thing this repository takes from that step is machinery's runtime fixtures, pinned by contract tag and asset sha256 (§4 path 1).
3. **Served as a plugin.** A conformance run of `examples/minimal` over each binding:
   - the 127.0.0.1 reference: `connect_run.py --url http://127.0.0.1:<port>`, as today;
   - stdio (D1): machinery's stdio runner, shipped in the contract release that adds the binding; the same fixtures and expectations, with the frame of §2 and the deadline enforced by the host's kill at `timeoutMs`.
   The design-review cases (12, from graphs quotes) become the quote matcher's fixtures when that plugin exists; they are not this runtime's.
4. **Guard input for the consumer.** The consumer's bridge tests its client against a recorded exchange. The proposed artefact from here: a `testdata/` directory of request and response bodies the Go runtime actually produced for each fixture, published with each release and pinned by sha256, so the consumer pins bytes from a real runtime instead of hand-written ones.
5. **Decisions and sign-offs.** Not code. The first PR body names the founder's signatures on this HLD and LLD and the transport ruling it was built on.

Missing for the example, in order: machinery's stdio binding and runner, and its first contract tag; the runtime fixtures moved into machinery; graphs#91 then #68; machinery#49 and #47.
