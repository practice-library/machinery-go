# machinery-go
Go implementation of the machinery plugin contract: pluggable, static binary.

Built for contract v1alpha1 of [practice-library/machinery](https://github.com/practice-library/machinery), release `contract/v1alpha1.0` (commit `65b3d2ce`, asset `machinery-contract-v1alpha1.0.tar`, sha256 `f9c513a94e735ec850f1ba09d8a13f919342b34c2e4aad1c8eaa4a774cffad4f`), with the generated module at `gen/go/v0.1.0`, pinned by `go.sum`. Contract v0 is not served. The runtime lands in two parts: `runtime/` holds the strict codec, the request rules and the Failure error; the 127.0.0.1 and stdio front ends, the deadline and an example follow. The design is in `docs/hld.md` and `docs/lld.md`.

## Checks

`.github/workflows/checks.yml` calls the organisation's reusable workflows from practice-library/.github at release v2.1.0, pinned by full commit: `leak-scan` and the four review guards (`denylist`, `removed-paths`, `release-note`, `schema-loosening`), ending in the `gate` the ruleset requires (`gate / gate`). `.github/required-checks.json` lists exactly the gate's needs. `.github/denylist.json` is the organisation's list, copied from practice-library/.github at that release. Every path is owned by the maintainers (`.github/CODEOWNERS`). `.github/workflows/automerge.yml` merges only allowlisted paths (`.github/automerge-paths.json`; none yet).
