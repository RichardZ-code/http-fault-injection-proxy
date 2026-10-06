# Final CI and manual release preparation

These workflows are prepared locally. They have not been dispatched or accepted on the resulting commit. P12's separate audit and P13 publication remain later gates. No tag, GitHub release, GHCR package or visibility change is authorized by preparation.

## Ordinary native CI

[Native Go checks](../.github/workflows/ci.yml) runs on ubuntu-24.04 amd64 and macos-15 ARM64, with Go 1.27.1, native cgo/race enabled, GOWORK=off, GOTOOLCHAIN=local and empty GOFLAGS. It asserts host/target platform, records compiler/Python identities, checks formatting without rewriting, tidy-diff, module integrity, vet, full uncached ordinary/race suites, helper regressions, builds and the documented binary demonstration from an external directory. Existing Python benchmark/capture tests execute through both Go suites without skips. Python and executable children are ordinary builds.

The prepared additions run pinned govulncheck against the current Go vulnerability database for native tests and a static linux/amd64 build configuration. Every JSON finding record fails the gate; streams/configuration/version metadata are retained. Scans exclude Python/container OS vulnerabilities and cannot establish absence of vulnerabilities. The reduced k6 smoke validates correctness/accounting/cleanup, never formal performance or a throughput threshold.

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

Each native archive contains faultproxy (0755), LICENSE, example.yaml, USAGE.md and RELEASE.json. The metadata binds source SHA, proposed version, native platform, build information and binary hash. Runtime output remains `faultproxy dev commit=<SHA> go=go1.27.1`: version input labels the candidate package, not an implemented semantic-version runtime. P13 version handling still needs a scoped decision and verification before claiming a released runtime version. No runtime change was made for this preparation.

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

## Later tested-image promotion

After separate P13 authorization, [Publish reviewed container](../.github/workflows/publish-container.yml) requires explicit version/source, preparation run ID/attempt and reviewed PROVENANCE.json hash. It verifies the selected run is successful, from this repository, workflow_dispatch, the exact preparation workflow and source SHA. The existing version tag must resolve to that SHA. The chosen artifact is downloaded by exact name/run with digest-mismatch failure.

Local provenance/hash checks require the exact candidate inventory and tested image archive. Docker loads it, verifies Docker store image ID and platform, then tags/pushes that image to `ghcr.io/<lowercase-repository>:<version>`. There is no build step or latest-run lookup. Existing published tags are rejected; authentication/network failures do not prove a tag absent. A fresh task-owned credential directory is used and removed after logout. GITHUB_TOKEN is used only here for registry authentication, and packages:write exists only on the promotion job; contents/actions remain read-only. This workflow does not create Git tags, release assets or package visibility. Those remain separately authorized Desktop/browser operations.

The entire promotion job, including its tag-absence check, uses a repository/version [Actions concurrency group](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idconcurrency) with cancel-in-progress: false. Version validation accepts only the canonical lowercase vMAJOR.MINOR.PATCH spelling without leading zeros, whitespace or suffixes; source/run/attempt differences cannot bypass the group for an accepted version. This serializes this workflow's promotions and preserves the running job. The default queue retains at most one pending job; a newer pending invocation can replace it. This is not registry-side locking and does not protect against unrelated external registry writers. Hosted serialization remains unverified until separately authorized execution.

Registry manifest digest, Docker store image ID, saved-image configuration digest, image-archive SHA-256 and Actions artifact digest are distinct. Docker containerd stores can report an index digest as .Id; the actual configuration digest is obtained from its saved bytes, never inferred from .Id. Record all relevant values in the review packet without equating them. Correct a published release through an explicit new version, never tag/asset replacement.

## Later public consumer verification

The manual [Verify published release](../.github/workflows/verify-release.yml) requires source/version, reviewed SHA256SUMS-file hash, published manifest digest, reviewed Docker store image ID and saved-image configuration digest. Both native jobs download all four assets using HTTPS curl without authenticated headers or GitHub tokens, validate hashes/safe archive inventories, run the installed native binary demo, build the downloaded source and run its demo. Linux uses a fresh empty Docker credential directory to pull by the explicit registry digest, verifies both store and saved-configuration identities and runs the same native smoke. Only its fixture image is built; the consumed proxy remains the anonymously pulled digest.

This cannot establish anonymous package access before publication. Signed-out browser access, final visibility checks and observing the final demo remain P13 user-facing gates. Prepared YAML and local helper tests do not substitute for successful hosted candidate/promotion/consumer runs on reviewed identities.
