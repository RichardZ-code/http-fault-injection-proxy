# HTTP Fault Injection Proxy

A Go project for local and CI testing of client behavior under controlled latency, synthetic 5xx responses, and upstream deadlines.

P01 design acceptance and its focused independent review are closed. P02 currently provides a CLI scaffold: help, development version identity, flag/environment resolution, and pure option validation. HTTP execution and full scenario validation return exit 1 with explicit "not implemented yet" errors. No listener is started and no configuration file is certified. Examples, fault handling, application metrics, containers, benchmarks, and releases remain future work.

Local checks passed on native macOS ARM64 with Go 1.27.1. The baseline Linux amd64/macOS ARM64 workflow is authored; hosted execution is pending. No passing CI or release claim is made.

## Development

Use Go 1.27.1 and a native C compiler for cgo/race tests (Apple Command Line Tools on macOS). These commands use process-local settings; they do not change global Go configuration:

```sh
export GOWORK=off GOTOOLCHAIN=local CGO_ENABLED=1
test -z "$(gofmt -l cmd/faultproxy/*.go internal/config/*.go internal/metrics/*.go)"
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

Tests include bounded executable subprocess checks from an unrelated temporary directory. The subprocess binary is an ordinary build, even in the race suite; race instrumentation covers the tested in-process scaffold and compatibility probes.

Only the documented long flags are accepted. Flags override matching environment values even when explicitly empty; empty runtime values are errors. Help/version require no config/upstream and ignore invalid runtime values, while malformed CLI syntax still fails with exit 2. Well-formed run/config-check invocations require explicit `--upstream` and `--config` values, then fail as unavailable with exit 1. Path readability and strict scenario validation remain unimplemented. Version output reports `dev`, the actual build VCS revision with `+dirty` when applicable (or `unknown`), and the build Go version. No release version is claimed.

YAML/Prometheus imports currently exist only in compatibility tests: node decoding/EOF and isolated in-memory exposition of a test-only metric. These do not establish configuration-schema or application-observability correctness. See the decision record for exact pins and the YAML prerelease limitation.

- [Accepted behavior contract](docs/design.md)
- [Design decisions and tradeoffs](docs/design-decisions.md)
- [Phase progress and evidence status](docs/progress.md)
