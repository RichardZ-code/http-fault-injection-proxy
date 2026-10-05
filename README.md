# HTTP Fault Injection Proxy

A Go project for local and CI testing of client behavior under controlled latency, synthetic 5xx responses, and upstream deadlines.

P03 provides tested HTTP pass-through. P04 replaces its temporary configuration subset with the complete strict schema and a working `--check-config`. Startup applies first-match fixed delays and deterministic or seeded synthetic 5xx rules. P05 adds forwarding deadlines, cancellation through blocked I/O, checked final flushing, and bounded signal-driven shutdown. Application metrics/access logs, containers, benchmarks, and releases remain later-phase work.

P04 native Linux/macOS CI passed on its implementation commit. P05 local verification uses Go 1.27.1 on native macOS ARM64; hosted P05 verification remains pending until user review and commit/push. See [progress](docs/progress.md) for exact identities, results, and review gates.

## Current HTTP runtime and startup boundary

The internal runtime owns data/admin HTTP/1.1 listeners and one fixed-upstream transport. Wire tests verify supported methods, escaped paths, repeated/empty query values, end-to-end headers, statuses, streamed response bytes, and bodyless HEAD/204/304 responses. Accepted request bodies are fully buffered up to 1 MiB before upstream work; larger bodies return 413 without contacting upstream. Malformed bodies return 400 while their context remains live; input read deadlines return 408 where writing remains possible. Rejected body connections close without waiting to drain further input.

Client forwarding claims are removed; direct-peer forwarding metadata and a generated request ID are recreated after Connection stripping. Reserved upstream fault markers and request IDs are sanitized on final headers, informational responses, and trailers. Real upstream 503 responses retain their status/body without an injected-fault marker. Real transport failures return 502; basic client cancellation propagates upstream. Unsupported methods, target forms, protocols, upgrades, paths, and queries are rejected before forwarding.

Admin GET/HEAD `/healthz` reports local health without probing upstream. Both admin routes bypass fault state. Data `/healthz` and `/metrics` remain application paths that can match rules. Admin GET/HEAD `/metrics` currently returns 503 with an explicit unavailable message; methods other than GET/HEAD on known admin routes return 405, and unknown routes return 404. Both listeners share admission, draining, and cleanup ownership. P06 application metrics and structured access logs remain unavailable.

An empty ruleset is ordinary pass-through under the full schema:

```yaml
version: 1
rules: []
```

Normal startup and config-check use the same loader. Both required fields, all rules, and pure upstream/listener syntax are validated before binding. Config-check prints `configuration valid` with a newline and exits 0 without creating rule state, binding, resolving/contacting upstream, or rewriting the input. Invalid options/configuration exit 2 with bounded stderr diagnostics; bind/runtime/forced-shutdown/cleanup failures exit 1. Help/version and clean graceful stop exit 0.

The [accepted schema](docs/design.md#4-strict-yaml-and-resources) defines the contract. Input is a readable regular UTF-8 file (optional leading UTF-8 BOM), at most 1 MiB, with exactly one document. Parser depth 8 and validator depth/node limits (8/10,000) supplement the byte cap; the post-parse node guard does not bound every parser allocation. Rules are limited to 100. Unknown/duplicate fields, duplicate IDs, nulls, coercions, multiple documents, aliases/anchors/merges/tags/directives/block scalars, and non-string keys are rejected recursively.

Seed defaults to 42 (uint64); `upstream_timeout_ms` defaults to 2000 and accepts 1-10000. Its single forwarding budget begins after body admission, decision allocation, injected delay, and the final cancellation check. It covers connection acquisition/dialing, sending, response headers/body, downstream transfer, and the checked final flush. A delay can exceed that duration without consuming it; selected synthetic responses never start a forwarding budget. Delay omission/zero means no delay; a positive delay is 1-5000 ms. Status is 500-599 with exactly one uint64 every-N selector (N >= 1) or finite probability in [0,1]. Probability zero is present and consumes a draw; probability one always selects. Delay-only rules need positive delay. Integers require unquoted decimal syntax; probabilities also permit decimal floats/exponents. IDs, supported methods, path prefixes, and remaining bounds follow the linked schema.

Rules match in YAML order by supported method and literal case-sensitive prefix of Go's decoded `URL.Path`, without query matching or another decode. The first match wins even when status is not selected; `/api` also matches `/apix`. Complete successful body admission and cancellation checks precede allocation. Each rule owns a synchronized uint64 counter and, for probability, a PCG stream derived from SHA-256 of the configured seed and rule ID. A probability match consumes exactly one `Float64` draw. Serial matches reproduce the seeded sequence after restart; concurrent client identities depend on allocation order. Exhausted counters fail locally without wrapping or forwarding.

Locks are released before cancellable delay or I/O. Delay precedes status/forwarding even for nonselection. Cancellation after allocation consumes its count/draw and may leave the selected action unperformed. Synthetic responses carry `X-Faultproxy-Injected: status`, text/plain UTF-8, and `fault injected\n` (HEAD is bodyless); they never contact upstream. Real upstream 503 and transport 502 remain unmarked. Metadata is diagnostic, not authentication or proof of client receipt.

Validate the [working example](examples/scenarios.yaml), start a local HTTP upstream, then run:

```sh
./bin/faultproxy --upstream http://unresolved.invalid --config examples/scenarios.yaml --check-config
./bin/faultproxy --upstream http://127.0.0.1:8081 --config examples/scenarios.yaml
```

There is no separate subset mode or bypass. Successful config-check certifies schema validity. A transport failure before final commitment generates an unmarked 502; forwarding expiry generates an unmarked 504 while the original write baseline remains usable. Informational 1xx responses do not commit a final status. After commitment, timeout/read/write/flush failure aborts the transfer, retaining the initial status without appending an error body. Client cancellation interrupts owned reads, delay, dialing and transfer; an allocated fault decision remains consumed.

Transport-failure causality is captured before diagnostics: a failure observed with a live forwarding context stays a transport failure if output later crosses the forwarding deadline. Failure to install the local error's write bound, write its body, or flush it aborts as a downstream failure while retaining the original upstream cause.

Normally copied proxied responses require an error-observing final flush with the forwarding context still active. Cleanup stops and joins cancellation callbacks before cancelling their contexts. Committed responses retain the earlier of the absolute forwarding and baseline write deadlines through later server finalization. Successful handler-observable completion does not certify subsequent chunk/trailer writes or client consumption. Tests separately exercise an ordinary fixed-length buffered tail, blocked body writes, post-handler framing, and subsequent keep-alive reuse.

Both servers use 5-second read-header, 10-second read, 30-second write and 60-second idle limits, with 32 KiB headers and at most 128 header values. The data handler records one absolute 30-second write baseline; it never extends that baseline during processing. Transport dial timeout is 2 seconds, response headers are capped at 32 KiB, and pools permit 128 connections per host. These bounds support finite HTTP transfers, not arbitrary streaming or unlimited slow readers. Earlier input/transport/baseline limits or a disconnected client can prevent delivery of a generated 504.

SIGINT/SIGTERM stop new admission on both listeners and start concurrent draining under one 5-second deadline. Admitted requests continue within their own limits. At grace expiry, forced causes are recorded, active contexts are cancelled, and both servers/connections close. One additional 1-second budget bounds cleanup and joins of handlers, native connections, serving and shutdown work. Clean drain exits 0; forced stop, genuine serving/close errors, or incomplete joins exit 1. Repeated signals do not create new budgets. A bounded failure exit is not proof of leak freedom before process termination. Application metrics/access logs remain P06.

Terminal runtime-failure diagnostics are best-effort within time remaining on the existing cleanup deadline. Native pipes, sockets and character devices already configured as nonblocking receive at most one write, capped at 512 bytes. Shared descriptor status flags are never changed. Initially blocking sinks, regular files and arbitrary writers are skipped; expired allowance, full/failed output or a short write can drop or truncate the message. This includes ordinary inherited blocking stderr, even when writable. No writer goroutine or additional wait budget is introduced. A blocked earlier runtime diagnostic can exhaust cleanup and require exit 1 without a final message.

## Development

Use Go 1.27.1 and a native C compiler for cgo/race tests (Apple Command Line Tools on macOS). These commands use process-local settings; they do not change global Go configuration:

```sh
export GOWORK=off GOTOOLCHAIN=local CGO_ENABLED=1
test -z "$(gofmt -l cmd/faultproxy/*.go internal/config/*.go internal/fault/*.go internal/metrics/*.go internal/proxy/*.go)"
go mod tidy -diff
go mod verify
go vet ./...
go test -count=1 -timeout=120s ./...
go test -race -count=1 -timeout=120s ./...
go build -o bin/faultproxy ./cmd/faultproxy
git diff --check
./bin/faultproxy --help
./bin/faultproxy --version
```

Tests include real upstreams and both production runtime listeners, plus bounded executable subprocess checks from an unrelated temporary directory. The subprocess binary is an ordinary build, even in the race suite; race instrumentation covers tested in-process code. To see public config-check, forwarding, fault/restart, deadline/partial-transfer, signal drain/restart/force, and rejection-before-bind checks:

```sh
go test -v -run '^TestExecutable(Startup|InvalidConfigBeforeBind|BindFailure|ConfigCheck|FaultRestart|DeadlineTransfer|SignalDrainRestart|SignalForced)$' -count=1 -timeout=120s ./cmd/faultproxy
```

Only the documented long flags are accepted. Flags override matching environment values even when explicitly empty; empty runtime values are errors. Help/version require no config/upstream and ignore invalid runtime values, while malformed CLI syntax still fails with exit 2. Run/config-check invocations require explicit `--upstream` and `--config` values. Both actions validate the complete schema; config-check returns before runtime construction. Version output reports `dev`, the actual build VCS revision with `+dirty` when applicable (or `unknown`), and the build Go version. No release version is claimed.

The pinned YAML prerelease is used for bounded streaming node validation of the complete schema and its compatibility probe. Prometheus remains test-only, exercising isolated in-memory exposition; application observability is not implemented. See the decision record for exact pins and the YAML prerelease limitation.

- [Accepted behavior contract](docs/design.md)
- [Design decisions and tradeoffs](docs/design-decisions.md)
- [Phase progress and evidence status](docs/progress.md)
