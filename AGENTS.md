# Development instructions

## Authority and phase scope

- Read the latest explicit phase request, [design](docs/design.md), [decisions](docs/design-decisions.md), and [progress](docs/progress.md) before editing. The latest user decision takes priority over the proposed contract. Report material conflicts; do not silently change scope.
- Work on one authorized P00-P13 phase at a time. Finishing a phase does not authorize the next. Necessary local edits and verification are authorized only within that phase's boundaries.
- Request narrowly scoped permissions for blocked operations. Do not request unrestricted access merely to avoid prompts, or install/reconfigure tools without authorization.
- GitHub Desktop is the user's normal commit/push route. Without separate explicit authorization, do not stage, commit, amend, push, tag, publish images or releases, dispatch publication workflows, or change visibility or remote settings.
- Preserve pre-existing user work. Do not reset, restore, clean, stash, rewrite history, or make unrelated edits. Do not modify another project's environment or repository.
- Only one implementation chat writes to this checkout. Independent reviewers are read-only. Focused independent reviews belong at P01, P08, and P12; do not require a full audit after every small change.

## Implementation

- Use small idiomatic Go packages and the standard library where practical. The accepted P00 baseline is Go 1.27.1; do not change it merely because a newer patch exists.
- Primary runtime modules are the proposed `go.yaml.in/yaml/v4` parser and `github.com/prometheus/client_golang` client, subject to P01 acceptance and P02 pin verification. Explain a proposed dependency expansion before adding it. Transitive dependencies still require review and checksum verification.
- Avoid speculative interfaces, generic middleware frameworks, plugin architectures, custom tracing stacks, and unrelated infrastructure. See the design's explicit exclusions.
- Keep validated configuration immutable. Synchronize rule counters/RNG state and return an immutable decision. Release locks before waiting, I/O, response writes, or logging.
- Preserve client cancellation, deadline ownership, resource cleanup, and the distinction between synthetic faults, real upstream responses, transport failures, and incomplete transfers.
- Add behavior-based tests for implemented behavior. Do not test placeholders as though features work. Early entry points must fail honestly for unimplemented operations.
- Never weaken a correct requirement, remove a failing assertion, disable race checks, or use blind retries to obtain a pass. Diagnose correctness failures before changing the implementation.
- Never expose secrets, raw credentials, sensitive request data, or proprietary code/data. Keep personal paths and local transcripts out of public documentation.

## Verification and evidence

Once the corresponding implementation exists, use the phase-appropriate checks:

```text
gofmt checks on tracked Go files
go mod verify
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...
go build -o bin/faultproxy ./cmd/faultproxy
git diff --check
```

CI checks formatting without silently rewriting files. Keep native cgo/race execution enabled. Add the phase-specific real HTTP, lifecycle, packaging, and evidence checks in the [verification matrix](docs/design.md#10-verification-by-phase). In P05, preserve the required 20 uncached repetitions of the relevant cancellation/shutdown tests, followed by full ordinary/race suites.

Distinguish permission/tool failures from correctness failures and preserve the first substantive error. An unchanged retry with scoped permission can resolve a permission restriction; a passing retry alone does not explain a correctness failure.

Report actual commands and exit results, failures, skips, limitations, and current Git status. Inspect the full changed-file list, including untracked files, and the final diff. Never stage merely to expose untracked diffs. Keep [progress](docs/progress.md) compact; do not paste bulky logs there.

Only publish feature, platform, CI, or performance claims supported by the implemented and verified revision. P00's disposable tests are environment evidence, not proxy tests. No project tests, benchmarks, containers, or implementation are authorized by the P01 documentation task.
