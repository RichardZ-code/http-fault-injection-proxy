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
| `go.yaml.in/yaml/v4` | `v4.0.0-rc.6` | Prerelease | 1.18 | P02 compatibility probe; P03 strict no-fault admission |
| `github.com/prometheus/client_golang` | `v1.24.1` | Stable | 1.25.0 | `internal/metrics/prometheus_compat_test.go` |

Official repository tags and public module version lists were rechecked during P02: no stable v4 tag exists in the approved YAML stream; rc.6 is its latest tag. Keep the approved major rather than silently substituting v3. Its API can still change before stable release. The P02 node/streaming probe passed; the full schema matrix remained P04 work. P03 subset admission is recorded below. Prometheus's stable pin supports private Registry/HandlerFor; the compatibility probe registers one test-only counter and asserts exposition and absence of additional collectors. Neither dependency was imported by the P02 executable; P03 now imports the pinned YAML module for subset admission.

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

At P02, pure options were checked before run/config-check reached an explicit unavailable action, which returns exit 1 under the accepted action/runtime-failure category. No scenario file was opened, generic YAML parsing was not certified as scenario validation, and no server was started. Help/version succeed independently of runtime configuration but still reject malformed CLI syntax. Development metadata comes from actual build VCS settings with dirty/unknown distinctions; no release identity was assigned to that scaffold patch.

## P03 implementation boundary

The initial startup/configuration boundary was unresolved: validation was required before binding, while full parsing belonged to P04. The user subsequently approved a temporary P03 run contract containing only required `version: 1` and explicit `rules: []`. It rejects unknown/duplicate fields, optional seed/timeout settings, nonempty rules, multiple documents and the other unsupported YAML features. Preserve the 1 MiB cap+1 input, UTF-8, parser depth 8 and node-count 10,000 limits. Admit a readable regular file and finish pure options plus strict subset validation before either bind; invalid input exits 2, bind/runtime failure exits 1. The CLI uses the same production runtime as internal fixtures. Full config-check remains explicitly unavailable with exit 1 and certifies no file.

This is a user-approved phase refinement to D02/D03/D05, now recorded in design section 3. P04 replaces the temporary validator with section 4's accepted full schema, including actual optional-setting and rule effects. It must not keep an additional subset mode, format, flag or bypass. The subset does not apply an unimplemented forwarding-timeout default. P05 owns full forwarding deadlines, checked final flush/retained finalization deadlines and graceful signals; P06 owns terminal observations. No configuration bypass or full-schema certification was added.

The runtime's metadata wrapper exists only to sanitize ReverseProxy's separate informational-response path. It delegates error-observing flushes and ResponseController unwrapping; it does not record completion, alter copy-error aborts, cancel a forwarding child context, or restore write deadlines. A separate response-body adapter removes reserved/hop-by-hop trailers at EOF/close, including unannounced keys. The configured transport remains shared and performs all body forwarding through ReverseProxy. D08/D10 and the normative finalization correction are preserved for P05/P06.

Current admin `/metrics` is a known but unavailable route: GET/HEAD returns 503, text/plain, `metrics not implemented yet\n` (HEAD bodyless). Known-route wrong methods retain 405/Allow GET, HEAD; unknown routes retain 404. This is an explicit P03 unavailable-route clarification, not a fake registry or data-plane fault response. Full metrics remains P06 work. Go's general OPTIONS handler is disabled so application rejection of OPTIONS `*` remains possible. Input-timeout classification accounts for net/http cancelling its request context after a read-deadline error; client-disconnect and input-timeout evidence are tested separately.

## P04 implementation boundary

P04 replaces the temporary P03 subset with the accepted full schema through one production loader shared by run and config-check. Explicit empty rules use the same schema/defaults; no compatibility mode remains. Config-check returns after pure option and complete file validation, before counter/RNG construction or listeners. `upstream_timeout_ms` is validated, defaulted and retained in the owned runtime configuration, with enforcement deferred to P05 as authorized by the P04 request.

The bounded UTF-8 reader preserves the pinned parser's acceptance of a leading UTF-8 BOM; other encodings fail. The validator counts the document node, root, collection children, mapping keys and values; document depth is zero and root depth is one. It rejects depth greater than 8 or count greater than 10,000 before typed conversion. The parser separately applies its depth-8 plugin. The node guard operates after parsing and is not a guarantee about every parser allocation; the cap+1 byte bound applies before parsing. Exact guard boundaries unreachable within the 100-rule schema are tested directly.

Request-owned decisions carry rule ID, allocated sequence, delay and selected status. Package-private dispatch results distinguish forwarding, synthetic response writes, cancellation and observed synthetic write errors. They do not assert terminal transfer completion, later framing/trailer success or client receipt. The existing metadata wrapper retains error-returning flush delegation. D08/D10, checked final flush and retained committed-response deadlines remain intact for P05/P06; no completion observer or deadline restoration is added here.

## P05 implementation clarification (D08/D09)

The forwarding child owns its total timer, cancellation callback, causal transfer state and started dials. Installed Go 1.27.1 Transport detaches dial cancellation with WithoutCancel while preserving context values; the narrow DialContext adapter restores the originating forwarding lifetime and joins started dialing during request cleanup. ReverseProxy still performs the forwarding/copy operation. No detached forwarding handler or retry loop is introduced.

The response wrapper installs the tighter absolute deadline at the first proxied downstream write. Ordinary pre-header stalls have not expired the connection's write deadline, so the handler can generate a bounded 504 under the original baseline after stopping/joining the callback. Informational writes receive a bound without final commitment; an already failed informational write can still prevent later error delivery. For committed responses the wrapper delegates FlushError, observes the final flush before cleanup, and never relaxes the retained deadline. A connection adapter records native read/write causality: net/http's write-error close can cancel its request context, which alone is not evidence of independent client cancellation. Earlier observed I/O failures do not age into forwarding timeouts during cleanup.

Signal notification is separate from the active-request base context. One coordinator gates admission, invokes both Shutdown calls with the same context, records force causes at grace expiry, and joins handlers and native connection finalization separately from serving loops. Listener Close is owned once across Serve-registration/shutdown races. Genuine accept/close errors survive expected-sentinel handling, including errors the native server would otherwise suppress during shutdown or connection closure. The fixed 5-second grace and additional 1-second cleanup remain the accepted policy; bounded failure results do not certify complete cleanup. P06 collectors/access logs and post-handler delivery observations are not implemented.

P05 audit corrections: ErrorHandler records transport errors and their deadline state through the same causal recorder as body/write failures, before diagnostic output. The original transport error remains available if local error delivery fails; failure to install its write deadline, write or flush aborts with a downstream terminal outcome. Cancellation/force precedence and true forwarding-expiry classification remain intact.

The executable's final runtime-error diagnostic uses the coordinator's already running cleanup deadline, with no renewed allowance. Only native pipes, sockets and character devices already configured as nonblocking receive at most one syscall write, capped at 512 bytes. Shared open-file-description status flags are never changed, even temporarily; duplicating a descriptor would not isolate those flags. Initially blocking sinks (including ordinary inherited blocking stderr), regular files and arbitrary writers are skipped. Expired allowance, full/failed output or a short write can drop or truncate the diagnostic. No detached writer or indefinite join is added. Earlier blocked diagnostics may remain unjoined at the reported cleanup-failure boundary; process exit 1 does not certify clean cleanup.


## P06 implementation details (D10)

Contract map: design section 8 and D10 own the four families, per-family labels, finite vocabularies, action/event reconciliation, and duration units/buckets; sections 5 and 7 plus D08/D09 own admission, causal outcomes, checked flushing, retained finalization deadlines and cleanup. P06 leaves the normative design unchanged. P05's F01/F02 corrections remain constraints.

Select lazy vectors in one private registry per runtime, registered before either listener. Untouched families/combinations are absent. HandlerFor uses the pinned client's zero HandlerOpts, without additional collectors, timeout workers, gathering callbacks or request instrumentation. No transactional active-scrape promise is added. Upstream evidence is deduplicated at the causal boundary and published with terminal accounting; action increments occur at actual starts outside the allocation lock. Pre-handler draining rejection remains outside the admitted-data accounting set. Terminal time includes handler-owned cleanup, excludes subsequent observer work, outer lifecycle removal and native post-return finalization.

The access-sink guarantee was not previously specified. Select the conservative F01-compatible capability policy: only native pipes, sockets and character devices already nonblocking; skip blocking stderr, regular files and arbitrary writers. Share the audited one-attempt native primitive with terminal diagnostics, never altering status flags or introducing goroutines, queues, retries, fallback output or another allowance. Production runtime startup, proxy/server error adapters and shutdown events also use this policy. Genuine errors remain internal while output uses fixed safe classifications; help/version/config-check/validation command output is unchanged.

Use enabled INFO slog JSON access records, no toggle, with 1,024-byte encoded access bounds, typed allowlisted fields and whole-record drops for invalid/oversized input. A private formatter buffer avoids sharing a formatter lock with external I/O. Per-runtime TryLock contention drops output rather than waiting; permitted native writes are single attempts. Errors/full/closed/unsupported sinks drop output; native short writes may leave partial fragments and are never described as delivered JSON. No ordering, durability, lossless delivery, or atomicity across other descriptor users is guaranteed. Successful pipe/socket output and skip/drop/broken-sink behavior are separately tested. Character-device support is capability-based, not a claim about every device driver.

Runtime diagnostic JSON retains a 512-byte bound. The final terminal diagnostic retains F01's existing deadline and cap/truncation policy. Its safe printed classification cannot replace the real exit failure or contaminate the already frozen request result. F02 upstream transport evidence remains distinct from later downstream delivery failure. Log loss never drops collector updates. Earlier P05 text about potentially blocked runtime diagnostic writers describes its historical implementation; P06 removes that blocking runtime path without changing drain/cleanup budgets.

P06 capture usability follow-up: provide the external Python 3.9+ POSIX [launcher](../scripts/capture_logs.py) and [runnable recipe](../README.md#runnable-log-capture), without another proxy mode/flag/dependency or a changed sink policy. Configure only newly owned pipes before inheritance, continuously drain stderr, forward SIGINT/SIGTERM, and reap/preserve the proxy status. A separate owned file writer receives a bounded nonblocking pipe; capture backpressure drops chunks, and capture failure asks the proxy to stop once while the launcher drains/discards. The writer's at-most-0.5-second finish allowance occurs after proxy reaping, then kill/reap, with no extension of proxy deadlines. Exclusive new files prevent overwriting existing captures. Partial writes/chunk drops/tail loss and capture failures can leave incomplete files; exit 0 is the proxy result, not capture certification. This external process ownership differs from the proxy's unchanged worker/queue-free logging path. At P06 acceptance, the README's container capture arrangement was an unverified P07 plan; the subsequent P07 execution record below supplies the Docker evidence.

## P07 preparation decisions

Preserve design section 9's accepted 5 s operation/1 s attempt, 100/200 ms backoff, 1/3 total attempts, 64 KiB response limit, GET-only statuses 502/503/504 plus classified transport/attempt/body failures, no redirects and bounded body cleanup. The serial CLI selects mode none and six operations by default, permits 1-1000, emits JSON outcomes/history and exits 0/1/2 as documented. These CLI/result choices are P07 refinements, not values attributed to the guide. Oversized bodies and permanent DNS/request errors stop without retry; synthetic evidence annotates outcomes without deciding retry eligibility. Success requires complete 2xx consumption, no arbitrary content equality rule.

Go 1.27.1 `Transport.shouldRetryRequest` and `Request.isReplayable` permit replayable GET recovery on reused connections, including nothing-written and first-read failures. An application attempt counts one Client.Do plus response handling; server-observed traffic is separate. Keep the shared reusable transport and reconcile actual client/proxy/upstream counts rather than promising a wire bound. This resolves the earlier design's informal "physical attempt" wording under the explicit P07 handoff without adding proxy retries. Transport also uses WithoutCancel for dials; an attempt-owned context value restores cancellation in DialContext, seals/join-started dials in cleanup and captures failure before cleanup cancellation. The loop never abandons a request goroutine or renews operation time.

Fixture choices: fixed synthetic GET routes, small constant bodies, atomic calls/active/cancelled, no reset API, no query/body/header echo. Delay defaults 3 s with 1 ms-20 s bounds. Unknown-length flushed chunk prefix plus abort supports a real post-header incomplete-transfer demonstration; a small fixed-length prefix would remain buffered. Health/stats exclude data counts. Native loopback default; explicit Compose interface bind; finite HTTP and short fixture shutdown bounds.

Release-candidate retry lifecycle correction: `/ok` flushes headers before writing its small healthy body, selecting unknown-length/chunked HTTP/1.1 framing. The unchanged retry executable can consume a fixed-length body and close its idle transport before the proxy's checked flush returns and terminal observation occurs; that genuine cancellation must remain `client_cancelled`, even after a client-observed complete 200. For this controlled demo, chunk EOF follows native server finalization after terminal observation, so client exit cannot race that boundary. Python-owned connections still use their existing reconciliation lifetime. Production classification, exact outcome assertions, retry allocation/deadlines and the fixed-length `/benchmark` route are unchanged. This is a fixture framing decision, not a general client-receipt guarantee or a change to the historical measured workload.

Base metadata verified 2026-10-05 through official image pages and public registry manifests/configs. Digest pins below identify image indexes; linux/amd64 children are separately identified. SHA-256 of fetched manifests/configs was checked, with config OS/architecture and safe version/user metadata inspected. This is not a signature audit, layer execution, installed tool-version check or Docker build. Later P07 Docker execution is recorded in progress; this registry metadata alone does not establish it.

| Base reference | Pinned index SHA-256 | linux/amd64 child SHA-256 |
| --- | --- | --- |
| golang:1.27.1-bookworm | `69a7b9788769bec032d238959b61854e9ae87f57be9029ec04e9885fabf99195` | `966278043a40889499db9b0cd196fc789c37c385d41bd9a10cb1e7764af60cdc` |
| gcr.io/distroless/static-debian13:nonroot | `e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3` | `2293b36c7c9082bf4115aab724b4d2cddec82c8eba39bf27ac0517e159acf150` |
| python:3.14.8-slim-trixie | `c3e521df8b2b498a7a682e7e18676771cb80c6b75b8699af886b2d554ce40151` | `65a94bb37b630c482dfd31e5fb9b449cb26c31eab1b7a125cd6bd624acfe3b30` |

Sources: [official Go images](https://hub.docker.com/_/golang), [official Python images](https://hub.docker.com/_/python), [Python branch support](https://devguide.python.org/versions/), [distroless published Debian 13 images](https://github.com/GoogleContainerTools/distroless), public `registry-1.docker.io` and `gcr.io` v2 manifests. Python 3.14 is supported in bugfix maintenance; host Python 3.9.6 meets the reviewed helper's API minimum but is not selected as the capture-image runtime. No pip packages or additional Go module.

Build platform executes pinned Go and cross-builds ordinary static linux/amd64 artifacts with build-local CGO_ENABLED=0, GOTOOLCHAIN=local, GOWORK=off, -trimpath and -buildvcs=false. No inferred source identity from excluded .git; record dirty tree/artifact hashes separately. Default final `proxy` and optional `capture` copy identical /out/faultproxy bytes from one build stage. Distroless plain/fixture/client targets avoid compilers, source, caches and inspection-only utilities. Numeric nonzero UID/GID 65532:65532 applies to all runtimes. Capture explicitly uses Python/helper PID 1; no proxy/helper production changes, alternate sink policy or new allowance. Base indexes offering ARM64 do not add an ARM64 application-container claim.

Compose uses service upstream, explicit linux/amd64, only 127.0.0.1 published ports, readonly validated-file long binds without source-directory auto-creation, no restart masking, no Docker socket/privilege and no start-order/readiness equivalence. Host checks separately wait for fixture health and isolated admin health. A 10 s external stop allowance covers unchanged proxy 5+1 s, helper post-reaping 0.5 s and orchestration margin. Dedicated writable capture mount/fresh exclusive file is optional. The capture override explicitly maps a nonzero host UID/GID to its owned mode-0700 output directory; files remain 0600 and plain runtimes retain 65532:65532. The recipe rejects host UID zero before launch. Actual Docker Desktop sharing/UID mapping/readability must be tested before a container claim. Existing logs are never overwritten; changing filename requires recreation, not start. The subsequent Docker Desktop execution record below verifies these choices in that environment.


P07 Docker continuation: preserve the same 17-file patch and all production/helper bytes. Use the installed Docker.app CLI with a process-local PATH when needed, explicit local desktop-linux context and existing builder; no installer/backend/context mutation. Desktop 4.94.0, Engine/CLI 29.8.2, Compose 5.5.1, Buildx 0.37.2 and BuildKit 0.33.1 were observed. ARM64 Go build-platform execution and amd64 application-image execution were separately verified; Apple Silicon amd64 execution is emulated, not native Linux evidence. Backend identity is unclaimed.

Actual process/mount checks verified plain UID/GID 65532:65532 and capture override 501:20, read-only config (EROFS), loopback ports, host-readable 0600 captures and service routing. Plain and capture proxy SHA-256 both equal `29454d8a55ae760fd0e42ac922dbe18e8fc6faf15b59c552db62115ab02ea09f`; helper bytes equal the reviewed source. Local Docker image IDs, source attribution and all checks are recorded in progress. No container runtime code change was needed.

Actual signal/drain/force, startup/config/output errors, delayed fixture, record privacy/accounting and full undrained writer-queue checks passed. Proxy reaping was observed while the file writer remained stopped; its existing post-reaping allowance and owned-writer kill/reap remain separate from proxy deadlines and Docker SIGKILL. A real capture filesystem limit preserved proxy exit 0 with incomplete output, confirming exit preservation is not capture certification. A forced shutdown's fixed plain terminal diagnostic is distinct from JSON access records. Native Linux/hosted CI and later release claims remain separate gates; these local images identify the uncommitted P07 build inputs.

## P09 preparation choices

The latest explicit request splits P09 into reviewed preparation and separately authorized formal collection. The [method](../benchmarks/README.md) implements section 11/D11's nine pairs, VUs 1/25/100, three repetitions, alternating order, excluded 5 s warm-up and 30 s measured window, exact 1 KiB fixture, separate 1,000-request N=5 and three 1,000-operation trials per retry mode. No formal dataset existed at initial preparation; the separately authorized collection/import is recorded below.

Routine preparation selections: official standalone k6 2.3.0 native macOS ARM64 and Python 3.9.6 for formal collection; one continuous constant-VU invocation per path, common scenario.startTime and request-start membership; fixed-window cohort throughput with completion tail separate; measured-only HTTP-duration trends and explicit p50/p95/p99; fresh fixture/proxy per pair retained across members on a fixed campaign endpoint map; serial fixed-count/retry cohorts preserving the existing 5/1 s and 100/200 ms policy; type-7 interpolated retry quantiles across all logical outcomes; no automatic reruns and no pair-member splicing. These are selected mechanics, not additional guide requirements or measured results.

INFO access construction/attempts and metrics remain enabled. Blocking regular-file stderr is skipped by the accepted sink policy, not equivalent to disabling logging. Capture is excluded; scrapes/readiness are outside timed traffic. No production proxy, capture, dependency, Go/action pin or lifecycle contract changes. The fixture alone adds immutable startup-created 1,024-byte content on /benchmark with atomic data counts.

The explicit P09 external-output requirement supersedes section 11's older ignored in-checkout staging wording. All builds/config copies/logs/raw/derived records remain in exclusive external runtime directories until complete validation and separately reviewed import. Formal mode requires expected full SHA, clean source, committed byte/mode identity, exact tools and plan, and repeated input/tool/binary checks; code does not prove human authorization. Smoke records preserve dirty input identity and reduced settings and cannot satisfy formal validation. Invalid attempts/errors/cleanup remain evidence; kills cannot count as passing teardown. Formal results require user review/Desktop commit/push, exact native CI and explicit collection authorization. P10 and pending native Linux container/provenance gates remain separate.

## P09 import / P10-P11 combined preparation decisions

The latest explicit user request authorizes these three preparation tasks together; it does not authorize P12 or publication. Preserve the accepted behavior/deadline/sink contracts and all measured production/workload inputs. The measured source remains 0da4d1f4356027b16e9f4f32164ee9261585c33d. Import the independent review's exact 167-file inventory plus three generated receipt/readme/checksum files. Preserve complete originals/review durably in ignored local storage first; never substitute rebuilt binaries, rewrite raw populations/quantiles or relabel original checksums as matching redacted bytes. The published textual subset requires restoring original binary bytes in a disposable copy for full formal validation. See [result receipt](../benchmarks/results/2026-10-06-macos-arm64-0da4d1f43560/IMPORT.json).

P10 selects explicit source/version and same-workflow-commit candidate preparation, two native archives plus exact source export/SHA256SUMS, and saved tested linux/amd64 image/provenance artifacts. Source checks compare every tracked blob/executable bit directly as well as Git status. Reject archive traversal, links/devices, duplicate paths, set-id modes and excessive member/size counts before extraction. Promotion consumes an explicit successful preparation run/attempt, verifies reviewed provenance and artifact digests, and loads/promotes the saved tested image. No rebuild/latest-run/overwrite path is selected. Public consumers use unauthenticated downloads, reviewed checksum-file identity and empty Docker credentials with explicit registry digest plus store/config identities. GitHub token permissions remain read-only except packages:write in the manual promotion job. Artifact retention is 14 days; expiry cannot be hidden by an unreviewed replacement.

Routine tooling choices: existing exact Go 1.27.1/actions pins, official upload v7.0.1/download v8.0.1 commit pins, govulncheck v1.8.0, standalone k6 2.3.0/actionlint 1.7.12 archive checksum pins and existing Docker bases. Record runner compiler/Python/Docker identities without claiming their mutable host installations are fixed. Native Linux container helper asserts Linux host and amd64 daemon; local Mac helper evidence is explicitly emulated. JSON vulnerability streams are inspected for finding records; scans exclude Python/OS image analysis. Source/tool/permission details are in [release preparation](release.md).

Keep runtime version handling unchanged: the proposed version labels archive metadata while --version honestly reports dev/source/Go. P13's guide owns final release version handling and any necessary new source freeze; prepared workflows must not call package metadata an implemented runtime version. Source exports and -buildvcs=false containers can report unknown runtime source, so separate reviewed archive/build provenance is mandatory. Docker's .Id can be an OCI index digest in a containerd store; actual configuration identity is computed from saved config bytes and kept distinct from store, registry and archive identities.

P11 wording ties every benchmark number to its measured source and raw data, retains sampling/load/logging/retry/closed-loop limitations, and offers source build/demo/log-capture/Docker commands without an unpublished image dependency. Prepared workflow YAML and local helper tests are not successful hosted executions. No production successful-path change or new formal measurement is part of this pass.
