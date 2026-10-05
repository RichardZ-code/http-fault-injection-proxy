# Design decisions

Entries D01-D12 record the P01 decisions and their original proposal status. The user accepted P01 and closed its focused independent review, including the final-flush/deadline correction, in the P02 handoff. This supersedes historical pending-review wording without changing the accepted semantics or claiming implementation. The [design](design.md) remains the single behavior contract; the P02 record below captures actual dependency/tooling choices.

There are no identified material source conflicts or blocking questions. Ordinary values and policies left open by the guide have concrete proposals below. Exact dependency/tool/action/image pins are implementation-phase work.

## D01 - Primary dependencies

- Decision: select `go.yaml.in/yaml/v4` and `github.com/prometheus/client_golang`, alongside the standard library and accepted Go 1.27.1 baseline.
- Status: proposal; exact release pins/API compatibility pending P02.
- Source/refinement: guide page 6 permits a maintained YAML parser and Prometheus client. The [YAML organization policy](https://github.com/yaml/go-yaml) recommends v4 for new work and limits older majors to security fixes; the [Prometheus upstream](https://github.com/prometheus/client_golang) provides the intended client. These are references consulted, not proof that an unselected release works.
- Rationale/tradeoff: use maintained upstream paths and existing node/registry facilities instead of custom parsers/exposition. v4 needs release-specific API verification; both modules' transitive dependencies still need checksum/vulnerability review. The P00 UUID probe is not a selected direct runtime dependency.
- Verification: P02 release/module metadata and native dependency checks; P04 schema matrix; P06 isolated registry behavior. Contract: design sections 1, 4, 8.

## D02 - Explicit restricted YAML schema

- Decision: presence-aware recursive schema validation, required version/rules, explicit empty ruleset, and concrete omission/zero/null behavior. Reject aliases/anchors/merges/directives/tags/coercions and additional documents, with depth/node limits.
- Status: proposal.
- Source/refinement: guide page 7 requires strict keys/types/pairs; selected P01 refinements restrict ambiguous YAML features and choose seed/timeout defaults explicitly.
- Rationale/tradeoff: deterministic validation with useful field/line diagnostics; some general YAML conveniences are intentionally unavailable. A parser option that ignores later documents cannot meet rejection requirements.
- Verification: P02/P04 pinned-parser behavior, malformed/duplicate/nested/extra-document tests, exact boundary/presence cases. Contract: design section 4.

## D03 - Fixed resource and timing budgets

- Decision: adopt the single resource/default table, including guide-suggested config/rule caps, bounded input/delay/forwarding budgets, finite server/transport bounds, and counter overflow rejection.
- Status: proposal.
- Source/refinement: guide pages 8/11 require finite limits and coherent deadline ownership; numerical refinements beyond its examples are P01 choices.
- Rationale/tradeoff: bounded local-test behavior and one small interface; high/slow/streaming workloads may exceed the supported limits. Per-request limits are not aggregate process-memory protection. Server/write or transport deadlines may win without a delivered 504.
- Verification: P03 input/transport boundaries; P04 numeric limits/overflow; P05 body/deadline/downstream-write cases. Contract: design sections 4, 7.

## D04 - Complete bounded body admission before decisions

- Decision: pre-buffer accepted request bodies before rule allocation and upstream forwarding; reject oversize/truncated/timed-out input without consuming fault state.
- Status: proposal.
- Source/refinement: guide page 11 requires honest body-limit semantics; the explicit P01 request prefers bounded pre-forward buffering for rejection without upstream effects.
- Rationale/tradeoff: a 413 can reliably precede rule/upstream effects; concurrent buffers and copies cost memory, and large/slow streaming uploads are unsupported. Response transfer remains streamed under deadlines.
- Verification: P03 known/chunked/exact-cap/cap+1/truncated bodies; P04 no rule allocation on rejection; P05 input cancellation and no upstream side effects. Contract: design sections 2, 4.

## D05 - Small CLI and honest validation modes

- Decision: retain the guide's flags/environment/listeners, presence-based override semantics, strict mode/syntax handling, and fixed exit contract. Help/version need no runtime config; config-check validates without network/listener effects.
- Status: proposal.
- Source/refinement: guide pages 7/8/12 establish the interface and proposed exits; empty values, duplicate flags, numeric listener addresses, output text, and mode rules are P01 refinements.
- Rationale/tradeoff: predictable local/container startup with no implicit config; listener hostnames and automatic fallback from explicit empty values are unsupported. Early unavailable operations must fail explicitly.
- Verification: P02 CLI cases outside checkout; P03 dual-bind failures; P04 full config-check validation. Contract: design section 3.

## D06 - Fixed HTTP origin and diagnostic metadata

- Decision: HTTP/1.1 origin-form requests with seven supported methods; validate path/query, preserve supported forwarding semantics, rebuild forwarded metadata after hop stripping, generate request IDs with crypto/rand, and reserve a status-only synthetic marker. Sanitize reserved metadata on final, informational, and trailer paths.
- Status: proposal.
- Source/refinement: guide pages 8/13/14 require fixed routing, Rewrite, sanitized forwarding, reserved fault evidence, and HTTP limits. Specific rejection/header/request-ID policies are P01 refinements informed by installed Go source.
- Rationale/tradeoff: clear synthetic/upstream distinction and no client-controlled routing; the tool does not support forward-proxy forms, upgrades, malformed queries, arbitrary protocols, or byte-exact header/framing preservation. Diagnostic headers are not authentication.
- Verification: P03 Host, escaped/query values, Connection tokens, reserved 1xx/trailers, HEAD/204/redirect/unsupported forms; P04 synthetic marker versus delayed real 503. Contract: design section 5.

## D07 - Independent synchronized rule decisions

- Decision: first-match literal prefixes; per-rule uint64 counter and mutex; SHA-256-derived per-rule PCG seeds; one probability draw per match including 0/1; immutable decisions; no rollback after allocation.
- Status: proposal.
- Source/refinement: guide pages 7/14 require exact Nth counts, synchronized independent state, no fall-through, and controlled seeded repeatability. PCG/seed derivation/exhaustion are P01 choices.
- Rationale/tradeoff: other-rule traffic cannot alter a rule's sequence, and locks remain short. Concurrent client assignment and cross-Go-version RNG sequences are not promised.
- Verification: P04 exact serial/concurrent selections, seed fixtures, independent rules, boundaries/restart; P05 cancellation after allocation; race suites. Contract: design section 6.

## D08 - Causal outcomes and commitment-aware deadlines

- Decision: keep forwarding context live through body transfer and a checked final downstream flush before terminal observation; retain an absolute write deadline no later than the forwarding deadline through committed proxied response finalization; distinguish final commitment, sent status, terminal outcome, and upstream events with fixed precedence.
- Status: proposal.
- Source/refinement: guide pages 8/15/16 distinguish pre-header 504, partial transfers, cancellation, and true upstream failures. The vocabulary/overlap precedence is a P01 refinement. The supplied P2 audit finding was verified against installed Go 1.27.1: ReverseProxy does not always final-flush fixed-length responses; finishRequest writes final framing/trailers after the handler, before clearing the write deadline.
- Rationale/tradeoff: truthful handler-observable classification when a sent 200/503 later fails; no replacement error body after commitment or assertion of delivery from WriteHeader/flush. Error-observing FlushError delegation prevents wrappers from hiding flush failures. A retained deadline bounds server finalization, but post-handler framing/trailer errors cannot change handler metrics.
- Verification: P05 pre/post-header timeout, stalled body/write, buffered-tail/slow-reader final flush, retained finalization deadline, client/force/downstream overlap; P06 same-state metric/log reconciliation. Contract: design sections 5, 7, 8, 10.

## D09 - One shared drain budget and bounded forced cleanup

- Decision: concurrent shutdown of both listeners with one grace budget; preserve admitted work while draining, then explicitly cancel/close and use a bounded join budget. Runtime/server failure cleans up both and exits unsuccessfully.
- Status: proposal.
- Source/refinement: guide pages 8/15 warn that Shutdown alone does not cancel handlers. Exact budgets, drain admission rejection, and forced exit behavior are P01 refinements.
- Rationale/tradeoff: process teardown cannot wait indefinitely; an unjoined task or blocked output cannot be described as a clean drain/leak-free shutdown.
- Verification: P03 partial-startup cleanup; P05 real process signals, clean/forced concurrent teardown, 20 uncached repetitions then full suites; P07 container stop/restart. Contract: design section 7.

## D10 - Four families and event-based accounting

- Decision: isolated registry with four application families only, no default Go/process/promhttp collectors; bounded labels/buckets; one terminal request/duration observation after the checked final proxied flush and handler cleanup, excluding post-handler server finalization; started delay/status events counted separately; real 5xx plus subsequent independent body failure can be two upstream events. INFO JSON access logs are enabled with no tuning flag.
- Status: proposal.
- Source/refinement: guide page 16 defines families and recommends combined-action accounting. Buckets, vocabulary, event deduplication, collectors, and logging defaults are P01 refinements.
- Rationale/tradeoff: distinct selected/started/terminal/delivered quantities and bounded cardinality. Handler completion cannot certify later chunk/trailer writes or client receipt. Event totals are not request totals; logging cost is included in the recorded benchmark environment, and output blocking remains a teardown failure possibility.
- Verification: P06 reconciliation example, cancellations/partial failures/rejections, concurrent scrapes, header/query/secret-free logs. Contract: design section 8.

## D11 - Phase-based tests, retry demo, and measurements

- Decision: preserve the P00-P13 gates, P05 repetition campaign, focused independent reviews, real HTTP/process tests, bounded GET-only retry policy, and the guide's paired benchmark/correctness/retry protocols.
- Status: proposal; no tests/measurements are executed by this decision.
- Source/refinement: guide pages 9/12-23 define phase ownership and benchmark protocol; retry timing/status/body-cap constants are P01 refinements.
- Rationale/tradeoff: useful failure examples and source-attributed workload evidence without arbitrary test-count/throughput goals. The retry schedule changes physical faults per logical operation; closed-loop p95 comparisons cannot establish capacity/SLA claims.
- Verification: P07 attempt/backoff/exhaustion/deadline cases; P08 review/vulnerability check; P09 raw paired data and count reconciliation. Contract: design sections 9-11.

## D12 - Native platforms and reviewed artifact promotion

- Decision: native darwin/arm64 and linux/amd64 binaries, linux/amd64 image, explicit emulation disclosure, baseline native CI in P02, and frozen-source nonpublishing preparation followed by separately authorized publication/promotion and consumer verification.
- Status: proposal; all platform/CI/container/release verification is pending.
- Source/refinement: guide pages 17/21/24-27 select these platforms and release boundaries. No extra platform or publishing route is proposed.
- Rationale/tradeoff: a small inspectable release inventory tied to reviewed bytes; no native arm64-container claim and no rebuild substituted for the tested image. User acceptance alone does not authorize remote publication actions.
- Verification: P07 packaging; P10 actual native CI/preparation; P12 audit; P13 native assets/source build/anonymous image consumption. Contract: design section 12.

## Focused independent review handoff

Review [AGENTS.md](../AGENTS.md), [README](../README.md), [design](design.md), this record, and [progress](progress.md) against the guide and latest P01 request. Remain read-only; do not stage, edit, run implementation tests, or imply P02 authorization.

Priorities: D02-D04 schema/body/resource boundaries; D06-D08 reserved metadata, admission/allocation order, RNG, causal classification and blocked-write deadlines; D09 shutdown/join feasibility; D10 event reconciliation; D11-D12 phase/provenance/publication boundaries. Report each material finding with severity, trigger, consequence, contract evidence, and smallest justified correction. Distinguish an implementation feasibility gap from a style preference. The supplied independent audit reviewed the five documents and identified one P2 final-flush/deadline gap. The narrow correction is documented in design sections 5, 7, 8, 10 and D08/D10; confirm error-observing flush ordering, retained committed-response deadlines, and the handler/server observation boundary. Targeted read-only closure confirmation and user acceptance remain pending before P02; no repeated full audit is required absent another material finding.

## P02 implementation record

The handoff above is the historical P01 review request, now closed by the user's supplied acceptance. P02 starts from committed P01 source `708b5613ea191c89e0178c2af61fcfd2373492fe`. D08/D10 and the normative contract are preserved; finalization implementation remains later-phase work.

### Dependency pins (D01 implementation)

| Direct module | Pin | Status | Declared minimum Go | Current import |
| --- | --- | --- | --- | --- |
| `go.yaml.in/yaml/v4` | `v4.0.0-rc.6` | Prerelease | 1.18 | `internal/config/yaml_compat_test.go` |
| `github.com/prometheus/client_golang` | `v1.24.1` | Stable | 1.25.0 | `internal/metrics/prometheus_compat_test.go` |

Official repository tags and public module version lists were rechecked during P02: no stable v4 tag exists in the approved YAML stream; rc.6 is its latest tag. Keep the approved major rather than silently substituting v3. Its API can still change before stable release. The limited node/streaming probe passes, but full strict-schema/depth/alias validation remains P04 verification. Prometheus's stable pin supports private Registry/HandlerFor; the compatibility probe registers one test-only counter and asserts exposition and absence of additional collectors. Neither dependency is currently imported by the executable.

Selected direct metadata reports no retraction/deprecation. The selected graph retains upstream requirements beyond compiled packages, including a deprecated `github.com/golang/protobuf` requirement; it is not imported by the scaffold/tests. The compiled test dependencies use the modern `google.golang.org/protobuf` path. Prometheus common/procfs/x/sys declare Go 1.25.0; protobuf declares 1.23. Upstream dependency tests also account for go.sum entries such as testify/goleak and older YAML majors, although project assertions use only the standard library. These are transitive graph/test requirements, not added primary runtime choices or a P00 UUID dependency decision. Native Go 1.27.1 compilation and ordinary/race tests passed; hosted Linux/macOS checks and the P08 vulnerability campaign remain pending.

Checksums were generated by Go with `sum.golang.org` enabled and no public-module exclusions/replaces. A fresh isolated module cache verified downloaded hashes against generated go.sum, passed integrity/tests/tidy checks, preserved metadata, and was removed. This is separate from declared compatibility and from an assertion of hosted execution.

Official pin references: [YAML tags](https://github.com/yaml/go-yaml/tags), [YAML tagged go.mod](https://github.com/yaml/go-yaml/blob/v4.0.0-rc.6/go.mod), [Prometheus release](https://github.com/prometheus/client_golang/releases/tag/v1.24.1), [Prometheus tagged go.mod](https://github.com/prometheus/client_golang/blob/v1.24.1/go.mod).

### Toolchain and native CI (D11/D12 implementation)

The module's `go 1.27.1` line sets its minimum requirement. No redundant toolchain directive is added. Local verification and setup-go select/assert exactly Go 1.27.1 with process/job-local `GOWORK=off`, `GOTOOLCHAIN=local`, and `CGO_ENABLED=1`. A go directive alone does not prevent a newer installed toolchain; CI's exact version/native assertions enforce the verification baseline. See [Go toolchain documentation](https://go.dev/doc/toolchain) and [nonmutating tidy](https://go.dev/ref/mod#go-mod-tidy).

| Official Action | Release | Verified tag commit |
| --- | --- | --- |
| `actions/checkout` | v7.0.1 | `3d3c42e5aac5ba805825da76410c181273ba90b1` |
| `actions/setup-go` | v7.0.0 | `b7ad1dad31e06c5925ef5d2fc7ad053ef454303e` |

Resolved official tag refs to commit objects and read each pinned action.yml/README. Both use Node 24; upstream documents runner v2.327.1 or later for that runtime. Current [GitHub runner documentation](https://docs.github.com/en/actions/reference/runners/github-hosted-runners) lists ubuntu-24.04 as x64 and macos-15 as ARM64 for public/private repositories. The workflow asserts host/target/cgo and Runner OS/architecture; runner access/allowance and actual job execution remain unverified.

References: [checkout v7.0.1](https://github.com/actions/checkout/releases/tag/v7.0.1), [pinned checkout source](https://github.com/actions/checkout/blob/3d3c42e5aac5ba805825da76410c181273ba90b1/action.yml), [setup-go v7.0.0](https://github.com/actions/setup-go/releases/tag/v7.0.0), [pinned setup-go source](https://github.com/actions/setup-go/blob/b7ad1dad31e06c5925ef5d2fc7ad053ef454303e/action.yml), [GitHub secure-use guidance](https://docs.github.com/en/actions/reference/security/secure-use).

CI uses push/PR-to-main checks, read-only contents permission, a 15-minute timeout, and matrix fail-fast=false. Checkout credential persistence is disabled; setup-go uses its built-in go.sum cache key. Both native jobs check formatting, nonmutating tidy, module integrity, vet, uncached ordinary/race tests, build, subprocess smoke, and metadata/whitespace preservation. No publication/dispatch/container jobs are added. Static YAML/shell checks are not hosted evidence.

### Scaffold action boundary (D05 implementation)

Pure options are checked before run/config-check reaches an explicit unavailable action, which returns exit 1 under the accepted action/runtime-failure category. No scenario file is opened, generic YAML parsing is never certified as scenario validation, and no server is started. Help/version succeed independently of runtime configuration but still reject malformed CLI syntax. Development metadata comes from actual build VCS settings with dirty/unknown distinctions; no release identity is assigned to this uncommitted patch.
