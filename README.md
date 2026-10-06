# HTTP Fault Injection Proxy

A Go project for local and CI testing of client behavior under controlled latency, synthetic 5xx responses, and upstream deadlines.

P03 provides tested HTTP pass-through. P04 replaces its temporary configuration subset with the complete strict schema and a working `--check-config`. Startup applies first-match fixed delays and deterministic or seeded synthetic 5xx rules. P05 adds forwarding deadlines, cancellation through blocked I/O, checked final flushing, and bounded signal-driven shutdown. P06 adds bounded Prometheus metrics and structured request logs. P07 adds native fixture/retry examples and container packaging. P07 container checks passed on Docker Desktop using emulated linux/amd64 execution on Apple Silicon; benchmarks and releases remain later phases.

P05 native Linux/macOS CI passed on its implementation commit: Linux executed in attempt 1; macOS acquired a runner and executed in attempt 2 after an infrastructure cancellation. P06 and P07 passed both native hosted jobs on their respective implementation commits. P07's previously recorded CI applies to `f9409004a19ed1e2cf68968b9124ef0bbd9e0392`; its workflow does not test containers. The P08 independent audit of `68fd054b16dd27b9676968e83d3be9dbca5933dd` found no actionable correctness findings or required production corrections; it did not independently reverify hosted CI. See [verification](docs/verification.md) and [progress](docs/progress.md) for attributed evidence and pending container/provenance gates.

## Current HTTP runtime and startup boundary

The internal runtime owns data/admin HTTP/1.1 listeners and one fixed-upstream transport. Wire tests verify supported methods, escaped paths, repeated/empty query values, end-to-end headers, statuses, streamed response bytes, and bodyless HEAD/204/304 responses. Accepted request bodies are fully buffered up to 1 MiB before upstream work; larger bodies return 413 without contacting upstream. Malformed bodies return 400 while their context remains live; input read deadlines return 408 where writing remains possible. Rejected body connections close without waiting to drain further input.

Client forwarding claims are removed; direct-peer forwarding metadata and a generated request ID are recreated after Connection stripping. Reserved upstream fault markers and request IDs are sanitized on final headers, informational responses, and trailers. Real upstream 503 responses retain their status/body without an injected-fault marker. Real transport failures return 502; basic client cancellation propagates upstream. Unsupported methods, target forms, protocols, upgrades, paths, and queries are rejected before forwarding.

Admin GET/HEAD `/healthz` reports local health without probing upstream. Both admin routes bypass fault state. Data `/healthz` and `/metrics` remain application paths that can match rules. Admin GET/HEAD `/metrics` exposes the runtime's private registry; methods other than GET/HEAD on known admin routes return 405, and unknown routes return 404. Both listeners share admission, draining, and cleanup ownership. Admin requests produce no data access records or application observations.

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

SIGINT/SIGTERM stop new admission on both listeners and start concurrent draining under one 5-second deadline. Admitted requests continue within their own limits. At grace expiry, forced causes are recorded, active contexts are cancelled, and both servers/connections close. One additional 1-second budget bounds cleanup and joins of handlers, native connections, serving and shutdown work. Clean drain exits 0; forced stop, genuine serving/close errors, or incomplete joins exit 1. Repeated signals do not create new budgets. A bounded failure exit is not proof of leak freedom before process termination.

Terminal runtime-failure diagnostics are best-effort within time remaining on the existing cleanup deadline. Native pipes, sockets and character devices already configured as nonblocking receive at most one write, capped at 512 bytes. Shared descriptor status flags are never changed. Initially blocking sinks, regular files and arbitrary writers are skipped; expired allowance, full/failed output or a short write can drop or truncate the message. This includes ordinary inherited blocking stderr, even when writable. No writer goroutine or additional wait budget is introduced. Runtime diagnostics use the same supported-sink policy. Final terminal messages contain fixed failure classifications, retaining genuine errors internally. Help/version/config-check and validation output retain their existing command behavior.

## Metrics and structured logs

Scrape the configured admin listener, for example:

```sh
curl http://127.0.0.1:9090/metrics
```

The executable fixture verified this GET route and unchanged accounting across additional scrapes. Each runtime creates its registry and four collectors before binding. There are no default Go/process collectors, scrape counters, exemplars, or global registration. Vectors are lazy: untouched combinations and families are absent, including an empty fresh exposition. Scraping reads in-memory collectors; a slow scrape holds no rule, request, or log lock. Active scrapes are not atomic cross-family snapshots. Reconcile cohorts after terminal observation.

| Family | Type | Exact labels | Counts |
| --- | --- | --- | --- |
| `faultproxy_requests_total` | Counter | `method`, `rule`, `outcome` | One terminal observation per admitted data handler |
| `faultproxy_injected_faults_total` | Counter | `rule`, `kind` | Actual `delay` or synthetic `status` action start |
| `faultproxy_upstream_errors_total` | Counter | `rule`, `kind` | Deduplicated `http_5xx`, `transport`, `timeout`, `body_read` events |
| `faultproxy_request_duration_seconds` | Histogram | `method`, `rule` | One duration in seconds for the same terminal set |

Method values are GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS, OTHER. Rule values are the at most 100 validated IDs plus reserved `none`. Outcomes and cause vocabulary are fixed in the [accounting contract](docs/design.md#fixed-vocabulary). Unsupported internal inputs normalize before collector lookup; unknown action/error kinds are ignored. No path, query, request ID, address, raw error, or arbitrary method becomes a label.

Finite histogram buckets in seconds: 0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30. Including +Inf/count/sum gives 16 series per method/rule. The conservative configured-cap bound is 23,230 series: requests `101*8*12 = 9,696`; actions `101*2 = 202`; errors `101*4 = 404`; histogram `101*8*16 = 12,928`. The full product is not preallocated.

Handler-level validation/body rejection, cancellation, incomplete transfer and forced shutdown receive one request and duration observation. Parser errors and admission rejection before the data handler, including draining rejection, are outside this set. Duration starts at data-handler entry, includes body admission, injected delay, forwarding, checked final proxied flush, and owned body/callback/dial cleanup. The elapsed value is captured before metric updates and log formatting/output; outer lifecycle-owner removal and post-handler server framing are excluded. It does not measure client receipt.

A combined delay/status decision can start two actions. Cancellation during its delay consumes the selected decision but starts no status action. Upstream events are frozen at their causal operation and published with the terminal snapshot, once per kind; actual 503 headers followed by an unrelated body-read failure count both events and one incomplete request. Synthetic responses, client/force cancellation and purely downstream errors do not fabricate upstream errors. An earlier genuine transport event survives a later downstream failure. A checked-flush failure can retain sent status 200 with `incomplete_response`; later handler-invisible chunk/trailer failures produce no invented second observation.

Access logging is enabled at INFO without a toggle, using standard-library slog JSON on stderr. One terminal record is attempted per admitted data handler; admin access logging is disabled. Fields are `time`, `level`, `msg`, `request_id`, `method`, `rule`, optional `sequence`, `decision` (`delay_ms`, nullable selected `status`), `started_actions`, `outcome`, fixed `cause`, nullable `upstream_status`, nullable `sent_status`, and `duration_seconds`. IDs are proxy-generated 32-character lowercase hex, not trusted input. Runtime start/stop events are INFO; sanitized failure events are ERROR. Bodies, credentials, cookies, queries, paths, addresses and raw HTTP/error objects are excluded.

The compatible [P06 sink policy](docs/design-decisions.md#p06-implementation-details-d10) permits only native `*os.File` pipes, sockets or character devices already marked O_NONBLOCK, checked through SyscallConn/Fstat/F_GETFL without flag mutation. Ordinary blocking stderr, regular files and arbitrary writers are skipped even when writable. No proxy CLI/environment/output-file controls are introduced. Use the external launcher below to capture logs into a file.

Each access record is encoded in a private buffer with bounded typed fields and a 1,024-byte limit; oversized/invalid records are dropped whole rather than slicing JSON. A per-runtime TryLock drops contended output, followed by one raw native syscall write with no poll wait, retry, fallback, worker, queue, or log-drain allowance inside the proxy. Full/closed/error sinks drop output; a short native write can leave a partial JSON fragment, which is not a successfully delivered record. No delivery ordering, atomicity across external writers, durability, or lossless output is promised. Metrics remain accurate when logs drop. Runtime diagnostic JSON is capped at 512 bytes; the separate final terminal diagnostic keeps its original 512-byte cap and existing cleanup deadline. No proxy logging work is detached from its owner.

### Runnable log capture

[capture_logs.py](scripts/capture_logs.py) requires Python 3.9+ on POSIX macOS/Linux, using only its standard library. It creates a fresh private capture file (mode 0600, subject to umask) and its own pipes. Only new owned pipe descriptors are configured as nonblocking, before inheritance; inherited stderr/stdout flags are never changed. It continuously drains the proxy's stderr into a bounded nonblocking pipe to an owned file-writer process. A slow/stalled file writer drops chunks instead of blocking the drainer or proxy. There is no unbounded queue.

SIGINT/SIGTERM sent to the launcher are forwarded to the proxy in its separate session. The launcher waits for and reaps the proxy, preserving exits 0/1/2 (signal termination uses the shell convention 128+signal). An early file-writer exit or a capture error requests SIGTERM once and drains/discards remaining output. The resulting proxy status still wins, so exit 0 does not certify capture success. Before child startup, setup failure exits 2 without fallback output to possibly blocked stderr; an existing file is preserved and rejected. After proxy exit, the file writer gets at most 0.5 seconds to finish, then is killed and reaped if stalled. This allowance belongs to the external helper, does not delay proxy shutdown, and can discard its buffered tail.

Run this complete block from the repository root, with ports 8080, 8081 and 9090 free. The demonstration also requires Go 1.27.1 and curl. It retains its private temporary directory for inspection. Production usage replaces the fixture/configuration arguments with your own; foreground launcher invocation also supports Ctrl-C.

```sh
(
  set -e
  GOWORK=off GOTOOLCHAIN=local CGO_ENABLED=1 go build -o bin/faultproxy ./cmd/faultproxy
  capture_dir="$(mktemp -d)"
  printf '%s\n' 'version: 1' 'rules: [{id: thirds, path_prefix: /, faults: {every_nth_request: 3, status: 503}}]' >"$capture_dir/scenario.yaml"
  python3 -m http.server 8081 --bind 127.0.0.1 --directory "$capture_dir" >"$capture_dir/upstream.log" 2>&1 &
  upstream_pid=$!
  capture_pid=
  cleanup() {
    if [ -n "$capture_pid" ]; then
      kill -TERM "$capture_pid" 2>/dev/null || :
      wait "$capture_pid" 2>/dev/null || :
    fi
    kill -TERM "$upstream_pid" 2>/dev/null || :
    wait "$upstream_pid" 2>/dev/null || :
  }
  trap cleanup EXIT
  curl --noproxy '*' -fsS --retry 10 --retry-delay 1 --retry-connrefused --retry-max-time 10 --max-time 1 http://127.0.0.1:8081/ >/dev/null
  python3 scripts/capture_logs.py "$capture_dir/proxy.jsonl" -- ./bin/faultproxy \
    --upstream=http://127.0.0.1:8081 --config="$capture_dir/scenario.yaml" \
    --listen=127.0.0.1:8080 --admin-listen=127.0.0.1:9090 &
  capture_pid=$!
  curl --noproxy '*' -fsS --retry 10 --retry-delay 1 --retry-connrefused --retry-max-time 10 --max-time 1 http://127.0.0.1:9090/healthz >/dev/null
  for i in 1 2 3 4 5 6; do
    curl --noproxy '*' -sS --max-time 5 -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/
  done
  kill -TERM "$capture_pid"
  wait "$capture_pid"
  capture_pid=
  python3 - "$capture_dir/proxy.jsonl" <<'PY'
import collections, json, sys
records, unparsed = [], 0
with open(sys.argv[1], "rb") as capture:
    for line in capture:
        try:
            record = json.loads(line)
        except (ValueError, UnicodeError):
            unparsed += 1
            continue
        if isinstance(record, dict) and record.get("msg") == "request completed":
            records.append(record)
print("parsed access outcomes:", dict(collections.Counter(r["outcome"] for r in records)))
print("unparsed lines:", unparsed)
for record in records[:1]:
    print(json.dumps(record))
PY
  printf 'capture directory: %s\n' "$capture_dir"
)
```

The verified small cohort yielded 200, 200, 503, 200, 200, 503 and six parsed access records (four `upstream_response`, two `synthetic_status`). This is a demonstration, not a lossless guarantee. Proxy contention/backpressure can drop records; native short writes, helper chunk drops/short writes, output failure, or writer termination can leave fragments or lose the tail. Valid JSON alone does not prove a complete capture. Ignore/report malformed lines as above; command-validation/final terminal diagnostics can also be plain text. No fsync/durability or client-receipt claim is made. Keep the directory until inspection, then remove only that owned directory. Capture tests additionally use the POSIX `ps` utility.

Formal benchmarks and k6 remain P09. The following P07 container recipes were verified locally with Docker Desktop. They do not establish native Linux or hosted CI results.

## P07 fixture and bounded GET retry demonstration

Build from the repository root with Go 1.27.1. Python 3.9+ and POSIX ps are additional capture/test tools, not dependencies of either Go example. The fixture and retry code use only the standard library.

```sh
GOWORK=off GOTOOLCHAIN=local CGO_ENABLED=1 go build -o bin/faultproxy ./cmd/faultproxy
GOWORK=off GOTOOLCHAIN=local CGO_ENABLED=1 go build -o bin/upstream ./examples/upstream
GOWORK=off GOTOOLCHAIN=local CGO_ENABLED=1 go build -o bin/retry-client ./examples/retry-client
python3 scripts/demo.py
# To retain files, choose a NEW directory whose parent exists:
mkdir -p scratch
python3 scripts/demo.py --output scratch/p07-native
```

Create `scratch` first for the retained-output command. The default demonstration uses private temporary files and free loopback ports, launches the actual three binaries and reviewed capture helper from outside the checkout, resets both processes between cohorts, reconciles terminal metrics, parses actual safe access records, sends SIGTERM, reaps children and rebinds ports. It removes its default temporary output after parsing. Retained output contains private captures/configs and process diagnostics; remove only the directory you created after inspection. Tests run this same script with ordinary temporary executable builds, including when the Go parent is race-instrumented.

The fixed fixture GET routes are `/ok` (200, `fixture ok\n`), `/error` (genuine 503, `fixture unavailable\n`), `/slow` (wait then 200), and `/partial` (200, chunked `prefix\n`, then abort framing). Bodies use text/plain UTF-8. `/healthz` returns `ok\n`; `/stats` returns JSON calls/active/cancelled. Readiness/statistics, unsupported methods (405), and unknown paths (404) do not consume data counts. Each data-route entry increments calls; active decrements on return; cancelled counts contexts interrupted during waits. No request echo or reset API. Restart resets counts. Default listen is `127.0.0.1:8081`; `--delay=3s` controls only slow/partial waits and accepts 1 ms through 20 s. Containers explicitly bind `0.0.0.0:8081`.

For individual exploration, start `./bin/upstream`, then the proxy with the [demo config](examples/demo.yaml) and its default loopback listeners. Admin readiness never probes upstream; check both services before sending data traffic. Reset the proxy and fixture before EACH comparison cohort. Use [pass-through](examples/pass-through.yaml) to disable faults explicitly.

```sh
./bin/faultproxy --upstream=http://127.0.0.1:8081 --config=examples/demo.yaml
# In another terminal, with fresh proxy/fixture state:
./bin/retry-client --url=http://127.0.0.1:8080/ok --mode=none --operations=6
# Reset both processes, then:
./bin/retry-client --url=http://127.0.0.1:8080/ok --mode=retry --operations=6
curl --noproxy '*' --max-time 2 -fsS http://127.0.0.1:9090/metrics
curl --noproxy '*' --max-time 2 -fsS http://127.0.0.1:8081/stats
```

Defaults follow the accepted contract: 5 s per logical operation, 1 s per attempt including body consumption/close, one attempt in `none`, at most three total in `retry`, cancellable 100/200 ms backoff without jitter. Retry 502/503/504 regardless of synthetic marker, classified connection/transport errors, per-attempt deadline and incomplete body transfer while the operation remains live. Permanent request errors, permanent DNS errors, other HTTP statuses, caller cancellation and whole-operation expiry stop. Redirect following and environment proxies are disabled. Success requires a fully consumed 2xx body; read at most 64 KiB+1, reject larger bodies without retry, close on every path and never perform a second unbounded drain. Started dials inherit the attempt's lifetime despite Transport's detached dial context and are joined before returning.

Each JSON result gives `operation_id`, actual `attempts`, final `outcome`, last-attempt `status` when present, `deadline_exhausted`, whole-operation `duration_seconds` including body cleanup/backoff/failures, and `history` with each attempt's outcome/status/injected evidence. Cleanup cancellation cannot rewrite the captured attempt cause. Exit 0 means all requested operations completed 2xx, 1 means at least one failed/cancelled or result output failed, 2 means invalid options/destination before requests. Defaults: mode none, six serial operations (allowed 1-1000), URL shown above. No concurrency/load harness or formal measurement aggregation is provided.

An application attempt is one `Client.Do` plus response handling. Go 1.27.1 can internally replay an idempotent GET on a reused connection; proxy Transport can also replay outbound work. The counter is not a hard wire-request bound. The controlled cohorts reconcile actual proxy/upstream observations and reject contamination/replay discrepancies. Observed native cohorts:

| Reset cohort | Logical operations | Application attempts / proxy requests | Logical success / failure | Synthetic 503 | Upstream calls |
| --- | --- | --- | --- | --- | --- |
| No retries | 6 | 6 / 6 | 4 / 2 | 2 | 4 |
| At most three attempts | 6 | 8 / 8 | 6 / 0 | 2 | 6 |

No-retry responses were 200,200,503,200,200,503. Fresh restart reproduced them. Metrics had matching terminal/histogram counts, two started status actions and no synthetic upstream error events; additional admin scrapes changed nothing. A separate native cohort observed 250 ms selected delay, genuine upstream 503, pre-header 504 and post-header incomplete 200, with two cancelled upstream waits and no active fixture work. These are correctness observations, not performance or general availability claims. Retries consume more requests and operation time and change the global fault schedule.

## P07 containers (verified on Docker Desktop)

Docker Desktop 4.94.0, CLI/Engine 29.8.2, Compose 5.5.1 and Buildx 0.37.2 were verified on the local `desktop-linux` context. Its Linux ARM64 daemon/build platform ran static linux/amd64 application images through emulation. The pinned Go builder executed Go 1.27.1 linux/arm64; the capture image executed Python 3.14.8. These are Mac-hosted Docker checks, not native Linux or hosted CI. No backend/context/settings change was made; the exact emulation backend was not identified.

The CLI was installed inside Docker.app but absent from the agent's PATH. On macOS, inspect that location before reinstalling. This optional PATH adjustment affects only the current shell and also resolves installed credential helpers:

```sh
if ! command -v docker >/dev/null 2>&1; then
  test -x /Applications/Docker.app/Contents/Resources/bin/docker &&
    export PATH="/Applications/Docker.app/Contents/Resources/bin:$PATH"
fi
```

Read-only preflight before creating resources:

```sh
docker --version
docker compose version
docker buildx version
docker context show
docker context inspect --format '{{.Name}} {{.Endpoints.docker.Host}}'
docker version
docker buildx ls
```

Confirm no unexpected DOCKER_HOST/DOCKER_CONTEXT override and a local Linux Docker Desktop endpoint. Inspect the existing builder's endpoint/platforms; do not select a cloud builder or switch global context. The following commands assume the verified local context is `desktop-linux`; substitute its observed name if different. They require Compose v2 with long bind syntax/create_host_path support, BuildKit/buildx and curl. Every command/subprocess must be bounded during verification. Do not create resources if the observed endpoint is remote or unexpected.

[Dockerfile](Dockerfile) default/final target `proxy` is distroless, runs `/faultproxy` directly as UID/GID 65532:65532. Targets `upstream` and `retry-client` contain just their respective example binaries and license in the same runtime. Optional `capture` adds supported Python 3.14.8 and the unchanged helper, launching `python3 /capture_logs.py` directly as PID 1. All proxy targets copy the same build output. There is no shell supervisor, TTY, Docker socket or alternate sink policy. Plain container stderr may be blocking; `docker logs` is not verified access capture. Capture uses the helper-owned pipe inside the container.

All run/build selections are explicitly linux/amd64. The Go builder executes on BuildKit's build platform and cross-builds with build-local CGO_ENABLED=0, GOWORK=off and GOTOOLCHAIN=local, asserting Go 1.27.1 and module integrity. Native cgo/race settings remain 1. No .git build context/VCS invention: container version identity is unknown; record starting SHA, dirty source hashes and actual image IDs separately. Native Darwin ARM64 and cross-built ELF amd64 are distinct from executed Linux containers. Observed daemon and builder-platform execution were Linux ARM64; all four final images/binaries were linux/amd64 and executed under emulation. Base index/amd64 child digests and sources are in [P07 decisions](docs/design-decisions.md#p07-preparation-decisions).

Verified plain-container recipe from the repository root, ports 8080/8081/9090 free:

```sh
export COMPOSE_PROJECT_NAME="faultproxy-p07-$(date +%s)-$$"
docker --context desktop-linux compose -f compose.yaml config
docker --context desktop-linux compose -f compose.yaml build
docker --context desktop-linux build --platform=linux/amd64 --target=retry-client -t faultproxy-retry:p07-local .
docker --context desktop-linux compose -f compose.yaml up -d
# Independently wait up to ten seconds for BOTH endpoints; no data readiness traffic:
curl --noproxy '*' --retry 10 --retry-delay 1 --retry-connrefused --retry-max-time 10 --max-time 1 -fsS http://127.0.0.1:8081/healthz
curl --noproxy '*' --retry 10 --retry-delay 1 --retry-connrefused --retry-max-time 10 --max-time 1 -fsS http://127.0.0.1:9090/healthz
docker --context desktop-linux run --rm --platform=linux/amd64 --network="${COMPOSE_PROJECT_NAME}_default" faultproxy-retry:p07-local --url=http://proxy:8080/ok --mode=none --operations=6
# Expected client exit 1 because operations 3/6 receive synthetic 503.
curl --noproxy '*' --max-time 2 -fsS http://127.0.0.1:9090/metrics
curl --noproxy '*' --max-time 2 -fsS http://127.0.0.1:8081/stats
docker --context desktop-linux compose -f compose.yaml stop proxy
docker --context desktop-linux inspect --format '{{json .State}}' "${COMPOSE_PROJECT_NAME}-proxy-1"
# Inspect application exit and OOM/kill evidence before removal; CLI exit is separate.
docker --context desktop-linux compose -f compose.yaml down
```

Use a fresh/reset Compose cohort for retry mode, pass-through (`SCENARIO_FILE=./examples/pass-through.yaml`), delay and partial/timeout checks. Service-name upstream is `http://upstream:8081`. Both proxy listeners bind inside containers to 0.0.0.0; all three published host ports bind to 127.0.0.1. The explicit YAML bind is read-only with create_host_path false, so missing source files cannot silently become directories. There is no implicit configuration, readiness health redefinition, auto-restart policy or released registry tag. Stop proxy while upstream remains alive. External 10 s stop allowance covers its existing 5+1 s policy, helper's separate 0.5 s allowance and orchestration margin; it does not change those internal bounds. Inspect nonroot process identity, effective mounts/ports/config hash and state before cleanup; a missing id/shell in distroless proves nothing about UID.

Verified capture override, starting from no running cohort:

```sh
mkdir scratch-capture
chmod 0700 scratch-capture
test "$(id -u)" -ne 0
export CAPTURE_UID="$(id -u)" CAPTURE_GID="$(id -g)"
export CAPTURE_DIR=./scratch-capture CAPTURE_FILE="cohort-$(date +%s)-$$.jsonl"
docker --context desktop-linux compose -f compose.yaml -f compose.capture.yaml config
docker --context desktop-linux compose -f compose.yaml -f compose.capture.yaml build
docker --context desktop-linux compose -f compose.yaml -f compose.capture.yaml up -d
# Wait for both readiness endpoints as above, then send the controlled cohort.
docker --context desktop-linux run --rm --platform=linux/amd64 --network="${COMPOSE_PROJECT_NAME}_default" faultproxy-retry:p07-local --url=http://proxy:8080/ok --mode=none --operations=6
# Expected client exit 1; inspect metrics/stats as in the plain recipe.
docker --context desktop-linux exec "${COMPOSE_PROJECT_NAME}-proxy-1" python3 --version
docker --context desktop-linux exec "${COMPOSE_PROJECT_NAME}-proxy-1" python3 -c 'import os; print(os.getuid(), os.getgid())'
# Stop proxy first; inspect its helper exit/state before down:
docker --context desktop-linux compose -f compose.yaml -f compose.capture.yaml stop proxy
docker --context desktop-linux inspect --format '{{json .State}}' "${COMPOSE_PROJECT_NAME}-proxy-1"
python3 - "$CAPTURE_DIR/$CAPTURE_FILE" <<'PY'
import json, sys
with open(sys.argv[1], 'rb') as stream:
    for line in stream:
        try:
            record = json.loads(line)
        except (ValueError, UnicodeError):
            print('unparsed fragment')
            continue
        if record.get('msg') == 'request completed':
            print(json.dumps(record))
PY
docker --context desktop-linux compose -f compose.yaml -f compose.capture.yaml down
```

This maps the capture process to the nonzero host UID/GID owning this new, ignored directory so mode-0600 files are host-readable on Linux. It overrides the capture image's default 65532:65532; plain proxy/fixture/client remain 65532:65532. On the verified Docker Desktop setup, UID 501/GID 20 processes created host-readable mode-0600 files in the owned output mount. Recheck sharing/ownership on another host; YAML alone does not establish it. Do not change permissions on existing directories with sensitive files. Choose a fresh exclusive filename for EVERY recreate/start: after stopping, change CAPTURE_FILE and use `up -d --force-recreate`; `start` cannot change an existing container's command. Existing capture files are rejected. Remove only this project's containers/network and owned output after inspection, never prune caches. Local image names above are intentionally retained development artifacts if built, not published releases.

Observed plain and capture cohorts matched the native table: six no-retry operations made six proxy requests/four upstream calls; six retry operations made eight/six. Empty-rule pass-through, fresh state, delay, real upstream 503, pre-header 504, post-header incomplete 200, exact metric reconciliation and admin isolation passed. Five capture cohorts produced 25 parsed access records, no malformed lines and no privacy-canary leaks. Actual process identity, loopback publication and service-name routing passed; a config write attempt failed EROFS. Missing/bad config and missing/unwritable/existing capture paths failed without orphan application resources. Admin remained healthy before a delayed/unavailable fixture; forwarding returned 502 until service recovery.

SIGINT clean stop and SIGTERM active drain/force passed for both entry points. Deliberately forced requests used a 10,000 ms forwarding config with the fixture held for 20 s and alive through cleanup. Plain/capture forced application/helper exits were 1 after approximately 5.42 s; active 3 s requests drained with exit 0. State/events showed no OOM or Docker SIGKILL. The helper preserved proxy exits 0/1/2. A SIGSTOP-stalled writer held 64,637 bytes in its 65,536-byte queue during a bounded 300-request correctness probe; output stayed undrained, proxy reaping preceded writer cleanup, and helper exit 0 released ports. Its unchanged separate writer allowance remains at most 0.5 s after proxy reaping. The helper may kill/reap its own stalled writer; that is distinct from Docker forcibly killing the application.

A real 1,024-byte file-size-limit failure also stopped capture normally, preserving proxy/helper exit 0 while leaving incomplete output. Capture remains lossy. Forced cleanup may add the plain `faultproxy: shutdown grace expired` terminal diagnostic; report that separately from parsed access records and unparsed fragments. Handler completion does not certify client receipt or post-handler framing. Native Linux container execution, committed-source image provenance and hosted Docker checks remain pending; exact dirty-source image/binary identities and historical emulation evidence are in [progress](docs/progress.md#p07-docker-continuation-results). P08 inspected that evidence and the existing images without rebuilding or executing containers; see [verification](docs/verification.md#evidence-boundaries-and-remaining-gates).

## Development

Use Go 1.27.1 and a native C compiler for cgo/race tests (Apple Command Line Tools on macOS). These commands use process-local settings; they do not change global Go configuration:

```sh
export GOWORK=off GOTOOLCHAIN=local CGO_ENABLED=1
test -z "$(gofmt -l cmd/faultproxy/*.go internal/config/*.go internal/fault/*.go internal/metrics/*.go internal/logging/*.go internal/proxy/*.go examples/upstream/*.go examples/retry-client/*.go)"
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
go test -v -run '^Test(Executable(Startup|InvalidConfigBeforeBind|BindFailure|ConfigCheck|FaultRestart|DeadlineTransfer|SignalDrainRestart|SignalForced|ObservabilityDemonstration|UndrainedStderrShutdown)|Capture)' -count=1 -timeout=120s ./cmd/faultproxy
```

Only the documented long flags are accepted. Flags override matching environment values even when explicitly empty; empty runtime values are errors. Help/version require no config/upstream and ignore invalid runtime values, while malformed CLI syntax still fails with exit 2. Run/config-check invocations require explicit `--upstream` and `--config` values. Both actions validate the complete schema; config-check returns before runtime construction. Version output reports `dev`, the actual build VCS revision with `+dirty` when applicable (or `unknown`), and the build Go version. No release version is claimed.

The pinned YAML prerelease is used for bounded streaming node validation of the complete schema and its compatibility probe. The pinned Prometheus client now provides the four runtime collectors and uninstrumented private-registry exposition. See the decision record for exact pins and the YAML prerelease limitation.

- [Accepted behavior contract](docs/design.md)
- [Design decisions and tradeoffs](docs/design-decisions.md)
- [Phase progress and evidence status](docs/progress.md)
