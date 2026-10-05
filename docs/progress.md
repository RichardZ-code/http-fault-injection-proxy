# Phase progress

P00 starting source: `cabf7c01134a774f846c72dc0b7864e3426c48e0` (`Initial commit`). P02 starting source: committed P01 `708b5613ea191c89e0178c2af61fcfd2373492fe` (`docs: define proxy behavior and development rules`). This record separates supplied acceptance from observed local checks. The P02 patch has no commit identity or hosted CI result.

| Phase | Status | Evidence / remaining gate |
| --- | --- | --- |
| P00 | PASSED (accepted user-supplied report) | Go 1.27.1 darwin/arm64 discovery, native build/run, cgo, ordinary/race tests, vet, normal build cache, fresh module download/checksum verification; disposable files removed and checkout preserved |
| P01 | ACCEPTED / REVIEW CLOSED (user-supplied handoff) | Five corrected documents verified committed and identical to the clean working tree at P02 start; finalization correction present |
| P02 | READY FOR REVIEW | CLI/module scaffold and baseline CI authored; local checks passed; patch unstaged/uncommitted; both hosted native jobs PENDING and full acceptance not established |
| P03 | NOT STARTED | Real pass-through HTTP |
| P04 | NOT STARTED | Strict fault configuration/decisions |
| P05 | NOT STARTED | Full deadlines and bounded lifecycle |
| P06 | NOT STARTED | Metrics/logs |
| P07 | NOT STARTED | Retry example and Docker; Docker setup deferred here |
| P08 | NOT STARTED | Correctness review and vulnerability check |
| P09 | NOT STARTED | Measurements; k6 setup deferred here |
| P10 | NOT STARTED | Final CI and nonpublishing release preparation |
| P11 | NOT STARTED | Verified README/demo and claims |
| P12 | NOT STARTED | Focused independent final audit |
| P13 | NOT STARTED | Frozen reviewed release, authorized publication, consumer checks |

## P00 evidence accepted for P01

The user authorized P01 after reviewing the P00 recheck. Its disposable tests established local development capability, not proxy correctness. Normal cache writes and module networking required scoped permissions; initial sandbox cache/DNS restrictions were resolved by unchanged approved retries. Go/cgo/architecture/proxy/checksum configuration was not persistently changed. The temporary download of `github.com/google/uuid@v1.6.0` did not select a project dependency.

GitHub Desktop is the user's commit/push route. CLI authentication is optional and the separate gh account is left unchanged. Live remote default-branch verification and actual Desktop push capability were not established. Docker and k6 remain deferred to P07/P09. These accepted findings were not rerun as P00 checks during P01.

## P01 evidence and review state

Scope: `AGENTS.md`, `README.md`, `docs/design.md`, `docs/design-decisions.md`, and this file only. The proposed behavior contract has one normative location; decisions D01-D12 provide provenance, rationale, tradeoffs, and future verification. README states implementation is unavailable.

Documentation self-review: full-file reading and requirement reconciliation; relative links/anchors; YAML example syntax and planned values; defaults/limits, exits, labels/outcomes, ordering and phase gates; private-path/secret/unsupported-claim/punctuation checks; `git diff --check`; tracked/new-file inspection and unchanged HEAD/branch/staged-state checks. Existing Ruby/Psych checked example syntax only, not the future Go parser. A check helper initially misinterpreted the expected exit 1 from new-file no-index diffs; corrected interpretation and direct whitespace checks passed without a document defect. These are documentation checks, not HTTP tests or an independent audit. No implementation tests, CI, benchmarks, containers, or proxy runtime demonstrations were run.

The source PDF and Idea_2.txt were read as external references, not copied into the repository. Current primary upstream sources and installed Go source informed API/dependency selection; release pins and compatibility checks remain P02 work. No material source conflict or blocking question is identified. All selected application refinements remain proposals.

The user supplied a completed focused independent P01 audit identifying one material P2 gap: buffered downstream bytes could outlive the forwarding deadline after premature cleanup/baseline restoration. Verified the finding against the installed Go 1.27.1 VERSION and source: ReverseProxy copy completion, ResponseController flush delegation, native FlushError, post-handler finishRequest/chunkWriter.close, and write-deadline clearing after finalization. This is source-supported design evidence, not a reproduced implementation failure.

Narrow corrections: require a checked error-observing final flush before terminal observation with the forwarding context/callback still active; retain the committed proxied response's absolute write deadline through server finalization; delimit handler observations from later chunk/trailer writes; add the P05 buffered-tail/slow-reader and finalization checks; reconcile D08/D10 and metrics duration/outcome wording. Only design.md, design-decisions.md, and this progress record changed in this correction. Full affected-file reading, consistency/link/anchor/whitespace checks and git diff --check passed; HEAD/branch/staged state and the original five-file P01 write set remain unchanged. No proxy tests, builds, CI, benchmarks, containers, or P02 work ran.

The latest P02 request supplies user acceptance and closure of the focused independent P01 finding. P02's read-only starting gate verified the five reviewed blobs, the corrected finalization contract, clean main/tracking branch, and repository identity. This is a starting-state consistency check, not a reopened independent audit. Historical proposal/pending wording in the preserved contract is superseded by that explicit acceptance.

Suggested commit summary only: `docs: define proxy behavior and development rules`. No staging, commit, push, tag, workflow dispatch, publication, visibility change, or persistent configuration change is authorized by this documentation record.

## P02 local evidence and remaining gate

Starting/final HEAD remains `708b5613ea191c89e0178c2af61fcfd2373492fe`, on main tracking origin/main. Effective fetch/push identity matches the intended repository. No fetch/pull, identity/authentication change, staging, commit, or publication occurred.

Observed Go 1.27.1 with native darwin/arm64 host/target, cgo=1, Apple Clang 21.0.0, empty inherited GOFLAGS/no workspace/public-module exclusions, default public proxy and sum.golang.org. Inherited GOTOOLCHAIN=auto was overridden per process to local; GOWORK=off and CGO_ENABLED=1 were explicit. Normal caches were used, plus a temporary isolated GOMODCACHE for the required fresh dependency pass. No global Go setting changed.

Implemented CLI help/version, presence-aware options/environment/defaults, pure upstream/listener/path syntax validation, and explicit unavailable run/config-check actions. Package boundaries exist only where P02 work needs them; no fault/proxy implementation placeholders. The two selected dependencies are exercised only by limited compatibility tests. Pin/API/runner sources are recorded in [P02 decisions](design-decisions.md#p02-implementation-record).

Passed locally (exit 0): gofmt validation including new untracked Go files; `go mod tidy -diff`; `go mod verify`; `go vet ./...`; `go test -count=1 -timeout=120s ./...`; `go test -race -count=1 -timeout=120s ./...`; `go build -o bin/faultproxy ./cmd/faultproxy`; built-binary subprocess smokes; documentation/link/style and workflow YAML/shell/structure checks; representative Git ignore checks and static Docker context-rule inspection; `git diff --check`; finalized module metadata preservation and unchanged accepted AGENTS/design blobs. Race results cover in-process code, not the ordinary subprocess binary or future proxy.

Fresh-cache pass: `go mod download -json`, checksum comparison to Go-generated go.sum, `go mod verify`, native uncached tests, `go mod tidy -diff`, and selected module-graph inspection passed under the isolated GOMODCACHE. The cache and owned temporary directories were removed; shared caches were not deleted. Actual download/hash verification is distinct from cached integrity, metadata compatibility, and hosted execution.

Failure record: initial sandbox DNS queries failed; scoped read-only retries succeeded. Authoring tidy failed to create the normal module cache; unchanged scoped retry succeeded. Sandbox cache access blocked tidy-diff/package dependency inspection; unchanged scoped retries passed. A normal build emitted a sandbox stat-cache warning but exited 0. The first fresh-cache helper wrongly required new sumdb lookup files despite existing go.sum entries: Go download passed, the helper failed, and cleanup ran. Installed Go checksum source confirmed existing sums avoid redundant lookups; the corrected helper checked downloaded hashes against generated sums and passed. No code assertion was weakened to obtain these passes.

External-directory demonstration with runtime PATH empty: help/version exit 0 with stdout only; unknown option exit 2; well-formed run/config-check exit 1 with stderr-only unavailable messages. Actual binary version identifies dev, the P01 base revision plus dirty state, and go1.27.1. It does not identify this patch as committed/released. Ignored `bin/faultproxy` is retained for user review.

CI configuration: explicit ubuntu-24.04 amd64/macos-15 ARM64 matrix, exact Go 1.27.1, verified official action commit pins, read-only permissions and bounded jobs. Hosted Linux/macOS execution: NOT RUN / PENDING. No CI URL or new source SHA is invented. No forwarding, fault-engine, lifecycle, application-metrics, Docker, benchmark, vulnerability campaign, or release verification occurred.

Review the CLI parser/option validation, executable boundary tests, dependency/prerelease record, workflow assertions, ignore rules, and README claims. After accepting the patch, commit/push through Desktop and inspect both native jobs for the resulting actual SHA; provide the run URL/results for verification. Full P02 acceptance requires those results. P03 remains NOT STARTED and needs a separate request.

Suggested commit summary only: `build: add Go CLI scaffold and baseline CI`. Changes remain unstaged and uncommitted.
