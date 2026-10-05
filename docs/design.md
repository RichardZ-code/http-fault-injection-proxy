# Proposed v0.1.0 behavior contract

Status: P01 was accepted with the final-flush correction. This document defines the accepted target contract and the user-approved temporary P03 startup contract below. Implementation evidence is recorded separately in [progress](progress.md); planned later-phase behavior is not a runtime claim.

## 1. Authority, purpose, and scope

The latest explicit user decisions govern. The HTTP Fault Injection Proxy Codex Build Guide, dated 2026-10-04, selects the v0.1.0 contract and P00-P13 gates. Idea_2.txt, PROJECT 2 and shared claim rules, supplies intent. Guide pages 6-8 define the core behavior; pages 12-27 define later verification and delivery. The [decision record](design-decisions.md) distinguishes source requirements from selected P01 refinements. No material source conflict has been identified. Source attachments are not repository files.

This is a local/CI HTTP failure-testing tool. Required v0.1.0 work comprises `net/http` and `httputil.ReverseProxy`, one fixed startup upstream, strict YAML, first-match rules, delay, every-Nth and seeded probabilistic synthetic 5xx, forwarding deadlines, synchronized state, cancellation, bounded shutdown, separate admin health/metrics, structured logs, four application metric families, fixture upstream, bounded GET retry example, real HTTP/race tests, k6 measurements, Docker, CI, and a reviewed versioned release.

Excluded: production-gateway claims; dashboard, accounts, database, Redis, Kubernetes, distributed coordination; packet manipulation, TCP resets, TLS interception, arbitrary TCP proxying; hot reload, custom tracing stack, plugins; WebSocket/CONNECT; proxy-level application retries; public deployment of the fault service. No users, performance numbers, platform support, or successful releases are claimed at P01.

The accepted Go baseline is 1.27.1. Proposed primary runtime modules are `go.yaml.in/yaml/v4` and `github.com/prometheus/client_golang`. The YAML organization recommends v4 for new projects; its older major lines receive security fixes only. The parser exposes nodes and strict-loading controls suitable for explicit schema validation. The Prometheus client supplies registries, counters, histograms, and exposition. Exact versions and their API/Go compatibility are verified and pinned in P02, not inferred from upstream main or the unrelated P00 UUID download probe. See references in section 13 and decision D01.

## 2. Pipeline and ownership

```text
data handler entry and lifecycle admission
  -> protocol/method/path/query/header validation
  -> complete bounded request-body admission
  -> cancellation check
  -> first rule match and synchronized immutable decision
  -> cancellable delay
  -> selected synthetic status OR forwarding to fixed upstream
  -> checked final flush for a normally returned proxied response
  -> terminal cleanup, one request observation, and structured log

separate admin handler -> local /healthz or /metrics
```

Observation admission means entry into the data handler, including handler-level rejections. Eligible means protocol/input/body validation succeeded and the request context is still live. Only eligible matches allocate a counter/RNG decision. Parser-level failures that never reach the handler are outside application request metrics. A process draining gate rejects new handler admissions with local 503 before body consumption or rule allocation; already admitted handlers may drain.

Configuration is parsed once and becomes immutable. The process owns both listeners, one shared outbound transport, active request contexts, server goroutines, and their cleanup. A request owns its buffer, delay timer, forwarding context, and upstream response body. No rule lock spans I/O. No abstraction is required solely to reproduce this diagram.

Planned locations:

| Location | Responsibility |
| --- | --- |
| `cmd/faultproxy` | CLI, validation orchestration, startup, signals, exit |
| `internal/config` | YAML schema and input/resource validation |
| `internal/fault` | Matching and synchronized per-rule decisions |
| `internal/proxy` | Admission, HTTP forwarding, deadlines, observation hooks, lifecycle |
| `internal/metrics` | Isolated registry and observations |
| `examples` | Standard-library upstream, GET client, scenarios |
| `benchmarks` | k6 workloads, raw trial records, calculations |
| `scripts`, `docs`, `.github/workflows` | Small verification/release helpers, records, CI |

These directories are not created in P01. Split packages only when implemented responsibilities justify it.

## 3. Planned CLI and startup

```sh
faultproxy --upstream http://127.0.0.1:8081 \
  --config examples/scenarios.yaml \
  --listen 127.0.0.1:8080 --admin-listen 127.0.0.1:9090
faultproxy --check-config --config examples/scenarios.yaml \
  --upstream http://127.0.0.1:8081
faultproxy --version
faultproxy --help
```

These commands are proposed interfaces, not runnable P01 demonstrations.

| Value | Environment fallback | Omission behavior |
| --- | --- | --- |
| `--upstream` | `UPSTREAM_URL` | Required |
| `--config` | `CONFIG_PATH` | Required explicit file; no implicit filename |
| `--listen` | `LISTEN_ADDR` | `127.0.0.1:8080` |
| `--admin-listen` | `ADMIN_LISTEN_ADDR` | `127.0.0.1:9090` |

Presence matters: an explicitly supplied flag wins even when empty. An environment variable present with an empty value is also an explicit empty value. Empty/whitespace-only resolved runtime values are invalid; do not fall back or trim them silently. Paths may contain internal spaces. Listener addresses must be numeric IPv4/IPv6 literals with a port in 1-65535 (`[::1]:9090` for IPv6); unspecified IPs are permitted for containers. DNS names for listeners and port zero are rejected. Data/admin resolved endpoints must differ. Non-loopback listeners require explicit selection and do not imply authentication or a public deployment contract.

Use the named long flags shown above; boolean modes accept their standard true/false forms. Reject unknown/repeated flags and positional arguments. At most one true mode among help, version, and config-check is allowed. Help/version ignore missing or invalid runtime environment/configuration and perform no file or network checks, but still reject malformed CLI syntax and conflicting modes. No additional logging/resource flags or broad configuration framework are selected in v0.1.0.

Help prints usage to stdout. Version prints `faultproxy <version> commit=<source-sha-or-unknown> go=<build-go-version>` to stdout; unknown development metadata is explicit. Config-check resolves all runtime values, checks both listener address syntax/distinctness, upstream syntax, config readability/size/schema, and every rule/limit. Success prints `configuration valid\n` to stdout. It performs no DNS lookup, upstream request, listener bind, or runtime-health check. Errors go to stderr with sanitized field/line information, never raw config contents or credential-bearing URLs.

| Exit | Meaning |
| --- | --- |
| 0 | Successful help/version/config-check, or clean graceful stop |
| 2 | CLI/config/upstream/listener validation failure, including unreadable requested config |
| 1 | Listener bind/startup failure, unexpected server/runtime failure, forced shutdown, or cleanup failure |

A missing config file is an error. Explicit `rules: []` is pass-through. Validate all supported configuration before binding. Acquire both listeners before serving; close the first if the second bind fails. Close both if later startup fails. Early P02 entry points reject unavailable config/fault functionality explicitly with exit 1 rather than accepting and ignoring it; full successful config-check remains gated on its implemented validation extent.

### Temporary P03 startup contract

The user approved one strictly validated P03 startup subset:

```yaml
version: 1
rules: []
```

Both fields are required; version is the unquoted decimal integer 1 and rules is an explicit empty sequence. Field order, comments, quoted string keys and flow collections follow the applicable YAML subset below. Reject unknown/duplicate fields, optional `seed`/`upstream_timeout_ms` even at their eventual defaults, nonempty rules, null/coerced values, malformed YAML and multiple documents (including an empty second document). Apply the 1 MiB cap+1 file read, UTF-8, parser depth 8, node-count 10,000, and restricted YAML feature checks. Requested input must be a readable regular file; file/read/close failures reject startup. Diagnostics omit raw parser text, contents and file paths. All option/upstream/listener validation and config admission finish before either listener binds. Validation failures exit 2; bind/runtime failures exit 1.

Valid startup connects to the production fixed-upstream pass-through runtime. Full `--check-config` remains explicitly unavailable with exit 1 after pure option checks, without file certification or listener/network activity. This subset does not apply the future forwarding-timeout default: whole-transfer deadlines, checked final flush and retained finalization deadlines, signal-driven graceful shutdown, and terminal observations remain P05/P06 work. Complete fault configuration and decisions remain P04 work.

P04 must replace this temporary validator with the accepted full schema in section 4, enabling its optional settings and nonempty rules with their contracted effects. Do not preserve a second configuration mode, bypass flag, alternate format or parallel subset validator. The eventual full schema still accepts explicit empty rules; P03's restrictions are a phase boundary, not a second product interface.

## 4. Strict YAML and resources

Planned example, from guide page 7:

```yaml
version: 1
seed: 42
upstream_timeout_ms: 2000
rules:
  - id: payments-unavailable
    method: POST
    path_prefix: /payments
    faults:
      every_nth_request: 3
      status: 503
  - id: inventory-slow
    path_prefix: /inventory
    faults:
      delay_ms: 250
  - id: reports-intermittent
    path_prefix: /reports
    faults:
      probability: 0.20
      status: 500
```

Example values alone do not establish defaults. Defaults below are selected P01 proposals (D02-D04).

### Schema and presence

The root is one mapping with required `version` (integer exactly 1) and `rules` (sequence, including an explicit empty sequence). Optional `seed` defaults to 42; optional `upstream_timeout_ms` defaults to 2000. Each rule requires string `id`, string `path_prefix`, and a `faults` mapping. Optional `method` must be one of the supported exact uppercase methods; omission matches all supported methods. Empty method/prefix/ID is invalid.

Fault fields are optional `delay_ms`, `every_nth_request`, `probability`, and `status`. Delay omission means no delay; zero is accepted as no delay and produces no delay event. A delay-only rule therefore needs a positive delay. Status requires exactly one selector, and either selector requires status. Both selectors together are invalid. `probability: 0` is present, valid, and consumes one draw per match while never selecting status; it is different from absence. Probability 1 always selects. A rule with neither positive delay nor a valid status/selector pair is invalid; empty `faults: {}` is invalid.

No field accepts explicit null. Integers are unquoted nonnegative decimal scalars with no leading plus, leading zeros other than zero, bases, separators, exponent, or float coercion. Numeric overflow is rejected before duration conversion. Probability accepts unquoted decimal integer/float or decimal exponent notation, finite and in [0,1]; reject NaN/infinity, non-decimal forms, signs on a negative value, and numeric strings. String fields require string nodes; no number/bool-to-string coercion. Durations are integer milliseconds, not YAML duration strings.

Reject unknown keys and duplicates recursively before binding typed values, including nested faults. Reject multiple documents, including an empty second document, unsupported schema version, bad root/node types, duplicate IDs, invalid bounds, and incomplete action pairs. Use presence-aware node validation, then conversion to immutable typed values. Do not rely on struct zero values to distinguish absent probability from zero.

YAML subset: UTF-8; ordinary scalar/mapping/sequence nodes; comments and block/flow collections permitted. Reject anchors, aliases, merge keys, explicit/custom tags, directives, complex/non-string keys, and literal/folded scalar styles. Enforce parser nesting bounds and node-count limits as well as schema checks. Do not expand aliases before rejecting them. A bounded streaming decoder must verify EOF after the first document; an option that silently stops after one document or an Unmarshal API that ignores subsequent documents does not establish rejection. Exact pinned parser behavior is tested in P02/P04.

### Resource/default table

All boundaries are inclusive unless stated otherwise. These are fixed v0.1.0 settings except YAML delay/timeout/seed values; no new tuning interface is selected.

| Resource | Selected value/default | Boundary behavior |
| --- | --- | --- |
| Config bytes | 1 MiB = 1,048,576 bytes | Read at most cap+1; larger or empty document fails startup |
| YAML nesting/nodes | Depth 8, 10,000 nodes | Greater fails before typed conversion; parser depth limit also enforced |
| Rules | 0-100 | More fails; zero only by explicit `rules: []` |
| Rule ID | `[a-z][a-z0-9_-]{0,63}` | ASCII 1-64 bytes; unique; literal `none` reserved as no-rule sentinel |
| Path prefix | Starts `/`, 1-256 UTF-8 bytes | No control characters; literal matching, no normalization |
| Delay | Omitted/zero = none; 1-5000 ms = action | Negative, float, overflow, or larger fails |
| Forwarding timeout | Default 2000 ms; 1-10000 ms | Zero/negative/overflow/larger fails; omission alone defaults |
| Seed | Default 42; uint64 0 through 18,446,744,073,709,551,615 | Invalid/overflow fails |
| Rule counter / N | uint64 counter; N in 1 through 18,446,744,073,709,551,615 | No wrap; counter exhaustion produces local 500 without increment/draw/upstream |
| Probability / status | finite [0,1] / integer 500-599 | Out-of-range or invalid pair fails |
| Incoming headers | `MaxHeaderBytes` = 32 KiB; `MaxHeaderValueCount` = 128 | net/http parser limits including request line; not an exact wire-byte allocation promise; parser rejection may bypass handler metrics |
| Request body | 1 MiB accepted bytes | Entirely buffer before decision; cap+1 detection; larger returns 413 |
| Both servers: read-header/read/write/idle | 5 s / 10 s / 30 s / 60 s | Finite deadlines; partial/error outcomes described below |
| Transport dial / TCP keepalive | 2 s / 30 s | Dial timeout is a transport failure unless forwarding context expires first |
| Transport response headers | `ResponseHeaderTimeout` = 0 | Mandatory forwarding context bounds queue, connection, headers, and body together |
| Transport response header bytes | 32 KiB | Excess upstream headers fail forwarding with 502 before commitment |
| Transport pools | MaxConnsPerHost 128; MaxIdleConns and MaxIdleConnsPerHost 128; IdleConnTimeout 60 s | Queued connection acquisition consumes forwarding deadline |
| Transport modes | `Proxy=nil`, `DisableCompression=true`, HTTP/1.1 only | No environment proxying, transparent decompression, or upgrade support |
| Shutdown | One 5 s grace budget, then at most 1 s cleanup/join budget | Force cancellation/close at grace expiry; incomplete cleanup exits 1, never waits indefinitely |
| Retry example | 5 s operation; 1 s attempt; 100/200 ms backoff; 1 or at most 3 attempts | Whole-operation deadline bounds attempts, cleanup, and backoff |

### Body admission

Validate request protocol/method/path/query first. For known Content-Length greater than the cap, return 413 immediately without reading/allocating rule state or contacting upstream. For accepted known length, absent length, or chunked input, read through a bounded reader capped at cap+1 bytes, with the input deadline and context intact. Exactly cap bytes are allowed. Unknown length becomes eligible only after EOF proves completion within the cap. Do not trust an absent/claimed length as proof of size.

Over-cap input returns 413; truncated/malformed input returns 400 while its context is still live; incoming read deadline returns 408 where writing is still possible. Known client or forced-shutdown cancellation takes precedence over these nominal input-error responses, stops reading, and produces the corresponding cancellation outcome without fabricating delivery. Close the input body on every path; do not drain an unlimited rejected body. Rejected oversized/malformed/timed-out body responses close that data connection. No counter increment, RNG draw, fault action, or upstream effect has occurred at these rejections. Check cancellation again after buffering and before allocation.

Forward the accepted bytes using an owned replayable reader; use the accurate Content-Length when there are no outbound trailers, and chunked framing when accepted end-to-end trailers must be emitted. Preserve those trailers except reserved/hop-by-hop metadata, after input EOF. Framing/transfer encoding is controlled by net/http, not byte-for-byte replayed. GET bodies are permitted within the same policy. HEAD requests may carry accepted input but responses remain bodyless. Incoming `Expect: 100-continue` may receive net/http's informational response during reading; it is not final response commitment or an upstream effect.

Buffering uses roughly a cap-sized payload plus bounded-reader/growth/copy overhead per active request. This is not an aggregate memory guarantee or an active-request concurrency cap. Slow streaming uploads and arbitrarily large bodies are outside scope; response bodies are streamed under deadlines without a configured whole-response byte cap.

## 5. HTTP forwarding and admin behavior

### Supported requests

Support exact uppercase GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS over cleartext HTTP/1.1. Reject other methods (including CONNECT/TRACE) with 405 and `Allow: GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS`. Reject non-HTTP/1.1 handler requests with 505; do not enable TLS, HTTP/2, or h2c listeners. Reject protocol-upgrade headers or a case-insensitive `upgrade` token in Connection with 400 before allocation. Reject absolute-form/authority-form targets, OPTIONS `*`, or non-origin-form targets with 400; this is a fixed reverse proxy, not a forward proxy.

The upstream URL must parse as `http`, have a nonempty hostname, a valid numeric port if explicit (1-65535), and no userinfo, query (including a bare `?`), fragment (including a bare `#`), opaque URL, or base path except empty or `/`. DNS upstream names and numeric IPv4/bracketed IPv6 are allowed. Reject invalid escapes, controls, or whitespace. Normalize the accepted root upstream path to empty so it cannot introduce an extra prefix. Upstream DNS/reachability is not startup/config-check validation. Client Host/authority and headers never choose the upstream.

Reject malformed escaped paths, control characters/invalid UTF-8 in decoded paths, or invalid `url.ParseQuery` input (including invalid percent escapes and unescaped semicolon separators) with 400 rather than silently dropping query fields. Use Go's parsed URL.Path and valid RawPath/EscapedPath without a second decode or path cleaning. Preserve valid escaped path and query values, repeated keys, and empty values; retain validated RawQuery through Rewrite. Do not promise preservation of malformed URL input, raw header spelling/order, or framing.

Use one `ReverseProxy` with Rewrite and SetURL, one configured transport, and explicit ErrorHandler/ModifyResponse observation hooks. Outbound Host is the fixed upstream authority. Preserve supported method, accepted bytes, end-to-end headers, upstream final status/body, HEAD/204/304 bodyless semantics, and redirects. ReverseProxy uses RoundTrip and does not add redirect following; integration-test clients disable redirect following to inspect the original response. Disable compression negotiation/decompression by the transport so the upstream encoding stays observable. No application retry loop is added; standard Transport may internally replay eligible requests on a broken reused connection. Controlled healthy-fixture call-count assertions do not claim a universal one-network-attempt guarantee.

### Header and request-ID policy

Ignore client `Forwarded`, all `X-Forwarded-*`, `X-Faultproxy-Injected`, and `X-Faultproxy-Request-ID`, including trailers. Let the standard library strip hop-by-hop headers and Connection-nominated fields. Inside Rewrite, after this stripping, use SetXForwarded to reconstruct only X-Forwarded-For from the direct peer address, X-Forwarded-Host from inbound Host, and X-Forwarded-Proto=`http`. Do not trust a preexisting forwarded chain. Recreate outbound `X-Faultproxy-Request-ID` from proxy-owned state after Connection stripping, so client Connection tokens cannot remove it or the reconstructed forwarding fields. Never copy client forwarding metadata back into outbound headers.

At each data handler entry generate 16 bytes with `crypto/rand`, encoded as 32 lowercase hexadecimal characters, independent of fault RNG. Return `X-Faultproxy-Request-ID` on handler-generated and proxied responses, and send it to the upstream. It is a diagnostic correlation ID, not authentication. Parser-level failures may have no request ID. No UUID module is required.

The sole fault marker is `X-Faultproxy-Injected: status`, present only when the proxy starts its configured synthetic 5xx response. It is absent on real upstream responses, delay-only responses, and local validation/502/504/shutdown errors. Remove upstream lookalikes for both reserved headers and reserved trailer values; replace request ID with the proxy's ID. A delayed upstream 503 is not marked synthetic.

Reserved-header sanitation must cover informational 1xx writes as well as final headers and trailers; ModifyResponse alone cannot sanitize ReverseProxy's separate 1xx path. Strip reserved trailer declarations/values before they are exposed, including late/unannounced reserved trailers. Reject upstream 101 as unsupported with 502 before any final response. Metadata is protected from client forgery within this tool but is not an authenticity/security mechanism.

### Local responses and observation

Generated responses use `text/plain; charset=utf-8`. Fixed bodies: synthetic status `fault injected\n`; 400 `bad request\n`; 405 `method not allowed\n`; 408 `request timeout\n`; 413 `request body too large\n`; 500 `internal error\n`; 502 `bad gateway\n`; 503 draining admission `shutting down\n`; 504 `gateway timeout\n`; 505 `http version not supported\n`. For HEAD, send the same applicable status/headers but no body; do not claim these bytes were delivered merely because WriteHeader was called.

Final commitment is the first final (>=200) WriteHeader, or implicit 200 caused by Write/Flush. Informational 1xx is not final commitment. Store the sent/committed status separately from terminal outcome. For proxied responses, handler-observable completion requires body-copy/close completion and an error-observing final flush while the forwarding context remains active, before terminal cleanup/observation. Successful writes and flushes are not proof the peer consumed the full response. After the handler returns, net/http still performs server finalization, including final chunk framing and trailer writes; those errors are outside handler observations. Completion labels describe only the handler-observable boundary, not verified delivery or successful server finalization.

A small observation wrapper must preserve Flush behavior and `Unwrap() http.ResponseWriter` for ResponseController; do not fake unsupported optional interfaces. Expose `FlushError() error` to delegate to the underlying error-observing flush and record its error; `Flush()` must use that same path. ResponseController prefers a wrapper's Flusher over Unwrap, so Flush/Unwrap alone can hide native flush errors. After ReverseProxy returns normally with a proxied response, explicitly call ResponseController.Flush through this error-observing path and check its result; a missing capability is an internal failure, not successful completion. Track downstream write/flush errors and upstream body-read errors separately. A failed final flush after commitment aborts the response with its causal outcome rather than appending a replacement body. Run terminal cleanup/observation even when ReverseProxy aborts a partial response via `http.ErrAbortHandler`; propagate that abort rather than returning as if a full transfer succeeded. Redirect ReverseProxy/server diagnostic logging through a sanitized adapter, not raw URL/error-string output. No generic middleware framework is needed.

### Admin listener

Only exact `/healthz` and `/metrics` are admin routes. GET/HEAD `/healthz` returns 200, `text/plain; charset=utf-8`, and `ok\n` (no body for HEAD) while serving normally; it makes no upstream call and reveals no config/credentials. It reports local process health, not upstream health. GET/HEAD `/metrics` exposes the isolated registry (HEAD bodyless). Other methods on these known routes return 405 with Allow GET/HEAD; unknown paths return 404, without implicit redirects or path cleaning.

Admin requests never allocate rule counters/RNG state or application data-request metrics/logs. `/healthz` and `/metrics` on the data port are ordinary application paths and can match rules/forward upstream. Admin requests follow the same listener lifecycle and finite server bounds.

## 6. Matching, decisions, and concurrency

Rules are evaluated in YAML order. First matching rule wins even when its status is not selected; never fall through. Compare case-sensitive decoded URL.Path, excluding query, with literal string prefix. `/api` matches `/apix`; `/api/` supplies that boundary. Do not regex-match, normalize, or decode again. Omitted method matches any supported method.

Each eligible match acquires that rule's mutex, checks counter exhaustion, increments its uint64 sequence once, makes the selection, and returns an immutable decision containing rule ID, sequence, delay, and selected status (or none). Counters start at zero; N selects N, 2N, 3N. Delay-only matches also allocate sequence numbers. No-match requests consume no state. A cancelled request observed before allocation consumes none; cancellation after allocation does not roll back state. Request cancellation and allocation can race: the allocated decision is authoritative if allocation completed.

Each probability rule owns `math/rand/v2.Rand` backed by `NewPCG`. Derive seeds from SHA-256 of the bytes `faultproxy-v1\x00`, followed by the configured seed as eight big-endian bytes, followed by `\x00` and the validated ASCII rule ID; each `\x00` denotes one zero byte, not literal backslash text. Take the first two consecutive eight-byte big-endian words as PCG seed1 and seed2. Under the same per-rule mutex call Float64 exactly once per probability match, even at p=0 or p=1, and select when draw < p. N/delay-only rules make no draws. Traffic to other rules never mutates this state; rule identity, not list index, determines the sequence.

Same config/seed and controlled serial match order are repeatable on the pinned Go baseline. No repeatable mapping of faults to concurrent client identities and no cross-Go-version RNG stability is promised. Restart resets counters and RNGs. Counter exhaustion returns local 500/internal_error without wrap, draw, or upstream call; an allocation failure is not a synthetic fault.

Release the mutex before waiting, forwarding, writing, observing metrics, or logging. Delay applies to every match, regardless of status selection. For positive delay, check context then start a cancellable timer/wait and count one started delay action. Stop/release the timer on every path. After wait, recheck context before starting a selected status response or forwarding. Cancellation may consume the selected decision but prevent its action from starting.

Expected, not measured:

- N=3 and 30 eligible serial matches with healthy upstream, no cancellation: 10 synthetic statuses and 20 upstream calls.
- N=5 and 1,000 eligible matches under concurrency: exactly 200 selected statuses, without assigning them to client identities. Started statuses equal selections only if no cancellation/intervening failures prevent them.
- A cancelled delay after allocation consumes its sequence/draw, starts a delay event if waiting began, and makes no upstream call.
- A nonselected first rule never falls through to a later overlapping rule.

## 7. Deadlines, outcomes, and shutdown

### Deadline ownership

Incoming server deadlines govern headers/body admission. A live request context governs body reading, delay, forwarding, and cleanup. The forwarding timeout begins only after delay completes, immediately before outbound queue/connection work. Create a child context with that deadline, retain it through response-body copying/closing and the checked final downstream flush, and cancel it in final cleanup. Receiving upstream headers or EOF does not cancel it. A response-header timer alone cannot enforce a stalled-body deadline.

The server initially sets its 30-second write deadline after incoming headers. At data-handler entry record and set one absolute baseline write deadline of handler-entry time plus 30 seconds through ResponseController; this explicit policy avoids needing an unavailable deadline-getter API. Never extend that baseline during request processing. The maximum nominal body-admission + delay + forwarding budgets are 10+5+10=25 seconds within that write budget; this is budget alignment, not a scheduler/slow-peer timing guarantee. Deadline overlap or a blocked downstream write can end the connection instead of delivering a generated 504. Zero ResponseHeaderTimeout is bounded by the total forwarding context, not unbounded forwarding.

Before committing a proxied response, set an absolute downstream write deadline to the earlier of the recorded baseline and forwarding deadline. Keep it, or any tighter deadline already installed, through body copy, checked final flush, handler cleanup/return, and net/http server finalization. Do not restore the longer baseline or clear the deadline for a committed proxied response. The native HTTP/1.1 server clears it after finishRequest; its post-handler writes therefore remain bounded even after request-context cancellation. A request-owned cancellation/expiry callback may tighten the deadline to interrupt blocked writes/flushes. Keep that callback active through the final flush, then stop and join it before cleanup cancellation and terminal observation, without relaxing the retained deadline. A successful upstream EOF alone does not authorize completion or an indefinitely blocked buffered-tail flush.

For an uncommitted local error response such as a pre-commit 504, stop and join the forwarding callback before attempting the generated response, and restore only the recorded baseline if it remains usable. This exception does not apply after proxied commitment; an expired write deadline or disconnected client can still prevent delivery of the local error. Once the handler has observed its final flush result and finalized cleanup, record terminal metrics/logs. Later server chunk/trailer writes are deadline-bounded but are not included in that observation or retroactively reclassified.

Use causal flags/context causes and commitment state, not error-string matching. For a failing terminal path, precedence at final observation is: recorded forced-shutdown cause; genuine client request cancellation; observed downstream write/flush failure; expired forwarding deadline; incoming body deadline; transport failure; upstream body-read failure; then local validation/internal failure. If the forwarding deadline caused the downstream write/flush failure, classify forwarding expiry rather than unrelated downstream failure, including when the retained absolute deadline fires before its context callback runs. Once terminal completion is finalized it is not retroactively changed by later shutdown. Record cancellation sources before cancelling children during cleanup; cleanup cancellation is not a new failure. If client and force causes overlap before completion, forced shutdown wins when its per-request cause was recorded; otherwise client cancellation wins.

| Event | Response behavior | Terminal outcome / upstream event |
| --- | --- | --- |
| Selected synthetic status starts | Configured 5xx, marker `status`; no upstream call | `synthetic_status`; started status action |
| Real upstream 5xx completes | Preserve status/body; no synthetic marker | `upstream_http_error`; one `http_5xx` event |
| Real non-5xx response completes | Preserve status/body | `upstream_response`; no error event |
| Transport failure before final commitment | Local 502 if still writable | `transport_error`; one `transport` event |
| Forwarding deadline before final commitment | Cancel upstream; local 504 if still writable | `upstream_timeout`; one `timeout` event |
| Deadline/read failure after final commitment, including forwarding expiry during final flush | Abort/truncate; retain sent status; never append an error body or second status | `incomplete_response`; `timeout` or `body_read` event, unless client/downstream/force caused it |
| Client cancellation/disconnect | Stop reading/wait/forward; do not claim a delivered response | `client_cancelled`; no cancellation-derived upstream error event |
| Validation/body-limit rejection | Local 400/405/413/505 as applicable; no allocation/upstream | `rejected`; no fault/upstream event |
| Body read deadline | Local 408 where possible; no allocation/upstream | `request_timeout`; no upstream event |
| New admission during drain | Local 503, no marker/state/upstream | `rejected`; no upstream event |
| Forced shutdown | Cancel active context and close remaining connections | `shutdown_cancelled`; no force-derived upstream error event |
| Downstream write/flush failure | Stop transfer/cancel outbound work; no replacement response | `downstream_error`; no derived upstream event |
| Local allocation/internal failure | Local 500 if uncommitted, otherwise abort | `internal_error`; no synthetic/derived upstream event |

The table describes expected responses, not guaranteed delivery. A failed write overrides a nominal synthetic/502/504 completion with the causal terminal outcome. Post-commit forwarding deadline/body failure is `incomplete_response`, not an apparently delivered 504. Preserve any genuine `http_5xx` event already observed before a later cancellation/transfer failure. Pure upstream body-read errors are distinct from downstream errors even if both surface through an abort.

### Shutdown and failure cleanup

On SIGINT/SIGTERM, atomically close admission for both servers and start concurrent Shutdown calls with one shared 5-second deadline. Do not cancel the active-request base context immediately: existing admissions drain, including body admission/delay/forwarding. Track serve and active-handler completion separately because Shutdown returning/Serve returning does not alone join handlers.

If grace expires, record a forced-shutdown context cause, cancel active request contexts (including blocked input reads via connection closure), call Close on both servers, and close idle outbound connections. Stop request-owned timers, close upstream bodies, remove deadline callbacks, and join tracked work within the additional 1-second cleanup budget. If work does not join, report failure when possible and exit 1 without indefinite waiting. Local unresponsive logging/output can also exhaust cleanup; do not label it clean. This is a bounded process-teardown policy, not proof of leak freedom after forced process exit.

On clean drain, close idle outbound connections and join owned work; exit 0. Expected `http.ErrServerClosed` is normal. Unexpected Serve/listener/runtime failure initiates the same cleanup of both servers and exits 1 even if drain succeeds. Failure of either startup bind cleans up the other listener. No new server is restarted automatically, no arbitrary process is killed, and repeated signals do not create separate full grace budgets. Server.Shutdown alone does not cancel active handlers; forced cancellation is explicit.

## 8. Metrics, logs, and accounting

### Fixed vocabulary

Method buckets: GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS, OTHER. Rule labels: validated configured IDs and reserved `none`; rejected/unmatched requests use `none` unless a rule was actually selected. Outcomes: `upstream_response`, `upstream_http_error`, `synthetic_status`, `transport_error`, `upstream_timeout`, `incomplete_response`, `client_cancelled`, `shutdown_cancelled`, `downstream_error`, `request_timeout`, `rejected`, `internal_error`. Fault kinds: `delay`, `status`. Upstream error kinds: `http_5xx`, `transport`, `timeout`, `body_read`. Log cause kinds: `none`, `validation`, `body_limit`, `input_deadline`, `draining`, `counter_exhausted`, `client`, `shutdown`, `downstream`, `transport`, `forwarding_deadline`, `body_read`, `http_5xx`, `internal`; these are not additional metric labels.

There are exactly four application metric families, created once per process/test registry:

| Family | Type/unit | Observation and labels |
| --- | --- | --- |
| `faultproxy_requests_total` | Counter, requests | Exactly one terminal increment per admitted data handler; labels `method`, `rule`, `outcome` |
| `faultproxy_injected_faults_total` | Counter, started actions | Positive delay wait start and synthetic status response start; labels `rule`, `kind` |
| `faultproxy_upstream_errors_total` | Counter, upstream events | Actual upstream final 500-599 headers, or a classified transport/timeout/body-read event; labels `rule`, `kind` |
| `faultproxy_request_duration_seconds` | Histogram, seconds | One terminal observation from data-handler entry through admission, delay, transfer, checked final proxied flush, and handler cleanup, before observation/log calls; excludes post-handler server finalization; labels `method`, `rule` |

Histogram finite upper buckets (seconds): 0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30; Prometheus adds +Inf, count, and sum. Every handler-observed rejection contributes requests/duration. Parser-level errors, admission before the handler, and all admin traffic do not. At quiescence, histogram count summed across labels equals summed request terminal counts. In-flight handlers have no terminal count yet. Proxied terminal outcomes include the checked final flush result; they do not certify final chunk/trailer writes after handler return or client receipt. No second terminal observation is added for server finalization failures outside the handler.

Count action start, not selection: a positive delay plus selected status can yield two fault events. A cancelled wait may yield only delay; a context already cancelled before wait yields neither. Status begins at the synthetic response's final WriteHeader attempt, independent of whether the client receives it. Events and successful/delivered responses are different quantities.

Each upstream event kind is recorded at most once per request. A real upstream 503 followed by an unrelated body-read failure yields `http_5xx` and `body_read` events but only one terminal `incomplete_response` request. A forwarding deadline yields `timeout`, not an additional `transport` or `body_read` for its resulting error. Cancellation/downstream/force-induced upstream read errors do not fabricate upstream events; an earlier real 5xx event remains. Upstream error totals need not equal failed terminal requests, and action totals need not equal synthetic terminal requests.

Use a fresh `prometheus.Registry` per process/test, with only these four collectors. Do not register default Go/process collectors, per-request collectors, or instrumented promhttp request counters. Expose through HandlerFor with no optional additional collector registration. Bounded registry/exposition failures return admin errors rather than changing data outcomes. Do not label by raw path/URL, query, client address, request ID, arbitrary status text, or error strings.

### Logging

Use standard-library `log/slog` with a JSON handler on stderr, INFO level, one terminal access event for every admitted data request, and sanitized startup/shutdown/error events. No access-log toggle is selected; record this enabled setting and level in benchmarks. Admin access logging is disabled. Configuration errors identify fields/locations without quoting raw data. Never log bodies, Authorization/Cookie headers, query strings, raw URLs/addresses, or raw potentially sensitive transport errors.

Access fields: request ID; method bucket; matched rule (`none` if absent); allocated sequence when present; selected delay/status decision; actual started action kinds; terminal outcome and fixed cause kind; upstream status if observed; sent status if committed (otherwise null); handler duration in seconds. A sent 200 plus `incomplete_response` must remain distinguishable from completion. Do not log random draws or treat status alone as success. Use fixed failure classifications in adapters for ReverseProxy/server diagnostics; do not leak a URL through their default formatted errors. Logs, metrics, and terminal state share the same decision/observation, not three independently inferred outcomes.

Expected accounting illustration for a delay-plus-every-third rule and six eligible serial requests, upstream normally 200, with request 3 cancelled during delay and the other five finishing:

| Quantity | Expected count |
| --- | --- |
| Admitted/allocated requests | 6 / 6 |
| Selected statuses (sequence 3, 6) | 2 |
| Started delay / status actions | 6 / 1 |
| Upstream calls / error events | 4 / 0 |
| Terminal upstream_response / synthetic_status / client_cancelled | 4 / 1 / 1 |
| Client-completed responses in this controlled fixture | 5; inferred independently from client observations |

The histogram has six observations; requests total six; faults total seven action events. Proxy metrics alone do not prove five responses were delivered.

## 9. Retry example

In P07, use a standard-library fixture upstream and GET-only client with one shared client/transport and explicit attempt loops. Mode `none` has one attempt; mode `retry` has at most three total attempts. Each logical operation has a 5-second context deadline and each attempt a child deadline of at most 1 second, including body reading/cleanup. Backoff is 100 ms then 200 ms, exponentially increasing without jitter, cancellable and bounded by the operation context. No proxy application retry loop or blind POST retries.

Retry only 502/503/504, a classified connection/transport error, or per-attempt deadline/body-transfer failure while the operation context remains live. Other HTTP responses are terminal; final success means a completed 2xx response. Operation cancellation/deadline is terminal and never retried. Disable redirect following for the demonstration. Read at most 64 KiB+1 response bytes; a larger body fails locally without retry. Close every body; reuse connections only after bounded complete consumption, and cancel/close a stalled/oversized transfer rather than unbounded draining. Retryable error-response cleanup also consumes attempt time.

Report logical operation ID, physical attempt count, final outcome, deadline exhaustion, and whole-operation duration including backoff and failed attempts. Tests must cover exhaustion, nonretryable responses, attempt/operation deadlines, cancellation during backoff, and body cleanup. Reset state explicitly between benchmark cohorts.

## 10. Verification by phase

This is a behavior-to-phase plan, not a record of executed tests. Use real httptest upstreams plus actual HTTP clients for forwarding; real subprocesses/signals for lifecycle. Synchronize events with channels, barriers, and readiness checks, not tiny sleeps. Bound timing assertions generously, give every operation/subprocess a deadline, and clean up listeners, bodies, timers, processes, and containers on failed assertions as well as success.

| Phase | Behavior and gate evidence |
| --- | --- |
| P01 | Documentation completeness/consistency, links, proposed status, allowed write set; user acceptance and separate focused read-only design review with material findings closed before P02 |
| P02 | Module/tool pins and scaffold; honest unavailable config entry points; help/version/invalid CLI outside checkout; formatting, mod verify, vet, uncached ordinary/race tests, build; native Linux amd64 and macOS arm64 baseline CI on intended commit |
| P03 | Fixed-upstream real pass-through; supported methods/body cap at and beyond boundary including chunked/truncated input; escaped path/repeated/invalid query; end-to-end headers/status/body; outbound Host and forwarded metadata; Connection attacks/reserved metadata/1xx/trailers; HEAD/204/304/redirect; CONNECT/Upgrade/protocol rejection; real 503/unreachable upstream/client cancellation; admin isolation and either-listener bind failure |
| P04 | Strict config types, unknown/duplicate nested keys, extra documents, aliases/tags/bounds/invalid upstream; N=3 exact calls and first-match non-fall-through; concurrent N=5 exact selections; independent counters/RNG, fixed seeds and p=0/1; delay-before-status and cancelled-delay state; restart; focused/full race checks |
| P05 | Full deadlines through upstream body, blocked downstream writes, checked final flush, and server finalization; buffered-tail/slow-reader case below; pre-header 504 versus post-header incomplete transfer; cancellation in body admission/delay/forward/body/flush; process clean drain/forced stop/concurrent shutdown, both listeners, cleanup after failures; 20 uncached repetitions of relevant cancellation/shutdown tests, then full ordinary/race suites |
| P06 | Four-family counts/buckets/labels; rejection/cancellation/5xx/partial-transfer/force accounting; combined actions, same-observation logs, secret/query exclusion; concurrent requests, scrapes, and shutdown under race detector |
| P07 | Retry cases from section 9; native demonstration; Docker non-root/config/startup/networking/stop/restart; missing/bad config and fixture cleanup; label native and emulated checks separately |
| P08 | Focused independent correctness review and material findings closed; complete behavior matrix, recorded-version govulncheck/current advisory check; optional bounded config fuzzing; full ordinary/race/vet/mod/format verification |
| P09 | Frozen harness and source attribution; paired overhead trials, exact correctness cohort, bounded retry cohorts, raw summaries/formulas/limitations as section 11 |
| P10 | Strengthened native CI and Linux container smoke; explicit-source nonpublishing preparation and provenance; preserve failure logs; actual hosted results on intended commit |
| P11 | Fresh-reader build/quick start/demo/tests/Docker checks; documented claims match verified revision and data |
| P12 | Separate read-only final audit of contract, implementation, measurements, CI/source/assets; material corrections verified on resulting revision |
| P13 | Frozen candidate, reviewed source/assets, explicit publication authorization, then public download/native execution/source build and anonymous image pull/smoke verification |

P05 buffered-tail/slow-reader verification: use real HTTP/1.1 connections and a fixed-length upstream response that reaches EOF while final downstream bytes remain buffered. Synchronize the upstream EOF and downstream backpressure, rather than assuming a small response will fill socket buffers. Verify that the handler's final flush observes forwarding expiry, terminates within the deadline plus a bounded scheduling tolerance, preserves the committed status, and records one `incomplete_response` with one `timeout` event; terminal observation must not precede that flush result. Also cover chunked/trailer responses: a successful handler flush must leave a write deadline no later than the forwarding deadline through server finalization, while metrics explicitly exclude later chunk/trailer failures. Observe server/connection completion separately from handler completion, with bounded client reads and cleanup; do not count a handler metric as proof of final framing delivery.

An in-process handler call does not substitute for actual network teardown/body-truncation evidence. Race passing is evidence, not proof of race freedom. Cross-compilation or an emulated Docker run is not native Linux execution. Do not repeat campaigns without a relevant change/new failure, hide initial failures, or add arbitrary coverage/test-count targets.

## 11. Benchmark protocol and provenance

In P09, freeze, review, commit, and push the harness before formal measurement through the separately authorized Desktop workflow. Measure unchanged source with a clean SHA. Use ignored staging while collecting; validate then commit selected raw records separately with the measured SHA. No P01 results or performance claims exist.

Overhead: direct client -> upstream versus client -> explicit `rules: []` proxy -> same upstream; fixed 1 KiB response; same GET path/headers/body policy, keep-alive, logging, metrics, ports, build and environment. Use native release binary without race instrumentation. Constant VUs 1, 25, 100, one request per iteration; three paired repetitions at each level. Alternate direct/proxy order between pairs. Each path has an excluded 5-second warm-up followed by a separate 30-second measurement with fresh measurement statistics, not a percentile contaminated by warm-up. Save actual duration, request count, throughput, p50/p95/p99, checks/unexpected errors, and complete structured k6 summaries per trial. Do not require identical observed request rates in these closed-loop workloads.

Compute each pair's proxied p95 minus direct p95; report the median and range of the three differences with both direct/proxy figures. If throughput ratios are reported, compute each paired ratio before summarizing. Never pool/average percentiles into a fictional combined percentile. Closed-loop constant-VU results are workload observations, not universal overhead, capacity, maximum throughput, availability, or SLA evidence.

Separate deterministic cohort: restart N=5 proxy; send 1,000 physical requests with no retries/cancellation; require 200 synthetic statuses and 800 upstream calls, reconciling client, proxy, and upstream counts. Do not mix it with a time-dependent constant-VU run. Separate retry cohort: 1,000 logical GET operations per mode, three repetitions per mode, restart/reset proxy before every trial; keep operation/attempt/backoff policy fixed. Save final success rate, physical attempts, attempts per operation, whole-operation p95 including failures, and deadline exhaustion. Retries alter the global physical-request fault schedule; they do not receive identically assigned faults per logical operation across modes. Keep fixed-delay and pre/post-header timeout demonstrations separate from overhead measurements.

Record source SHA, config/harness hashes, CPU/architecture, OS, memory, Go/k6 versions, build flags, timestamp, logging level/access setting, metrics settings, keep-alive, and Docker/emulation. Preserve invalid-run reasons and rerun a complete comparable pair when justified, rather than choosing the best run. Historical measurements retain their measured source; successful-path changes require new affected measurements before claims are current. Never copy Runner evidence or relabel old results.

## 12. Packaging, CI, and release boundaries

Planned native binaries: darwin/arm64 and linux/amd64. Planned published container: linux/amd64. Apple Silicon uses the native binary primarily; a documented container path requires explicit linux/amd64 platform selection and tested Docker Desktop emulation. No native arm64 container claim is selected.

P07 plans a multi-stage build, small non-root runtime, exec-form entry point, read-only config mount, and service-name upstream networking in Compose. Bind listeners inside containers to unspecified addresses; publish data/admin ports on host loopback only. Test stop/restart and signal budget; arrange a container stop timeout greater than the 6-second grace-plus-cleanup policy. Docker and k6 setup remain deferred to P07/P09. No Prometheus server/Grafana dashboard is needed to expose metrics.

Baseline native CI starts in P02; P10 strengthens it with full checks and native binary demos, plus Linux release-platform container smoke. Pin actual dependency/tool versions, official Action commit SHAs, and builder/runtime image references in the implementation phases after verifying them. Do not invent P01 pins/hashes. Use read-only default workflow permissions, job timeouts, preserved diagnostics/artifacts with stated retention, and no unnecessary personal token. Hosted YAML validity does not establish hosted execution.

Nonpublishing release preparation accepts an explicit full frozen source SHA and version. It checks out that SHA, runs native checks, prepares darwin/arm64 and linux/amd64 binary archives, a source archive from that commit, and SHA256SUMS for the three archives. Binary archives contain the executable, license, example config, and usage/source metadata; source archive includes committed module metadata. Verify safe archive paths, permissions, version/source output, checksums, and native/source smoke checks. The tested linux/amd64 image archive, image ID, archive checksum, and provenance are preparation artifacts, not an extra public download.

Publication is a separate explicitly authorized operation: verified tag/frozen SHA, repository/package visibility, GitHub release, GHCR image promotion. The publication workflow consumes the selected successful preparation run and verifies repository/source/artifact identities/digests, then loads and promotes its reviewed image instead of rebuilding or choosing an implicit latest run. Use built-in GITHUB_TOKEN and grant packages write only to the publication job. Ordinary push never publishes automatically. Do not overwrite published tags/assets or replace bytes silently; corrections require an explicit versioned plan.

P13 completion requires signed-out public asset access/download, checksum comparison to reviewed bytes, native execution on both claimed platforms, source-archive build/quick start, and anonymous container pull by recorded digest with stop/restart smoke. Keep registry manifest digest, image/config identity, and archive hashes distinct. Record release closure after freeze without moving the tag to a later progress-only commit. No release/platform/CI/performance claim is established by this design.

## 13. Technical references consulted

Consulted 2026-10-04 for design selection; selected release APIs/pins still require P02 verification:

- [YAML organization repository and maintenance policy](https://github.com/yaml/go-yaml), [parser API source](https://github.com/yaml/go-yaml/blob/main/yaml.go), and [node API source](https://github.com/yaml/go-yaml/blob/main/node.go).
- [Prometheus Go client](https://github.com/prometheus/client_golang) and [promhttp source](https://github.com/prometheus/client_golang/blob/main/prometheus/promhttp/http.go).
- Installed Go 1.27.1 source: `net/http/httputil/reverseproxy.go`, `net/http/server.go`, `net/http/transfer.go`, `net/http/responsecontroller.go`, and `math/rand/v2/pcg.go` / `rand.go`. These establish Rewrite ordering, 1xx handling, partial-copy abort behavior, server deadlines/Shutdown limits, trailer framing, Unwrap, and PCG/Float64 APIs. P01 audit correction also verified ReverseProxy copy completion without an unconditional fixed-length final flush, ResponseController's FlushError/Flusher/Unwrap precedence, native FlushError, and finishRequest/chunkWriter.close after ServeHTTP with deadline clearing afterward. Application policies above remain proposals rather than guarantees supplied by these APIs.

Design authority references: guide pages 2-9, 11-27; Idea_2.txt PROJECT 2 and shared scope/evidence rules. The [decision record](design-decisions.md) records proposal provenance, and [progress](progress.md) records actual phase evidence separately.
