# Two-minute client-failure demo

Use the [quick start](../README.md#quick-start) prerequisites and source build. From the repository root:

```sh
export GOWORK=off GOTOOLCHAIN=local CGO_ENABLED=1
mkdir -p bin
go build -o bin/faultproxy ./cmd/faultproxy
go build -o bin/upstream ./examples/upstream
go build -o bin/retry-client ./examples/retry-client
./bin/faultproxy --check-config --upstream=http://127.0.0.1:8081 --config=examples/demo.yaml
python3 scripts/demo.py
```

The script allocates free loopback ports and launches the production binaries and Python capture helper from a clean temporary directory. It uses no private local paths or unpublished image. Each cohort owns its fixture/proxy state. Readiness uses isolated health routes, and data connections remain live through terminal reconciliation before closure.

| Sequence | Expected observable behavior |
| --- | --- |
| Normal response | Completed 200 and fixture body, one upstream call, one upstream_response terminal observation |
| Every-third 503 | Six operations without retries: 200,200,503,200,200,503; four upstream calls; two synthetic_status observations and status actions |
| Bounded retries | Fresh state: six successes with eight application attempts/proxy requests and six upstream calls; at most three total attempts, 100/200 ms backoff, 1 s attempt and 5 s operation budgets |
| Restart | Fresh proxy/fixture reproduces the every-third sequence |
| Fixed delay | /ok waits for the configured 250 ms delay before the upstream response; delay counted separately |
| Real upstream failure | /error returns upstream 503 without an injected marker; http_5xx event stays distinct |
| Forwarding timeout | /slow returns a complete pre-header 504; /partial retains initial 200 with incomplete framing, never a replacement 504 |
| Metrics and cleanup | Request/histogram/action/error counts reconcile; extra scrapes change nothing; upstream waits cancel; all owned children stop/reap and all three ports rebind |

Actual output consists of JSON cohort/accounting summaries and parsed access records, followed by `PASS: production native cohorts; processes reaped and ports rebound; no harness kill`. Across the five small cohorts the script checks 25 parsed access records with no malformed lines. These are real correctness observations, not formal performance samples or a lossless capture guarantee. Every incomplete transfer and cancellation remains distinct from successful handler completion.

To retain actual output, select a fresh directory:

```sh
mkdir -p scratch
python3 scripts/demo.py --output scratch/demo-review
```

Existing output is rejected to preserve captures. Inspect the created directory before removing only that directory. Capture is lossy under contention/backpressure, partial writes, output failure or stalled writer termination; successful proxy exit does not certify complete logging.

For executable cancellation and clean/forced SIGINT/SIGTERM behavior, run the bounded lifecycle checks:

```sh
go test -v -count=1 -timeout=120s ./cmd/faultproxy \
  -run '^TestExecutable(SignalDrainRestart|SignalForced|UndrainedStderrShutdown)$'
```

These tests use ordinary executable children, clean up on failures, and require real process exit/reaping and released ports. A harness kill protects failed teardown but cannot satisfy success. The production drain is five seconds plus at most one second forced cleanup; retained forwarding deadlines and best-effort diagnostics do not extend that budget. Admin readiness is not upstream readiness.

The [Docker recipe](../README.md#docker-local-emulation-verified) builds local linux/amd64 images, uses service-name routing, read-only configuration and loopback-only publication. It was checked locally through Mac emulation. The prepared [Linux CI smoke](../scripts/docker_smoke.py) additionally checks restart, metrics, retry/timeout cohorts, nonroot identity, clean SIGINT/SIGTERM and forced shutdown using only scoped resources. Native Linux execution and committed-image provenance remain hosted gates; public GHCR consumption is later release work.
