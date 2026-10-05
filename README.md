# HTTP Fault Injection Proxy

A Go project for local and CI testing of client behavior under controlled latency, synthetic 5xx responses, and upstream deadlines.

P01 design acceptance and its focused independent review are closed. P02 provides the CLI scaffold: help, development version identity, flag/environment resolution, and pure option validation. P03 connects the public CLI to the tested production HTTP pass-through runtime after strict validation of the approved temporary no-fault config subset. Full config-check remains explicitly unavailable with exit 1 and starts no listener. Fault handling, application metrics, containers, benchmarks, and releases remain future work.

Local P03 runtime/startup checks passed on native macOS ARM64 with Go 1.27.1. Historical P02 native Linux/macOS CI passed on its implementation commit; it does not verify this uncommitted P03 patch. See the progress record for source identities and limitations. P03 hosted verification is pending.

## Current HTTP runtime and startup boundary

The internal runtime owns data/admin HTTP/1.1 listeners and one fixed-upstream transport. Wire tests verify supported methods, escaped paths, repeated/empty query values, end-to-end headers, statuses, streamed response bytes, and bodyless HEAD/204/304 responses. Accepted request bodies are fully buffered up to 1 MiB before upstream work; larger bodies return 413 without contacting upstream. Malformed bodies return 400 while their context remains live; input read deadlines return 408 where writing remains possible. Rejected body connections close without waiting to drain further input.

Client forwarding claims are removed; direct-peer forwarding metadata and a generated request ID are recreated after Connection stripping. Reserved upstream fault markers and request IDs are sanitized on final headers, informational responses, and trailers. Real upstream 503 responses retain their status/body without an injected-fault marker. Real transport failures return 502; basic client cancellation propagates upstream. Unsupported methods, target forms, protocols, upgrades, paths, and queries are rejected before forwarding.

Admin GET/HEAD `/healthz` reports local health without probing upstream. Data `/healthz` and `/metrics` remain upstream application paths. Admin GET/HEAD `/metrics` currently returns 503 with an explicit unavailable message; methods other than GET/HEAD on known admin routes return 405, and unknown routes return 404. The runtime has immediate resource cleanup and sibling-server failure cleanup. It does not implement P05 graceful signals, full forwarding deadlines, checked final flush/retained finalization deadlines, or P06 completion observations.

The user-approved temporary P03 configuration is:

```yaml
version: 1
rules: []
```

Both fields are required. Before either listener binds, startup validates options/upstream/listener addresses and a readable regular config file: 1 MiB cap+1 read, UTF-8, exactly one document, parser depth 8, node count 10,000, restricted YAML features, exact version type/value, and empty rules. Unknown/duplicate fields, optional seed/timeout fields, nonempty rules, malformed input and extra documents fail with exit 2. Bind/runtime failures exit 1. No requested config is ignored; diagnostics omit raw YAML contents and file paths.

Save those two fields as `scenario.yaml`, start a local HTTP upstream, then run:

```sh
./bin/faultproxy --upstream http://127.0.0.1:8081 --config scenario.yaml
```

P04 replaces the temporary validator with the accepted full schema. There will be no separate subset mode or bypass. P03 has no whole-transfer forwarding deadline, checked final flush/retained finalization deadline, or graceful signal handling. Ordinary process termination is not a verified graceful drain.

## Development

Use Go 1.27.1 and a native C compiler for cgo/race tests (Apple Command Line Tools on macOS). These commands use process-local settings; they do not change global Go configuration:

```sh
export GOWORK=off GOTOOLCHAIN=local CGO_ENABLED=1
test -z "$(gofmt -l cmd/faultproxy/*.go internal/config/*.go internal/metrics/*.go internal/proxy/*.go)"
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

Tests include real upstreams and both production runtime listeners, plus bounded executable subprocess checks from an unrelated temporary directory. The subprocess binary is an ordinary build, even in the race suite; race instrumentation covers tested in-process code. To see the public CLI forwarding demonstration and rejection-before-bind checks:

```sh
go test -v -run '^TestExecutable(Startup|InvalidConfigBeforeBind|BindFailure)$' -count=1 -timeout=120s ./cmd/faultproxy
```

Only the documented long flags are accepted. Flags override matching environment values even when explicitly empty; empty runtime values are errors. Help/version require no config/upstream and ignore invalid runtime values, while malformed CLI syntax still fails with exit 2. Run/config-check invocations require explicit `--upstream` and `--config` values. Run validates the temporary subset before startup; config-check remains unavailable with exit 1 after pure option checks and does not certify even a subset file. Complete scenario validation remains P04 work. Version output reports `dev`, the actual build VCS revision with `+dirty` when applicable (or `unknown`), and the build Go version. No release version is claimed.

The pinned YAML prerelease is used for bounded streaming node validation of the temporary subset and its compatibility probe. Prometheus remains test-only, exercising isolated in-memory exposition; application observability is not implemented. See the decision record for exact pins and the YAML prerelease limitation.

- [Accepted behavior contract](docs/design.md)
- [Design decisions and tradeoffs](docs/design-decisions.md)
- [Phase progress and evidence status](docs/progress.md)
