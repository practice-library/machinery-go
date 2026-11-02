# machinery-go: high-level design

Status: **signed by the founder on 2026-11-02** ("approved all start going through your recommendations"), with the rulings recorded in `docs/hld.md` §7. Building waits for machinery's stdio binding and its contract tag (convergence plan, phase 3).

## 1. Purpose

machinery-go is the Go runtime of the machinery plugin contract. It is plan practice-library/machinery#17, PR 6: "the same runtime in Go, with the generated module pinned by `go.sum`. CI runs the fixtures against a minimal Go example plugin."

It exists for two reasons, both from issue #1:
1. **The second implementation.** Python is the first (machinery-python, plan #17 PR 4). A second runtime in another language, passing the same fixtures, is what shows the contract is language-neutral rather than "Python's wire format".
2. **The home for hot paths.** The founder's rule: Go is the default for a measured hot path, and Rust only on a measured gain of 30 percent or more. A Go runtime is the precondition for measuring anything in Go.

A third reason comes from the consumer side. A private consumer CLI, written in Go with the standard library only, will call machinery plugins through a bridge it is building. A Go runtime is the reference that its hand-written client is checked against. (The consumer's repository is private; its issues are cited below as "the consumer's bridge issue" and "the consumer's contract issue", and the founder holds the numbers.)

## 2. Boundaries: what it owns and what it does not

| Owns | Does not own |
|---|---|
| A Go package that serves a plugin as `machinery.plugin.v1alpha1.PluginService` over Connect unary JSON | The contract itself: `proto/`, `contract/v1alpha1/` schemas, `conformance/` fixtures and runner (machinery) |
| The rules proto cannot state, enforced in Go (caps bounds, input presence, network presence, host pattern, replay rules), answered `invalid_argument` | The generated code (`gen/go` in machinery, tagged `gen/go/v0.1.0`); this repository only pins it |
| The strict JSON decoder (UTF-8, no duplicate keys, no NaN, no lone surrogates, unknown fields refused) | Any job's logic. fact-refresh stays Python (machinery, or machinery-python per #17 open point A) |
| The deadline, the `outputBytes` cap, integral numbers written as integers, the `Failure` detail on every error | The gateway (practice-library/.github#41, plan #17 step 7) |
| A minimal example plugin used only to prove conformance in CI | The cross-language recommendation (machinery, `docs/languages.md`, led by practice-machinery-92) |
| Static release binaries of the example, linux x64 and arm64, with sha256 | The registry row that pins a released binary; the consumer owns it (issue #1) |

Contract v0 (stdin and stdout) is **not** implemented. machinery-python does not implement it either, and plan #17 step 8 retires v0 once nothing calls it. Implementing it now would be work to delete. The README's "Implements contract v0" line is stale for this reason and is corrected in the first PR (`docs/lld.md` §6).

## 3. Consumers

| Consumer | What it needs from machinery-go | Evidence |
|---|---|---|
| machinery (the contract) | A second runtime that passes `conformance/connect/` and, so, proves the fixtures are not Python-shaped | machinery#17 §6 step 6; `conformance/README.md` "every runtime must write them alike" |
| The gateway (.github#41) | Nothing Go-specific. It speaks Connect JSON with `curl`, so any conforming runtime works | machinery#17 §3 "the gateway itself" |
| The private consumer CLI | A reference for its stdlib client. Its process may open no TCP socket, so it cannot use today's loopback HTTP. It reaches a plugin over stdio or a unix socket; which one is the open ruling in §8 | the consumer's bridge issue (PR 2) and its own HLD's transport decision |
| A future Go plugin (quote matcher) | The runtime as a library | issue #1 "first candidate"; `docs/backlog-plan.md` |
| The language study | The cost and gaps of a second runtime, measured | roll-call brief item 4 |

## 4. Design in one picture

```
caller (gateway curl / consumer client / connect_run.py)
   │  POST /machinery.plugin.v1alpha1.PluginService/<Call>
   │  Content-Type: application/json, Connect-Protocol-Version: 1, Connect-Timeout-Ms
   ▼
listener: 127.0.0.1:<port>  or  unix socket   (never another interface)
   ▼
strict JSON decode ─► rules check ─► deadline = min(caps.timeSeconds, Connect-Timeout-Ms)
   ▼
plugin method (plain Go values: map[string]any with integral numbers as int64)
   ▼
encode: every envelope field written, integers as integers, body ≤ caps.outputBytes
   ▼
success 200 envelope   |   Connect error + Failure detail (base64 binary)
```

The generated `pluginv1alpha1connect` handler is used for routing and Connect protocol handling where it is strict enough. Where its JSON codec is more lenient than the contract (likely lone surrogates and NaN; to be measured, see §7 B), the runtime decodes the body itself first. `docs/lld.md` §3 says exactly where.

## 5. How it is released

- **Library:** a Go module, `github.com/practice-library/machinery-go`, tagged `vX.Y.Z`. A Go plugin pins it through `go.sum`. Nothing is published to a registry beyond the Go module proxy, which needs no credential.
- **Binaries:** the example plugin, built with `CGO_ENABLED=0 -trimpath`, for linux/amd64 and linux/arm64, attached to the GitHub release with a sha256 per file. The same command at the same commit rebuilds the same bytes; CI checks it. This is issue #1's "static binary with no dependency install".
- Releases are cut by a maintainer from a commit on `main`, as `gen/README.md` of machinery does for the generated code.

## 6. Issues this design draws on

- machinery#17 (the plan; this is its PR 6), #31 (item 2: a `toolchain` line in go.mod when the Go runtime lands), #29 (integers as integers, which Go gets right by default per `gen/README.md`), #10 (retry on `unavailable`), #24 (proto follow-ups), #6 (contract v0 step 1).
- machinery-python#17 (end-to-end and deadline tests the Python runtime still lacks; the Go runtime should have both from its first PR), and the merged PRs #4 to #16 (the bugs the Python runtime hit: decoding errors over `outputBytes`, overflow to infinity, status before Failure). Each is a test the Go runtime must carry.
- practice-library/.github#41 (the gateway), #94 (node contract guards), #96 (v3.0.0; the gate caller pin moves with it).
- The consumer's bridge issue (stdlib only, no TCP) and its contract issue (its plug contract mapped to machinery's), both private.
- practice-library/graphs: no language or runtime node exists today (eight vendor graphs, none of a language or runtime). Proposal: none needed here; a Go or Connect vendor graph would only matter if a refresh job read their docs.

## 7. Points ruled by the founder (2026-11-02)

Rulings: **A** stdio (family decision D1, machinery `docs/hld.md` §6.1); the unix socket is not taken, and one core serves stdio and the 127.0.0.1 reference binding. **B** a strict pre-decode. **C** ubuntu-24.04 and ubuntu-24.04-arm. **D** Go 1.26 with a `toolchain` pin. The runtime fixtures move to machinery. The proposals as written before the ruling follow.


- **A. The transport for a caller that may not open TCP (family decision D1).** The options and their table are in machinery's `docs/hld.md` §6.1, which this HLD does not restate: **(A) stdio**, v1alpha1 bodies on stdin and stdout (the consumer's D1), or **(B) a unix socket** carrying today's Connect JSON (this HLD's first draft). The family recommendation is **(A) stdio**, and this HLD carries it. Loopback HTTP on 127.0.0.1 stays the reference binding either way, and the fixtures run over it.

  For this repository only: the Go runtime serves either from one core. Stdio is a second front end of about 150 lines (the deadline becomes the process's own timer, and the caller kills on overrun, as in contract v0). A socket is a different `net.Listener` of about 20 lines. So the ruling decides which front end is built first, not the design. The founder decides.
- **B. The strictness gap.** connect-go's JSON codec is protojson, which follows proto's JSON rules, not the contract's. Where they differ (unverified until the first PR measures it against the raw-body fixtures: lone surrogates, which Go's string decoding replaces with U+FFFD rather than refusing, and `NaN`, which protojson accepts as a double) the contract wins. Proposed: a strict pre-decode in the runtime, as machinery-python does in `codec.py`. The alternative, asking machinery to relax the fixtures, would make the contract follow one library.
- **C. CI arches.** Proposed: ubuntu-24.04 and ubuntu-24.04-arm for every job (the coordinator's order). The Python runtime runs x64 only, and its native wheels cover linux x86_64 and macOS arm64 only, so a timed comparison is like for like on x64 only.
- **D. Version floor.** `gen/go/go.mod` requires Go 1.26.0 (set by connect-go v2.0.2). Proposed: the same floor, with a `toolchain` line pinning the patch (machinery#31 item 2).

## 8. Dry run: the worked example (paper only)

The founder's worked example is the vendor graph of a terminal and worktree manager for coding agents, slice 1 (graphs#68: 126 nodes, read from the upstream repository at tag `v1.4.245`). Its directory is under the upstream owner's name, `vendors/stablyai/`, because the product's own name is on the organisation's denylist (graphs#91). This document uses neither the product's name nor the private consumer's, for the same reason: it is meant to land in a public repository whose gate runs that denylist.

| Step | Who | What machinery-go does | What it imports from graphs | Missing today |
|---|---|---|---|---|
| 1. The facts | graphs (#68, held by #91) | Nothing | Nothing | #91 merged, #68 moved under `vendors/stablyai/` |
| 2. Re-read the pinned tag | machinery fact-refresh (#49, #42, #47) | Nothing yet: fact-refresh is Python. The runtime fixtures live in machinery, and this runtime pins them (`docs/lld.md` §4) | Nothing | #49 (a host shared by several graphs is kept for only one) is what bites first: the upstream docs host is likely cited by more than one graph. #47 (choose the graphs) lets a dry run read only this one. Runtime fixtures in machinery |
| 3. Serve it as a plugin | machinery-python serves fact-refresh; machinery-go serves the minimal example only | Proves the same five calls answer the same codes and envelopes in Go. It does not run the job. The Go job would be the quote matcher on `RunUnit` | Nothing at runtime. A future quote matcher's fixtures are machinery's design-review cases, built from graphs quotes | The quote matcher (proposed issue in `docs/backlog-plan.md`) and its fixture set |
| 4. Facts as guard input for the consumer's commands | the private consumer CLI, through its bridge | The reference server its client is tested against, over the transport §7 A rules on | Nothing: the consumer reads the pinned graph, not this runtime | The transport ruling (§7 A); the consumer's bridge PR 2 |
| 5. Decisions and sign-offs | the founder's private record | Nothing: this repository records its decisions in issues and PR bodies only | Nothing | n/a |

What this repository does **not** need from graphs, now or for the example: any node. A runtime carries no facts. The facts the consumer guards on (worktree states `active`/`working`/`permission`/`done`/`inactive`, the `rm --force` waiver of uncommitted changes and of the terminals-stopped proof, runtime-scoped terminal handles) reach it from graphs directly.

What the example teaches this design:
- The transport ruling (§7 A, machinery `docs/hld.md` §6.1) is on its critical path; nothing else in machinery-go is.
- The denylist reaches documentation too: no file here may name the vendor or the consumer, and the first PR's body must not either.
