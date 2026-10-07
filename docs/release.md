# CI, release preparation, and publication

The v0.1.0 candidate passed native CI/preparation, separately accepted independent P12 review, and explicitly authorized P13 publication/public-consumer verification. The frozen source and published bytes are recorded below. Preparation alone never authorizes publication; future releases require their own review and authorization.

## Ordinary native CI

[Native Go checks](../.github/workflows/ci.yml) runs on ubuntu-24.04 amd64 and macos-15 ARM64, with Go 1.27.1, native cgo/race enabled, GOWORK=off, GOTOOLCHAIN=local and empty GOFLAGS. It asserts host/target platform, records compiler/Python identities, checks formatting without rewriting, tidy-diff, module integrity, vet, full uncached ordinary/race suites, helper regressions, builds and the documented binary demonstration from an external directory. Existing Python benchmark/capture tests execute through both Go suites without skips. Python and executable children are ordinary builds.

The final native checks run pinned govulncheck against the current Go vulnerability database for native tests and a static linux/amd64 build configuration. Every JSON finding record fails the gate; streams/configuration/version metadata are retained. Scans exclude Python/container OS vulnerabilities and cannot establish absence of vulnerabilities. The reduced k6 smoke validates correctness/accounting/cleanup, never formal performance or a throughput threshold.

Only Linux runs [plain/capture container smoke](../scripts/docker_smoke.py): explicit linux/amd64, native host/daemon assertions, nonroot identity, read-only config, loopback ports, service-name upstream, pass-through, deterministic faults, retry accounting, delay/real-503/pre/post-header timeout distinctions, restart, SIGINT/SIGTERM, forced cleanup and port reuse. Capture checks a small cohort's parsed records and 0600 permissions, not losslessness. Each invocation owns unique containers/network; failure diagnostics are saved before scoped teardown. It never rebuilds the selected proxy image. Mac Docker runs omit --native and remain emulation evidence.

Teardown records each container/fallback removal failure and continues remaining owned containers, network removal and every port check. The final docker-smoke.json write is attempted even after cleanup failures; its original failure and cleanup errors remain separate, and either fails verification. A fallback timeout can leave that owned resource behind and network removal may also fail. An unwritable report destination cannot preserve the report, so the raised diagnostic includes the original failure and cleanup/persistence errors. Cleanup failures do not establish resource release.

Job timeouts bound all campaigns. Relevant reports, binaries, raw smoke records and failure logs are uploaded with 14-day retention. Artifact expiry is a review gate: inspect/download preserved bytes before expiry, or prepare a new separately reviewed candidate. No implicit latest-run selection or silent replacement is allowed.

## Verified pins

Official action tags were resolved through GitHub's public tag API, and upload/download input schemas were inspected on October 6, 2026:

| Action | Version | Commit |
| --- | --- | --- |
| actions/checkout | v7.0.1 | 3d3c42e5aac5ba805825da76410c181273ba90b1 |
| actions/setup-go | v7.0.0 | b7ad1dad31e06c5925ef5d2fc7ad053ef454303e |
| actions/upload-artifact | v7.0.1 | 043fb46d1a93c77aae656e7c1c64a875d1fc6a0a |
| actions/download-artifact | v8.0.1 | 3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c |

Sources: [checkout](https://github.com/actions/checkout/tree/v7.0.1), [setup-go](https://github.com/actions/setup-go/tree/v7.0.0), [upload](https://github.com/actions/upload-artifact/tree/v7.0.1), [download](https://github.com/actions/download-artifact/tree/v8.0.1). Artifact downloads explicitly fail digest mismatches. No personal token or additional third-party publication action is used.

[ci_tools.sh](../scripts/ci_tools.sh) installs [govulncheck v1.8.0](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck@v1.8.0) into task-owned GOBIN using module checksum verification. Official standalone [k6 v2.3.0](https://github.com/grafana/k6/releases/tag/v2.3.0) and [actionlint v1.7.12](https://github.com/rhysd/actionlint/releases/tag/v1.7.12) archive SHA-256 pins are verified before extracting only the tool member. Host architecture selects the verified Darwin ARM64 or Linux amd64 asset. This is checksum/source verification, not a signature audit. Docker/base-image pins remain the accepted [P07 decisions](design-decisions.md#p07-preparation-decisions); runner Docker/BuildKit and C compiler versions are recorded rather than claimed fixed. No project dependency or shared tool setting changes.

## Prepare release candidate (nonpublishing)

The manual [Prepare release candidate](../.github/workflows/prepare-release.yml) accepts a full lowercase source SHA and a proposed vMAJOR.MINOR.PATCH without leading zeros. The selected workflow commit must equal that SHA, and both native jobs check out exactly it. Clean-source checks reject unstaged/staged/untracked changes. Native checks, vulnerability analysis, smoke and installed-archive demos must pass before candidate assembly.

Each native archive contains faultproxy (0755), LICENSE, example.yaml, USAGE.md and RELEASE.json. The metadata binds source SHA, proposed version, native platform, build information and binary hash. Runtime output remains `faultproxy dev commit=<SHA> go=go1.27.1`: version input labels the candidate package, not an implemented semantic-version runtime. For v0.1.0, the user explicitly accepted that runtime presentation after independent review. No runtime change or asset rebuild was made; no semantic v0.1.0 runtime string is claimed.

The Linux job exports exact committed source through git archive, preserving tracked modes/module metadata, safely extracts it and builds/runs its demo. Exported source has no .git, so its local build may report unknown runtime source; the reviewed archive hash and candidate provenance bind it to the selected commit. This differs from native candidate binaries with VCS identity.

Public download inventory is exactly:

```text
http-fault-injection-proxy_<version-without-v>_darwin_arm64.tar.gz
http-fault-injection-proxy_<version-without-v>_linux_amd64.tar.gz
http-fault-injection-proxy_<version-without-v>_source.tar.gz
SHA256SUMS
```

SHA256SUMS lists those three archives. The Linux job saves the *already tested* plain proxy image, records its Docker store image ID, linux/amd64 platform, source attribution, archive SHA-256 and smoke evidence. Container runtime identity remains unknown under the accepted -buildvcs=false build; explicit build-source provenance is separate. Assembly records both native identities, repository, run ID/attempt, all candidate hashes and PROVENANCE.json. Its SHA-256 is printed for the review packet.

The image archive, image metadata, smoke report, platform metadata and PROVENANCE.json are preparation artifacts, not additional public release downloads. The selected candidate artifact is named `candidate-<full-SHA>-<version>-<attempt>`. Preparation never tags, creates a release or logs into GHCR. Default permissions are contents:read; same-run artifact download uses the Actions runtime. Failure evidence uploads run even when preceding checks fail.

## Tested-image promotion

After separate P13 authorization, [Publish reviewed container](../.github/workflows/publish-container.yml) requires explicit version/source, preparation run ID/attempt and reviewed PROVENANCE.json hash. It verifies the selected run is successful, from this repository, workflow_dispatch, the exact preparation workflow and source SHA. The existing version tag must resolve to that SHA. The chosen artifact is downloaded by exact name/run with digest-mismatch failure.

Local provenance/hash checks require the exact candidate inventory and tested image archive. Docker loads it, verifies Docker store image ID and platform, then tags/pushes that image to `ghcr.io/<lowercase-repository>:<version>`. There is no build step or latest-run lookup. Existing published tags are rejected; authentication/network failures do not prove a tag absent. A fresh task-owned credential directory is used and removed after logout. GITHUB_TOKEN is used only here for registry authentication, and packages:write exists only on the promotion job; contents/actions remain read-only. This workflow does not create Git tags, release assets or package visibility. Those remain separately authorized Desktop/browser operations.

The entire promotion job, including its tag-absence check, uses a repository/version [Actions concurrency group](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idconcurrency) with cancel-in-progress: false. Version validation accepts only the canonical lowercase vMAJOR.MINOR.PATCH spelling without leading zeros, whitespace or suffixes; source/run/attempt differences cannot bypass the group for an accepted version. This serializes this workflow's promotions and preserves the running job. The default queue retains at most one pending job; a newer pending invocation can replace it. This is not registry-side locking and does not protect against unrelated external registry writers. The authorized v0.1.0 job executed successfully with this protection configured; no concurrent hosted promotion campaign was performed.

Registry manifest digest, Docker store image ID, saved-image configuration digest, image-archive SHA-256 and Actions artifact digest are distinct. Docker containerd stores can report an index digest as .Id; the actual configuration digest is obtained from its saved bytes, never inferred from .Id. Record all relevant values in the review packet without equating them. Correct a published release through an explicit new version, never tag/asset replacement.

## Public consumer verification

The manual [Verify published release](../.github/workflows/verify-release.yml) requires source/version, reviewed SHA256SUMS-file hash, published manifest digest, reviewed Docker store image ID and saved-image configuration digest. Both native jobs download all four assets using HTTPS curl without authenticated headers or GitHub tokens, validate hashes/safe archive inventories, run the installed native binary demo, build the downloaded source and run its demo. Linux uses a fresh empty Docker credential directory to pull by the explicit registry digest, verifies both store and saved-configuration identities and runs the same native smoke. Only its fixture image is built; the consumed proxy remains the anonymously pulled digest.

Anonymous access is established only after publication. For v0.1.0, both native public-consumer jobs and signed-out repository/release/package visibility passed. Prepared YAML and local helper tests alone do not establish those results.

## Published v0.1.0

Published on 2026-10-07 UTC (October 6 in America/Chicago), following user-supplied independent P12 approval and explicit P13 authorization. The public [repository](https://github.com/RichardZ-code/http-fault-injection-proxy), [release](https://github.com/RichardZ-code/http-fault-injection-proxy/releases/tag/v0.1.0) and [package](https://github.com/users/RichardZ-code/packages/container/package/http-fault-injection-proxy) were checked signed out. Tag v0.1.0 resolves to `23b46fdfc86ccf662f17b05e30cbe5ac867ddab1`; subsequent documentation closure does not move it.

| Identity | Verified value |
| --- | --- |
| Candidate run / attempt | [37536448305](https://github.com/RichardZ-code/http-fault-injection-proxy/actions/runs/37536448305) / 1 |
| PROVENANCE.json SHA-256 | `3cc7b14070e06c7f9e112968af9bee6b9915c4a6fb7742bf5434096644852596` |
| SHA256SUMS file SHA-256 | `69c6547bfccc3d92ea8f78ebf42e5d32840198abeca7f5fe197e8d95dfd4ebcd` |
| Darwin ARM64 archive SHA-256 | `1d81dd1648ae0ca782dc265bde2dd8a3ebb86107521cb8fd7fddeaea5cba5512` |
| Linux AMD64 archive SHA-256 | `309419f4714b5b94eff72fea05e1599d526dc034fb8f288df4947c54a9671049` |
| Source archive SHA-256 | `5e7723b6ab6f63d7b41470963d81a6ac26bb9923096ea10042cf510fcd9bf224` |
| Registry manifest digest | `sha256:0304313ab0fa34bc9ee9f9382c84e69c1b129f95d00b6a4d24f6e0109dd9b1da` |
| Docker store image ID | `sha256:1303fe8cf5ae5abe6be561db75da36597474ff94258230a6a41adbf08f15e6ab` |
| Saved-image configuration digest | `sha256:1303fe8cf5ae5abe6be561db75da36597474ff94258230a6a41adbf08f15e6ab` |

The four uploaded release assets match reviewed candidate bytes. GitHub's automatically generated source links are separate from the reviewed `_source.tar.gz` asset. No image.tar or raw evidence was uploaded as a release asset. [Promotion 37557393472, attempt 1](https://github.com/RichardZ-code/http-fault-injection-proxy/actions/runs/37557393472) verified the reviewed artifact/provenance, found the image tag absent, loaded the saved tested plain image and pushed it without rebuilding. The package was observed public immediately after promotion.

Pull the verified plain image by immutable manifest digest:

```sh
docker pull --platform=linux/amd64 ghcr.io/richardz-code/http-fault-injection-proxy@sha256:0304313ab0fa34bc9ee9f9382c84e69c1b129f95d00b6a4d24f6e0109dd9b1da
```

The version tag is `ghcr.io/richardz-code/http-fault-injection-proxy:v0.1.0`. It is a linux/amd64 image; Apple Silicon execution requires emulation. The capture target is not a published release image.

[Public-consumer verification 37557579597, attempt 1](https://github.com/RichardZ-code/http-fault-injection-proxy/actions/runs/37557579597) passed on native Linux AMD64 and macOS ARM64 using Go 1.27.1. Each downloaded all four assets anonymously, verified hashes/inventories, exercised the installed native binary, built downloaded source with module checks, and ran both five-cohort demonstrations. Each demonstration produced 25 parsed access records, zero malformed records, and reaped owned processes/rebound ports without a harness kill. Linux additionally pulled the public image by digest with empty Docker credentials, verified store/config identities, and passed native plain-image smoke, restart, signals, forced shutdown (exit 1 in 5.167 seconds) and cleanup with no errors. Docker verification is intentionally Linux-only. This consumer workflow does not repeat the full CI/candidate suites or vulnerability scans.

Runtime `dev`/unknown-source presentation, bounded lossy capture, historical benchmark attribution, non-atomic test port handoff, and the unconfirmed historical Linux startup cause remain disclosed limitations. See [publication progress](progress.md#p12-acceptance-and-p13-publication) for the evidence boundary and local preservation record.
