# machinery-go
The Go runtime of the machinery plugin contract: a library that serves a Go plugin, and a static example binary.

Implements contract v1alpha1 of [practice-library/machinery](https://github.com/practice-library/machinery): the service `machinery.plugin.v1alpha1.PluginService` (`Describe`, `Plan`, `RunUnit`, `Merge`, `Run`) as Connect unary JSON. Built for release `contract/v1alpha1.0` (commit `65b3d2ce`, asset `machinery-contract-v1alpha1.0.tar`, sha256 `f9c513a94e735ec850f1ba09d8a13f919342b34c2e4aad1c8eaa4a774cffad4f`), with the generated module `github.com/practice-library/machinery/gen/go` at tag `gen/go/v0.1.0`, pinned by `go.sum`. Contract v0 (one request on stdin, exit codes) is not served. Bindings served: 127.0.0.1 and stdio (below); `Describe` does not declare them (`docs/lld.md` §2). The design is in `docs/hld.md` and `docs/lld.md`.

## The runtime

`runtime` serves any value that has some of the methods `Describe`, `Plan`, `RunUnit`, `Merge` and `Run` (`runtime/plugin.go`). It answers `unimplemented` for any method the value lacks. For each call, the runtime does the following, so the plugin doesn't have to:
- **Decoding:** it decodes the request strictly: UTF-8, one JSON value, no key given twice, no NaN, no lone surrogate, unknown fields refused. Proto's leniency stays (proto field names, integers written as strings, null read as left out). It also checks the rules proto can't state (caps bounds, input, network, host pattern, replay). A broken one is `invalid_argument`.
- **Numbers:** they are doubles. An integral number up to 2^53 reaches the plugin as `int64`, and every other number as `float64`. On the way out, every integral number is written as an integer (`3`, never `3.0`; `1e20` as `100000000000000000000`), and every other one as Python's `repr` writes it, so this runtime and machinery-python write the same bytes for every number.
- **Deadline:** the deadline is `caps.timeSeconds`, or the caller's timeout if that is sooner. The plugin's `ctx` ends at the deadline, and the runtime answers `deadline_exceeded` then, whether or not the plugin returned.
- **The answer:** it writes every envelope field, and an answer over `caps.outputBytes` is `resource_exhausted`. A `Refusal` becomes its code. Any other error, panic or out-of-bounds answer becomes `internal`.
- **Errors:** every error the runtime answers carries a `machinery.plugin.v1alpha1.Failure` detail (unpadded base64, with no debug copy). Its message is the warnings joined by `; `. The error body fits `outputBytes`.

It has two front ends over one core:
- **127.0.0.1 (the reference binding).** It binds `127.0.0.1:<port>`, never another interface, and prints `http://127.0.0.1:<port>` as the first line of stdout.
- **`-stdio`, machinery's stdio binding.** It handles one call per process. Stdin is one JSON line `{"path": ..., "timeoutMs": N}` followed by the request body. Stdout is one line `{"status": <code or "ok">}` followed by the response or Connect error body. The exit status is 0 on ok and 1 on any error.

The five paths are served with `net/http`, not connect-go's handler. connect-go supplies the codes, the error and the detail; why it's used only for those is stated in `runtime/serve.go`.

`examples/minimal` is the Go port of machinery-python's `examples/minimal.py`. It speaks the fact-refresh job's schemas but refreshes nothing, and each call shows one outcome:

```sh
go run ./examples/minimal            # prints its base URL first
go run ./examples/minimal -stdio     # one call on stdin and stdout
```

## Checks

`.github/workflows/checks.yml` runs `unit` and `build` on ubuntu-24.04 and ubuntu-24.04-arm:
- **`unit`:** `go mod download` and `go mod verify`, `go vet`, `go test -race`, govulncheck (pinned by `tools/go.sum`, a module of its own so its dependencies never reach the runtime's), and `go run ./tests/guards`, which removes each guard in turn; the tests must then fail.
- **`build`:** two `CGO_ENABLED=0 -trimpath` builds of the example, with their sha256 values compared.

Conformance against machinery's runtime fixtures is added when machinery's contract tag carries `conformance/runtime/`.

The workflow also calls the organisation's reusable workflows from practice-library/.github at release v2.1.0, pinned by full commit: `leak-scan` and the four review guards (`denylist`, `removed-paths`, `release-note`, `schema-loosening`), ending in the `gate` the ruleset requires (`gate / gate`). `.github/required-checks.json` lists exactly the gate's needs. `.github/denylist.json` is the organisation's list, copied from practice-library/.github at that release. Every path is owned by the maintainers (`.github/CODEOWNERS`). `.github/workflows/automerge.yml` merges only allowlisted paths (`.github/automerge-paths.json`; none yet).
