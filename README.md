# machinery-go
Go implementation of the machinery plugin contract: pluggable, static binary.

Implements contract v0 of [practice-library/machinery](https://github.com/practice-library/machinery) at release v0.1.0 (commit `533522ec`): one JSON request on stdin, one envelope on stdout, verbs `describe`, `plan`, `run-unit` and `merge`. A plugin conforms when it passes machinery's black-box suite (`conformance/run.py`) at that commit. Nothing is implemented yet; see issue #1.

## Checks

`.github/workflows/checks.yml` calls the organisation's reusable workflows from practice-library/.github at release v2.1.0, pinned by full commit: `leak-scan` and the four review guards (`denylist`, `removed-paths`, `release-note`, `schema-loosening`), ending in the `gate` the ruleset requires (`gate / gate`). `.github/required-checks.json` lists exactly the gate's needs. `.github/denylist.json` is the organisation's list, copied from practice-library/.github at that release. Every path is owned by the maintainers (`.github/CODEOWNERS`). `.github/workflows/automerge.yml` merges only allowlisted paths (`.github/automerge-paths.json`; none yet).
