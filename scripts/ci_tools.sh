#!/usr/bin/env bash
# Install verified tools only into the supplied task-owned directory.
set -euo pipefail
out=${1:?external evidence directory}
mkdir -p "$out/tools"
exec > >(tee "$out/tools.log") 2>&1
export GOBIN="$out/tools" GOTOOLCHAIN=local GOWORK=off
export GOFLAGS=
go install golang.org/x/vuln/cmd/govulncheck@v1.8.0
"$GOBIN/govulncheck" -version | tee "$out/govulncheck-version.txt"
"$GOBIN/govulncheck" -json -test ./... >"$out/govulncheck-native.json" 2>"$out/govulncheck-native.stderr"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 "$GOBIN/govulncheck" -json ./... >"$out/govulncheck-linux-static.json" 2>"$out/govulncheck-linux-static.stderr"
python3 - "$out" <<'PY'
import json, pathlib, sys
for name in ('govulncheck-native.json', 'govulncheck-linux-static.json'):
    text = (pathlib.Path(sys.argv[1]) / name).read_text()
    decoder, position, findings, objects = json.JSONDecoder(), 0, 0, 0
    while text[position:].strip():
        position += len(text[position:]) - len(text[position:].lstrip())
        record, position = decoder.raw_decode(text, position)
        objects += 1
        findings += 'finding' in record
    assert objects, 'missing scanner output'
    print(name, 'objects', objects, 'finding records', findings)
    assert findings == 0, 'vulnerability findings require review'
PY
case "$(go env GOHOSTOS)/$(go env GOHOSTARCH)" in
  linux/amd64)
    k6_asset=k6-v2.3.0-linux-amd64.tar.gz
    k6_hash=39c3117b6af817592dcd0ce4242105c0a7af10948c2a425306f0be8f7a8a8ab1
    lint_asset=actionlint_1.7.12_linux_amd64.tar.gz
    lint_hash=8aca8db96f1b94770f1b0d72b6dddcb1ebb8123cb3712530b08cc387b349a3d8
    ;;
  darwin/arm64)
    k6_asset=k6-v2.3.0-macos-arm64.zip
    k6_hash=b2417a3038edc5fe81dc178a889237724b595c5c9cfed875822008e46e862c7d
    lint_asset=actionlint_1.7.12_darwin_arm64.tar.gz
    lint_hash=aba9ced2dee8d27fecca3dc7feb1a7f9a52caefa1eb46f3271ea66b6e0e6953f
    ;;
  *) echo 'unsupported native tools platform'; exit 1 ;;
esac
curl -fL --connect-timeout 10 --max-time 120 -o "$out/tools/$k6_asset" "https://github.com/grafana/k6/releases/download/v2.3.0/$k6_asset"
curl -fL --connect-timeout 10 --max-time 120 -o "$out/tools/$lint_asset" "https://github.com/rhysd/actionlint/releases/download/v1.7.12/$lint_asset"
python3 - "$out/tools" "$k6_asset" "$k6_hash" "$lint_asset" "$lint_hash" <<'PY'
import hashlib, pathlib, sys, tarfile, zipfile
root = pathlib.Path(sys.argv[1])
for name, expected in zip(sys.argv[2::2], sys.argv[3::2]):
    path = root / name
    assert hashlib.sha256(path.read_bytes()).hexdigest() == expected, name
    if name.endswith('.zip'):
        with zipfile.ZipFile(path) as archive:
            member = [n for n in archive.namelist() if n.endswith('/k6')]
            assert len(member) == 1
            data = archive.read(member[0])
        target = root / 'k6'
    else:
        with tarfile.open(path) as archive:
            member = [m for m in archive.getmembers() if pathlib.PurePosixPath(m.name).name in ('k6', 'actionlint') and m.isfile()]
            assert len(member) == 1
            data = archive.extractfile(member[0]).read()
        target = root / ('actionlint' if name.startswith('actionlint') else 'k6')
    target.write_bytes(data)
    target.chmod(0o755)
PY
"$out/tools/actionlint" -version | tee "$out/actionlint-version.txt"
# Bash and Python syntax are checked separately. Do not depend on runner-global
# shellcheck/pyflakes versions; actionlint still checks workflow expressions/schema.
"$out/tools/actionlint" -shellcheck= -pyflakes=
"$out/tools/k6" version | tee "$out/k6-version.txt"
python3 benchmarks/harness.py smoke --k6 "$out/tools/k6" --output "$out/benchmark-smoke" >"$out/benchmark-smoke.log" 2>&1
python3 benchmarks/harness.py summarize --dataset "$out/benchmark-smoke" >"$out/benchmark-summary.json"
