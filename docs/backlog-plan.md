# Backlog plan: practice-library/machinery-go

Status: **signed by the founder on 2026-11-02** ("approved all start going through your recommendations"), with the rulings recorded in `docs/hld.md` §7. Building waits for machinery's stdio binding and its contract tag (convergence plan, phase 3).

Open issues at `main` 03045fd: 2. Open pull requests: 0.

| Issue | Title today | Proposal | Reason |
|---|---|---|---|
| #1 | Implement the machinery contract in go (waits for contract v0) | **Rewrite** | Its blocker is gone: contract v0 is released (machinery `v0.1.0`), v1alpha1 is in machinery's `proto/`, and the generated Go module is tagged `gen/go/v0.1.0`. The body still says "Not started: waits for the contract", and its "first PR" (CODEOWNERS, gate caller, README) already landed as #2. Rewrite it as plan #17 PR 6: the thin v1alpha1 runtime, per `docs/hld.md` and `docs/lld.md`. Move the quote matcher out into its own issue (below), so #1 is one PR. |
| #3 | Add discover and schema-check with the first check.json | **Keep** | Still true and still small. It is triggered by the first `check.json`, which this repository has no plan to add (the runtime has no graph data). Keep it open as a tracked condition, not as work. machinery-python has the same issue (#2); the two could be closed together if the founder rules that runtime repositories never hold a `check.json`. |

## Proposed new issues (not opened; listed for the sign-off)

| Proposed | Why | Depends on |
|---|---|---|
| Go runtime follow-ups to machinery#31 item 2: a `toolchain` line in the go.mod of the runtime | machinery#31 says this belongs to "plan #17 PR 6", which is this repository's #1. It becomes part of #1's PR, not its own issue. | #1 |
| The quote matcher as the first Go plugin (whole-quote match with NFKC, curly-quote and whitespace folding, table cells) | It is in #1's body today, but it is a plugin, not the runtime, and it needs a decision first: which job it serves and which fixtures it passes. machinery's design-review cases (`conformance/cases/run-unit-design-review.json`) are its fixtures. The Rust-only-on-a-30%-gain rule needs it as the bake-off subject. | #1, and machinery#17 open point A (where plugins live) |
| A Go twin of `quoteOneLine`, checked against `conformance/quote-one-line.json` | `lib/js/README.md` of machinery says "a Python or Go twin, when one is written, is checked against the same table". The private consumer CLI is Go and echoes refused input, so it is the likely consumer. Proposal only; nothing asks for it yet. | none |

## What this plan does not decide

- Whether Go is the right home for hot paths at all. That is the language study machinery leads (`docs/languages.md` in machinery). This repository sends it the Go runtime's view.
- Whether this repository also serves contract v0 (stdin and stdout). The HLD proposes not to, as machinery-python does not; see `docs/hld.md` §3.
