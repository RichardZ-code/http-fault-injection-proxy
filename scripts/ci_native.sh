#!/usr/bin/env bash
# Shared nonpublishing checks. Output stays outside the source checkout.
set -euo pipefail
out=${1:?new external evidence directory}
mkdir -p "$out"
exec > >(tee "$out/native.log") 2>&1
: "${EXPECT_OS:?}" "${EXPECT_ARCH:?}"
export GOWORK=off GOTOOLCHAIN=local CGO_ENABLED=1 PYTHONDONTWRITEBYTECODE=1
export GOFLAGS=
go version
go env GOHOSTOS GOHOSTARCH GOOS GOARCH CGO_ENABLED CC GOWORK GOTOOLCHAIN GOFLAGS
python3 --version
python3 -c 'import sys; assert sys.version_info >= (3,9)'
test "$(go env GOVERSION)" = go1.27.1
for field in GOHOSTOS GOOS; do test "$(go env "$field")" = "$EXPECT_OS"; done
for field in GOHOSTARCH GOARCH; do test "$(go env "$field")" = "$EXPECT_ARCH"; done
test "$(go env CGO_ENABLED)" = 1
case "$EXPECT_OS/$EXPECT_ARCH" in
  linux/amd64) expected_runner_os=Linux; expected_runner_arch=X64 ;;
  darwin/arm64) expected_runner_os=macOS; expected_runner_arch=ARM64 ;;
  *) echo 'unsupported native matrix'; exit 1 ;;
esac
if [ "${GITHUB_ACTIONS:-false}" = true ]; then
  printf 'Runner: %s/%s\n' "$RUNNER_OS" "$RUNNER_ARCH"
  test "$RUNNER_OS" = "$expected_runner_os"
  test "$RUNNER_ARCH" = "$expected_runner_arch"
fi
compiler=$(go env CC)
"$compiler" --version
unformatted=$(git ls-files -z '*.go' | xargs -0 gofmt -l)
test -z "$unformatted" || { printf '%s\n' "$unformatted"; exit 1; }
go mod tidy -diff
go mod verify
go vet ./...
python3 scripts/release_test.py
python3 scripts/evidence_test.py
python3 scripts/preparation_test.py
go test -v -count=1 -timeout=120s ./... | tee "$out/ordinary.log"
go test -v -race -count=1 -timeout=120s ./... | tee "$out/race.log"
mkdir -p "$out/bin"
go build -o "$out/bin/faultproxy" ./cmd/faultproxy
go build -o "$out/bin/upstream" ./examples/upstream
go build -o "$out/bin/retry-client" ./examples/retry-client
python3 scripts/demo.py --proxy "$out/bin/faultproxy" --upstream "$out/bin/upstream" \
  --client "$out/bin/retry-client" --output "$out/demo" | tee "$out/demo.log"
python3 scripts/evidence.py benchmarks/results/2026-10-06-macos-arm64-0da4d1f43560
git diff --exit-code -- go.mod go.sum
git diff --check
