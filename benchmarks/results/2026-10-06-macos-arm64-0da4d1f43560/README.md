# Reviewed macOS ARM64 benchmark evidence

Measured source: `0da4d1f4356027b16e9f4f32164ee9261585c33d`. Collection: October 6, 2026, 05:18:40–05:30:51 UTC. Native Apple M3 (8 cores, 16 GiB), macOS 26.6.2, Go 1.27.1/cgo=1, Python 3.9.6 and standalone k6 2.3.0. No containers or emulation.

This is the approved **textual evidence subset**: 167 existing files plus this README, [IMPORT.json](IMPORT.json) and published SHA256SUMS. The unchanged full bundle and independent review were copied to durable ignored local storage before transformation. That local copy is not a public archive. IMPORT.json records original/published hashes, exclusions and field transformations. Original binary manifest/hash entries and [original bundle checksums](provenance/original-bundle-SHA256SUMS) remain evidence for omitted bytes, not checksums for redacted files.

## Results

| VUs | Median paired proxy minus direct p95 (ms) | Three-pair range (ms) | Median paired proxy/direct throughput |
| --- | ---: | ---: | ---: |
| 1 | 0.060 | 0.060–0.061 | 0.631 |
| 25 | 1.481 | 1.256–1.565 | 0.435 |
| 100 | 5.427 | 5.3928–7.153 | 0.573 |

Nine alternating-order pairs contain 18 path trials with excluded 5 s warm-up and 30 s measured start-membership windows. [Derived results](dataset/derived.json), [full per-trial calculations](independent-summary.json), [raw manifest](dataset/manifest.json) and the [portable collection handoff](review-handoff.md) retain per-trial counts, quantiles, tails, paired differences and ranges. No favorable replacements or invalid timed trials occurred.

The separate 1,000-request correctness cohort observed 200 synthetic responses and 800 upstream calls. Each of three no-retry trials completed 800/1,000 operations with 1,000 physical requests; each retry trial completed 1,000/1,000 with 1,249 physical requests and 1,000 upstream calls. All-outcome operation p95 ranges were 0.143–0.205 ms without retries and 103.012–103.186 ms with retries. All 6,000 operation histories are retained. Retry modes consume different physical fault schedules; these are not identically assigned logical faults. Fixed-delay and pre/post-header timeout controls are separate correctness evidence.

Shared-machine desktop activity remained; there was no continuous resource monitoring. Only three repetitions were collected. INFO record construction and emission attempts were enabled, but blocking-file output was skipped under the accepted sink policy; capture was excluded. Closed-loop HTTP-duration results exclude initial DNS/connection/blocked time and vary with client/fixture contention. They establish neither maximum capacity, an SLA, universal overhead nor Linux/container performance.

## Verification and restoration

Independent dataset review checked all 152 dataset hashes, 181 outer checksums, 72 source identities, accounting/exits, 18 path trials, nine pairs, six retry trials and derived arithmetic. Both harness summary forms reproduced derived.json. Retry type-7 quantiles were recalculated from individual operation records. Individual overhead latency samples were not retained: k6 aggregates and subsequent arithmetic were checked, not independently reconstructed latency quantiles. Hosted CI identity is previously recorded/reported evidence; the dataset reviewer could not retrieve those logs.

From this directory, verify the published subset using:

```sh
shasum -a 256 -c SHA256SUMS
```

The textual subset alone **cannot pass the formal validator**, because three binaries were intentionally excluded. For formal validation, copy `dataset` into a new disposable directory, restore the original `proxy`, `upstream` and `client` bytes from the private preserved collection (never rebuild substitutes), verify their manifest SHA-256 entries, then invoke the measured harness:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 benchmarks/harness.py summarize \
  --require-formal --dataset "$RESTORED_DATASET"
```

Run that command from a checkout of the measured source. Normalized `${DATASET_ROOT}`, `${COLLECTION_ROOT}`, `${TOOL_ROOT}`, `${SOURCE_ROOT}` and `${USER_HOME}` strings are portable provenance labels, not executable paths. Original binaries retain embedded local paths and stay outside Git. The independent review itself is preserved privately; its approved inventory hash is in IMPORT.json.

Native Linux container execution, committed-image provenance, hosted final CI and release/consumer acceptance remain separate pending gates.
