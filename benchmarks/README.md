# P09 benchmark method

Preparation only. Formal measurements have not started. Review and commit the harness through GitHub Desktop, verify that exact commit's native CI, and obtain separate authorization before invoking `formal`. A successful smoke does not advance this gate. The [accepted protocol](../docs/design.md#11-benchmark-protocol-and-provenance) and [P09 decisions](../docs/design-decisions.md#p09-preparation-choices) explain source authority and routine choices. Historical [P08 verification](../docs/verification.md) is separate evidence.

## Tools and setup

Application builds use Go 1.27.1, native cgo=1 and a native C compiler. Helpers require Python 3.9+ and standard POSIX tools. The selected formal host/runtime is native macOS ARM64 with Python 3.9.6. Pure tests also run on the existing Linux/macOS CI without k6 or Docker. k6 is separately built with Go 1.26.8; this is not the application compiler.

Selected tool: [official k6 v2.3.0](https://github.com/grafana/k6/releases/tag/v2.3.0), stable and the current official latest release when preparation began. No installed k6 was present. Homebrew was already available, and the [official macOS installation route](https://grafana.com/docs/k6/latest/set-up/install-k6/) is `brew install k6`; that unversioned command does not pin the bytes required by this protocol. The task chose the official standalone archive to avoid changing a shared installation. No Homebrew installation, PATH, security/quarantine or global tool setting was changed.

Runnable setup from the repository root, outside the checkout:

```sh
TOOL_DIR="$(mktemp -d)"
curl -fL --connect-timeout 10 --max-time 120 \
  -o "$TOOL_DIR/k6-v2.3.0-macos-arm64.zip" \
  https://github.com/grafana/k6/releases/download/v2.3.0/k6-v2.3.0-macos-arm64.zip
curl -fL --connect-timeout 10 --max-time 30 \
  -o "$TOOL_DIR/checksums.txt" \
  https://github.com/grafana/k6/releases/download/v2.3.0/k6-v2.3.0-checksums.txt
printf '%s  %s\n' b2417a3038edc5fe81dc178a889237724b595c5c9cfed875822008e46e862c7d \
  "$TOOL_DIR/k6-v2.3.0-macos-arm64.zip" | shasum -a 256 -c -
# Also compare the official checksum list; retain it with the downloaded archive.
grep '^b2417a3038edc5fe81dc178a889237724b595c5c9cfed875822008e46e862c7d' "$TOOL_DIR/checksums.txt"
unzip -q "$TOOL_DIR/k6-v2.3.0-macos-arm64.zip" -d "$TOOL_DIR"
K6_BIN="$TOOL_DIR/k6-v2.3.0-macos-arm64/k6"
chmod u+x "$K6_BIN"
file "$K6_BIN"
"$K6_BIN" version
printf '%s  %s\n' efb8282e24ffe18f54ce3679eaa71d72e0645bbb97b14e2a05cc8b192abf48e3 \
  "$K6_BIN" | shasum -a 256 -c -
```

The archive hash was independently matched to the official release API's digest and checksum file. This is checksum/source verification, not an independent signature audit. Expected version output: `k6 v2.3.0 (commit/e088784614, go1.26.8, darwin/arm64)`. Leave any password/security prompt to the operator; do not remove quarantine or change security settings. Formal mode requires these exact executable bytes, exact version, native ARM64 and Python 3.9.6. A different installation needs a reviewed protocol update first.

## Preparation commands

These commands do not invoke formal measurement:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 benchmarks/harness.py plan
PYTHONDONTWRITEBYTECODE=1 python3 benchmarks/harness_test.py
GOWORK=off GOTOOLCHAIN=local CGO_ENABLED=1 \
  go test -v -count=1 -timeout=120s ./examples/upstream ./cmd/faultproxy \
  -run 'Test(BenchmarkFixture|BenchmarkHarness)$'
RUN_PARENT="$(mktemp -d)"
PYTHONDONTWRITEBYTECODE=1 python3 benchmarks/harness.py smoke \
  --k6 "$K6_BIN" --output "$RUN_PARENT/smoke"
PYTHONDONTWRITEBYTECODE=1 python3 benchmarks/harness.py summarize \
  --dataset "$RUN_PARENT/smoke"
# Deliberate failure must exit 1 and leave an invalid manifest and raw evidence.
PYTHONDONTWRITEBYTECODE=1 python3 benchmarks/harness.py smoke \
  --bad-check --k6 "$K6_BIN" --output "$RUN_PARENT/failed-check"
# Both commands below must fail; neither promotes smoke evidence to formal.
PYTHONDONTWRITEBYTECODE=1 python3 benchmarks/harness.py summarize \
  --dataset "$RUN_PARENT/failed-check"
PYTHONDONTWRITEBYTECODE=1 python3 benchmarks/harness.py summarize \
  --dataset "$RUN_PARENT/smoke" --require-formal
```

Smoke: one 1-VU direct/proxy pair, 500 ms excluded warm-up and 500 ms measured window per path; 20 serial N=5 physical requests; ten logical operations in each retry mode, one repetition per mode; three low-load requests per zero/fixed-delay condition; one pre-header and one post-header timeout. This profile is reduced and labeled smoke throughout. k6 requires at least a one-second total constant-VU scenario. The smoke additionally executes deterministic JS phase-boundary checks before traffic. There are no formal 1,000-operation correctness/retry runs in this command.

Source SHA, dirty status and hashes of tracked/untracked inputs identify the patch tested. No ignored `bin` executable is consumed. Build outputs, configurations copied from source, logs, raw/derived JSON and scratch files are created in a new external directory. Existing output and output resolving inside the checkout, including symlinks, are rejected. Generated Python bytecode is disabled. All default tests can execute without installed k6; `TestBenchmarkHarness` invokes the calculation/ownership checks in both native Go suites.

## HTTP workload and observability

The narrow fixture extension `/benchmark` returns status 200, exactly 1,024 bytes (`0123456789abcdef` repeated 64 times), `Content-Length: 1024` and `text/plain; charset=utf-8`. The payload is built once at startup. Atomic fixture counters exclude `/healthz` and `/stats`. Each pair checks actual bytes on both paths before timed work and records the extra calls separately in outside-window snapshots. Readiness uses upstream `/healthz` and isolated admin `/healthz`, never the measured data route. The configured upstream authority, rather than the inbound client Host, is used on forwarded requests.

One [workload](workload.js) serves both targets: synchronous bodyless GET `/benchmark`, Accept text/plain, Accept-Encoding identity, X-Benchmark v1; response status, exact ASCII bytes, content length/type and HTTP/1.1 checks. Redirects and batches are disabled; request timeout is 2 s, keep-alive and per-VU connection reuse enabled. No sleep, arrival-rate pacing, hidden application retry or parallel requests within an iteration. k6 may manage its transport internally; unexpected counts/checks/errors invalidate overhead. The Python fixed-count cohort reuses one HTTPConnection without automatic retries or redirects. The existing Go retry client uses one reusable transport and can internally replay an eligible GET; admitted proxy traffic and fixture counts are measured separately from application attempts.

Empty rules use [empty.yaml](empty.yaml) and production `--check-config` before startup. Standard ordinary native builds retain the full production proxy, validation, metrics, deadlines, final flush and shutdown path. No race/coverage/debug flags, benchmark-only proxy, release metadata injection or optimization is used. Actual Go build information and binary hashes are recorded.

Baseline logging remains enabled INFO access logging without a toggle. Record construction, capability checks and attempts still occur. Its initially blocking regular-file stderr is unsupported and skipped by the accepted sink policy; skipping emission does not disable logging cost. No flags on inherited descriptors are modified. No capture helper runs in the baseline. The fixture emits no access logs in either path. Metrics remain enabled; health/stats/reconciliation scrapes occur outside timed windows, never continuously during load. Metrics are handler observations, not proof of client receipt; body checks supply the client evidence. The [P05/P06 policy](../README.md#metrics-and-structured-logs) remains unchanged, including causal failure capture, retained finalization deadline and lossy logging.

## Formal plan and timing

Generated schedule, not executed during preparation:

| Pair | VUs | Order |
| --- | --- | --- |
| v1-r1 | 1 | direct, proxy |
| v1-r2 | 1 | proxy, direct |
| v1-r3 | 1 | direct, proxy |
| v25-r1 | 25 | proxy, direct |
| v25-r2 | 25 | direct, proxy |
| v25-r3 | 25 | proxy, direct |
| v100-r1 | 100 | direct, proxy |
| v100-r2 | 100 | proxy, direct |
| v100-r3 | 100 | direct, proxy |

Nine pairs, eighteen measured path trials. Fresh fixture and proxy per pair; both remain running across its members on one campaign-fixed three-port map. Each path gets a new k6 invocation with continuous constant VUs for 5 s warm-up plus 30 s measurement. Connections are warmed within that invocation and retained across its warm-up/measurement boundary. They are recreated for the other path. Direct and proxy load never run simultaneously.

Common origin is `exec.scenario.startTime`. Request membership is assigned at synchronous request start using the shared millisecond wall clock, before HTTP I/O: elapsed < 5,000 ms is warm-up; [5,000, 35,000) ms is measured; later starts are tail. A warm-up request finishing after 5,000 ms remains excluded. A measured request finishing after 35,000 ms remains included. No per-VU first-request origin or percentile subtraction is used. Date clock resolution is milliseconds; clock anomalies or invalid boundaries fail validation. Counter increments before/after HTTP detect interrupted work, while tagged custom duration trends isolate measured samples. Warm-up and tail checks remain correctness gates but are excluded from reported measured checks/counts/latencies.

All three start-phase counter populations, including explicit zero tail counters, must reconcile: phase starts equal phase completions, and warm-up + measured + tail-start counts equal global starts, completions, iterations and HTTP requests. The completion tail is elapsed drain time, not another request population. A measured-start request finishing after the window is counted once in the measured phase. The workload exports tail submetrics explicitly; older preparation summaries lacking them are historical evidence and do not satisfy this corrected validator.

Explicit gracefulStop is 3 s, longer than the 2 s request bound; interrupted or missing iterations invalidate the pair. The timing record keeps configured warm-up/window, first measured start, last measured finish, completion tail beyond the end boundary, k6 actual execution duration and orchestrator wall duration separately. Throughput is completed requests whose starts belong to the measured cohort divided by the fixed 30 s start-membership window (0.5 s in smoke). Completion tail is reported separately and is not added to that denominator. The first/last observation span is not mislabeled as the denominator. Startup, warm-up, summary serialization and teardown do not enter it.

Latency is the k6 `http_req_duration` value copied from each response into the measured-only trend, in milliseconds: sending + waiting + receiving, excluding initial DNS, connection establishment and blocked connection time. It is not TTFB, connection-inclusive operation latency, iteration time, proxy handler duration or retry-operation duration. See [metric definitions](https://grafana.com/docs/k6/latest/using-k6/metrics/reference/), [constant VUs](https://grafana.com/docs/k6/latest/using-k6/scenarios/executors/constant-vus/), [graceful stop](https://grafana.com/docs/k6/latest/using-k6/scenarios/concepts/graceful-stop/) and [closed-loop semantics](https://grafana.com/docs/k6/latest/using-k6/scenarios/concepts/open-vs-closed/).

The selected 2.3.0 legacy-compatible `handleSummary(data)` object is saved whole as JSON, using explicit `--new-machine-readable-summary=false`; no rounded console parsing or remote JS imports. p(50), p(95), p(99), min/max are explicit. Built-in metrics retain whole-run checks/failures/counts; phase counters/trends supply isolated populations, clock extrema and completion checks. Counter `rate` values use whole-run execution time and are not used as measured throughput. Complete raw files remain unchanged; normalized/derived values are separate. The harness pins options in the script and passes only its explicit workload variables; CLI settings override configuration/environment where applicable. Process environment excludes inherited K6_* settings, proxy variables and Go flags, uses an empty task-owned k6 config, disables usage reporting, automatic extensions, dashboard and trace output, and does not enable a REST address, remote outputs or any cloud command. See [custom summary](https://grafana.com/docs/k6/latest/results-output/end-of-test/custom-summary/) and [checks](https://grafana.com/docs/k6/latest/using-k6/checks/). Checks alone do not cause a nonzero k6 exit; correctness thresholds and explicit raw validation both gate acceptance. No hardware-sensitive percentile/rate threshold is selected.

## Separate cohorts and calculations

Formal N=5 correctness: fresh [nth.yaml](nth.yaml) process, exactly 1,000 serial physical requests without retries/cancellation. Client status/marker/body hashes, 200 synthetic actions, 800 upstream calls, 1,000 terminal/duration observations and zero upstream errors must reconcile. Unexpected transfer/cancellation/replay/extra traffic invalidates it. This is not the unpredictable total of a time-limited constant-VU test.

Retry: fresh proxy/fixture for every trial, three repetitions per mode, 1,000 serial logical GET operations per trial. Use the unchanged example CLI's none=1 or retry=at most 3 attempts, 5 s operation/1 s attempt including cleanup, cancellable 100/200 ms backoff, no jitter/redirects, 64 KiB body bound. Raw stdout is one JSON record per operation with ID, attempts, final status/outcome, deadline exhaustion, duration_seconds and attempt history. Exit 1 for valid unsuccessful operations is data; setup/output/transport/accounting failures remain invalid execution. Synthetic 503 responses are intended, counted separately from unexpected errors. All outcomes enter success denominators and whole-operation quantiles. Missing records invalidate rather than shrinking the sample.

Before calculating successes or quantiles, validate the attempt-history transitions. Success must end in a completed 200 attempt; status and outcome must agree with the terminal attempt. Earlier attempts must be retryable failures, and a non-deadline failed operation must exhaust its mode's attempt allowance. Operation expiry can override the last attempt's outcome, end during backoff, or precede the first attempt; its status retains the last observed status (absent/zero before any attempt) and deadline_exhausted is true. Attempt expiry may preserve already received 200/503 headers. These failed operations remain in the denominator and latency population; contradictory histories are invalid data.

Report each retry trial's success rate, physical requests, physical attempts/logical operation, application attempts, upstream calls, synthetic actions, final failures, deadline exhaustions and p95 operation latency in seconds. Raw durations include backoff and failed operations. Quantile convention: Hyndman-Fan type 7, sorted durations, h=(n-1)*0.95, linear interpolation between floor/ceil indices; n=1 returns its only value. This independently specified calculation is not claimed to match k6's quantile algorithm. Do not pool repetition percentiles. The physical every-N schedule changes fault assignment when clients retry; modes are not paired identical-fault logical operations and do not prove general availability improvement.

Separate controls: three serial zero-delay and three 50 ms delay requests with actual elapsed observations/action reconciliation, no hardware-sensitive latency assertion; a 200 ms forwarding configuration against the fixture's 3 s wait produces unmarked pre-header 504 or initial 200 plus incomplete chunked transfer on `/partial`, never a replacement 504. The delayed rule matches only `/benchmark`, so slow/partial routes independently test forwarding expiry. Accounting waits for quiescent terminal/histogram publication and idle upstream work; extra admin traffic does not consume state.

For overhead pair i, calculate `proxy_p95_i - direct_p95_i` at full precision, retaining negative values. At each VU report all three differences, their median and min/max range, with both paths' count/window/rate/p50/p95/p99/check/error/tail/raw references. This is not `median(proxy_p95) - median(direct_p95)`. Throughput ratios use each pair's proxy rate / positive direct rate, then a median of ratios. Never pool/average percentile summaries into an overall percentile. Round presentation only.

## Raw schema, validity and cleanup

Versioned `manifest.json`: schema=1, protocol=p09-v1, kind=smoke/formal, full plan, expected/actual SHA, dirty state, committed byte/mode match, input hashes, build commands/information/hashes, exact application/k6/Python/compiler versions, machine OS/architecture/CPU/core/memory/power information (unknown with reason if unavailable), native execution, UTC timestamp, fixed observability/request conditions, port map, process commands/PIDs/exits, per-trial order/attempt, raw references, derived fields, initial/final identities, file hashes, validity and first error plus cleanup errors. No serial-number/username query, full environment dump or credential collection is performed. Battery identifiers are omitted; command/build/error metadata replaces checkout and home prefixes with `${SOURCE_ROOT}` and `${HOME}` before serialization. Resolve SOURCE_ROOT to the measured checkout and HOME to the local home when reproducing those recorded arguments; byte hashes and relative input/raw references are preserved. Inspect raw tool diagnostics before any import because failure messages can include local paths. Raw summaries, physical requests, retry JSON lines, copied configurations, stdout/stderr and binaries are external and hashed. `derived.json` is generated only after complete validation and clearly labels smoke as unsuitable for performance claims.

The validator rejects wrong plans, duplicate/missing/reordered pairs or paths, missing/malformed/nonfinite quantiles, bad counts/units/durations, interrupted work/check/HTTP errors, missing/truncated/changed raw files, mismatched config copies/binaries/source/tool/conditions, missing retry records, invalid controls and raw/derived mismatches. Every dataset declaring kind=formal receives formal source/runtime/build validation, including plain summarize; --require-formal additionally refuses smoke datasets. Merely renaming a directory or changing its display label cannot satisfy the formal plan/source guards. Invalid manifests remain diagnostic evidence, not partial formal result tables.

Fixed-count collection persists framing-complete, body-limit-admitted response records to correctness/requests.json on both success and failure, including unexpected statuses/body hashes with ok=false. Completion requires all declared Content-Length bytes, the terminal chunk plus a terminated trailer section, or EOF for a close-delimited response. A short bounded read alone is not completion. Close-delimited EOF cannot distinguish an early close from an intended end; the separate expected-body check still rejects missing/wrong fixture bytes. Completed fixed-length responses release their response handle for connection reuse.

Body reads are individually bounded and retain at most 65,537 bytes: the 64 KiB limit plus one oversized sentinel byte. Oversized responses are rejected before record admission, without reading the rest. Chunked trailers require CRLF termination and are limited to 64 KiB and 100 lines including the final blank line. The existing two-second socket timeout and cancellation/owned cleanup remain effective. The adjacent collection.json records planned/completed counts, first failure/request identity, cancellation, received status/marker and connection-cleanup errors. For an uncompleted/unadmitted response, failure metadata separately records framing, declared_content_length (null when not applicable), partial_body_bytes and partial_body_sha256 for the bounded bytes actually observed. Those bytes may be only a prefix, even if the server sent more. Timeout/cancellation retains previously observed bytes; it does not invent bytes still buffered elsewhere. No such response enters completed_responses, and no records are invented for unattempted requests. Failure/cancellation keeps the dataset invalid, prevents derived reporting, and preserves these raw files/hashes through owned cleanup. Storage failure can prevent persistence and is reported alongside the original failure.

An invalid overhead path invalidates its whole pair. Failed attempt identity and available members are retained. There are no automatic retries or allow-dirty formal escape. Stop for review; a later separately authorized new complete campaign can replace an invalid campaign under the same reviewed conditions. Retain the failed campaign with its reason, do not splice a good half into another attempt, choose fastest trials or discard a noisy but valid number. Review all valid results, including unfavorable ones. Any cleanup failure invalidates collection; a harness kill is failed teardown protection, never passing application behavior.

Only owned process sessions are signalled. Proxy terminates before upstream so its existing drain budget remains effective; each stop has an 8 s bound, then owned kill/reap protection and an invalid cleanup record. Build/tool commands also use owned sessions: their existing timeout or interruption kills the owned group and allows at most 3 s for pipe collection/direct-child reaping, preserving the original failure. A surviving tool group after ordinary completion is killed and rejected. Independently owned resources are all attempted, children reaped, HTTP connections/files closed, all three ports rebound, first substantive errors preserved alongside cleanup errors. Repeated signals add no budget; interruption unwinds the same cleanup. Port selection releases task-owned reservations immediately before launch; a competing owner winning that race causes failure, never an unrelated kill. Keep useful external evidence until reviewed, then remove only directories created for this task. No Docker resource or global cache cleanup is performed.

## Later formal continuation, not executed

After preparation review, user Desktop commit/push, exact-revision native CI verification and explicit collection authorization:

```sh
# Required operator input from the reviewed/pushed harness commit, not invented here.
: "${MEASURED_SHA:?set the verified full harness commit SHA}"
: "${K6_BIN:?set the verified standalone k6 executable path}"
# No-traffic source/plan guard before creating runtime output.
PYTHONDONTWRITEBYTECODE=1 python3 benchmarks/harness.py plan --expected-sha "$MEASURED_SHA"
FORMAL_PARENT="$(mktemp -d)"
PYTHONDONTWRITEBYTECODE=1 python3 benchmarks/harness.py formal \
  --expected-sha "$MEASURED_SHA" --k6 "$K6_BIN" \
  --conditions 'AC power; awake; no concurrent builds/race/Docker/heavy work; record any disturbances' \
  --output "$FORMAL_PARENT/dataset"
PYTHONDONTWRITEBYTECODE=1 python3 benchmarks/harness.py summarize \
  --require-formal --dataset "$FORMAL_PARENT/dataset" >"$FORMAL_PARENT/recalculated.json"
```

The condition text is an operator attestation, not a programmatic proof of human review or an idle machine. Keep the Mac plugged in/awake and record real disturbances. A scoped operator-owned `caffeinate` can accompany collection, with its PID/reaping documented; the harness does not modify global power settings or stop unrelated jobs. Client, fixture and proxy share this native machine's resources, so the client or fixture can limit results.

Formal guards check expected 40-character SHA, clean staged/unstaged/untracked state, actual source bytes/executable modes against committed blobs even if Git status flags hide modifications, committed required inputs, exact native tool versions and an external new path before traffic. Input/source/tool/binary identities are checked at builds/trial boundaries and completion. Never edit source, upgrade tools or write progress mid-campaign. Any drift stops/invalidates collection. All output remains external until finished; this supersedes the design's older ignored in-checkout staging wording under the explicit P09 request.

After complete validation, independently inspect/recalculate raw records and every outcome. Only after that separate review, import the complete selected dataset and derived table into a new `benchmarks/results/<dataset-name>` for another review. For example, with NEW_DATASET_NAME chosen and no existing destination:

```sh
: "${NEW_DATASET_NAME:?choose a new reviewed dataset directory name}"
case "$NEW_DATASET_NAME" in ''|*[!a-zA-Z0-9_-]*) exit 2;; esac
mkdir -p benchmarks/results
test ! -e "benchmarks/results/$NEW_DATASET_NAME"
cp -R "$FORMAL_PARENT/dataset" "benchmarks/results/$NEW_DATASET_NAME"
```

This copies the complete validated external dataset; it is not authorized during preparation. Retain invalid-attempt history and link any previous failed campaign in the reviewed results record. Runtime artifacts can be bulky; review the complete import and private runtime paths before the separately authorized results commit, retaining raw evidence/identity and removing no adverse measurements. Attribute records to MEASURED_SHA, not the later documentation/results commit. No broad benchmark-results ignore rule is introduced. The user commits results separately through Desktop. A successful-path source change requires affected remeasurement; an unrelated change can retain explicitly historical results only with a justified source comparison. Never relabel old measurements.

Constant-VU traffic is closed-loop: achieved rate varies with latency. Local comparisons do not establish maximum capacity, fixed-arrival-rate SLA or universal overhead. Three pairs are the prescribed comparison, not a broad statistical guarantee. No formal CI performance threshold, container axis, remote output or published performance claim is added. Native Linux containers, committed-image provenance and hosted Docker checks remain pending and are not established by native Go checks or Mac timings.
