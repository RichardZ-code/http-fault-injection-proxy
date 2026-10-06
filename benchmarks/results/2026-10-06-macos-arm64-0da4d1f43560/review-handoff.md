> Portable copy of the original collection handoff. Absolute temporary links are normalized. CI is recorded/reported evidence, not independently reverified by the dataset reviewer. Collection-time status statements below are historical.

# Formal P09 dataset review handoff

Measured source: `0da4d1f4356027b16e9f4f32164ee9261585c33d`. Dataset: `dataset/`. Protocol p09-v1, kind formal, one campaign and one attempt per prescribed pair. Reviewed harness CI run 37415876833, attempt 1 was verified before authorization; CI is not benchmark execution evidence.

Campaign UTC: 2026-10-06T05:18:40.624708+00:00 through 2026-10-06T05:30:51.686575+00:00 (October 6, 2026, approximately 12:18-12:30 AM America/Chicago).

## Conditions and provenance

Native Apple M3, 8 cores, 16 GiB RAM; macOS 26.6.2, ARM64. Go 1.27.1 with cgo=1; Apple clang version 21.0.0 (clang-2100.1.1.101); Python 3.9.6; k6 v2.3.0 (commit/e088784614, go1.26.8, darwin/arm64).

Clean committed bytes/modes and hashes for all 72 tracked files matched before, at trial boundaries, and after collection. Final independent file-hash and clean-HEAD checks passed. The source/tool/build/config identities, commands, UTC timing, endpoint map and accounting snapshots are retained in dataset/manifest.json. All three binaries are ordinary native builds, without race, coverage or custom optimization flags.

The selected tool executable and retained official archive/checksum identities were reverified before collection. Archive/checksum copies are in tool-evidence/. This is byte/checksum/source verification, not an independent signature audit or a fresh vulnerability scan.

AC power was attached before and after collection. The scoped task-owned caffeinate process exited 0 and was reaped. The operator manually quit legacyScreenSaver-x86_64 before traffic; earlier sustained CPU evidence is retained in process-recheck.json/process-analysis.json and is not benchmark data. Post-operator samples confirmed its absence. Chrome renderer used about 33% of a core over the ten-second preflight interval; WindowServer used about 22%. Immediately before/after the campaign Chrome reported 18.6%/37.1%, WindowServer 20.8%/20.2%, and coreaudiod 10.1%/8.1%. These snapshots are not continuous proof of idle conditions. Ordinary desktop activity and periodic assistant progress/output remained; no unrelated processes were terminated. No agent concurrent test/race/Docker workload ran.

An earlier thermal query returned unavailable errors; actual before/after campaign queries reported no recorded thermal/performance warnings and no CPU power status. This does not establish absence of throttling. The original condition attestation conservatively records the earlier unavailability; operator-ledger.json preserves the successful later query outputs. No operator signal/disturbance event was reported during collection. No continuous machine/resource telemetry was collected.

Production INFO access construction/capability checks/attempts and metrics remained enabled. Initially blocking regular-file stderr was skipped under the accepted sink policy; no capture helper ran. Metrics/health/statistics were read outside the timed trials. Fixed 1 KiB body, HTTP/1.1, keep-alive, one bodyless GET per iteration, no application retries, 2-second request bound and 3-second gracefulStop. Client, fixture and proxy share this machine.

## Every paired result

Each path used an excluded 5-second warm-up and a 30-second measured request-start window. Throughput is measured-start completions / 30 seconds. Completion tails are separate; warm-up/tail starts are excluded. The complete k6 summary, whole-run counts/checks, start/completion phase populations, percentile min/max and wall/execution boundaries are retained.

| Pair | Order | Direct count | Proxy count | Direct rps | Proxy rps | Direct p95 ms | Proxy p95 ms | Difference ms | Ratio |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| v1-r1 | direct, proxy | 364746 | 236512 | 12158.200000 | 7883.733333 | 0.046 | 0.106 | 0.06 | 0.648429318 |
| v1-r2 | proxy, direct | 378468 | 238991 | 12615.600000 | 7966.366667 | 0.041 | 0.101 | 0.06 | 0.631469503 |
| v1-r3 | direct, proxy | 382287 | 239502 | 12742.900000 | 7983.400000 | 0.04 | 0.101 | 0.061 | 0.626497893 |
| v25-r1 | proxy, direct | 1325377 | 580557 | 44179.233333 | 19351.900000 | 1.152 | 2.408 | 1.256 | 0.438031594 |
| v25-r2 | direct, proxy | 1063887 | 463106 | 35462.900000 | 15436.866667 | 1.451 | 3.016 | 1.565 | 0.43529623 |
| v25-r3 | proxy, direct | 1166318 | 488401 | 38877.266667 | 16280.033333 | 1.305 | 2.786 | 1.481 | 0.418754576 |
| v100-r1 | direct, proxy | 1058304 | 606125 | 35276.800000 | 20204.166667 | 4.56 | 9.9528 | 5.3928 | 0.57273241 |
| v100-r2 | proxy, direct | 1137951 | 544187 | 37931.700000 | 18139.566667 | 4.221 | 11.374 | 7.153 | 0.478216549 |
| v100-r3 | direct, proxy | 839335 | 523436 | 27977.833333 | 17447.866667 | 6.017 | 11.444 | 5.427 | 0.623631804 |

| VUs | All three p95 differences ms | Median difference ms | Min/max ms | Median paired throughput ratio |
| --- | --- | ---: | --- | ---: |
| 1 | 0.06, 0.06, 0.061 | 0.06 | 0.06, 0.061 | 0.631469503 |
| 25 | 1.256, 1.565, 1.481 | 1.481 | 1.256, 1.565 | 0.43529623 |
| 100 | 5.3928, 7.153, 5.427 | 5.427 | 5.3928, 7.153 | 0.57273241 |

## Every path trial

| Pair/path | Measured count | Warm-up count | Tail-start count | p50 ms | p95 ms | p99 ms | Completion tail s | k6 execution s | Orchestrator wall s | Raw summary |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| v1-r1/direct | 364746 | 65116 | 4 | 0.034 | 0.046 | 0.105 | 0 | 35.000637 | 35.1188011 | [raw](dataset/v1-r1-attempt1/direct-summary.json) |
| v1-r1/proxy | 236512 | 41551 | 12 | 0.079 | 0.106 | 0.179 | 0 | 35.000566 | 35.0892544 | [raw](dataset/v1-r1-attempt1/proxy-summary.json) |
| v1-r2/proxy | 238991 | 41603 | 10 | 0.079 | 0.101 | 0.173 | 0 | 35.00069 | 35.0988154 | [raw](dataset/v1-r2-attempt1/proxy-summary.json) |
| v1-r2/direct | 378468 | 68906 | 18 | 0.033 | 0.041 | 0.067 | 0 | 35.000623 | 35.1174893 | [raw](dataset/v1-r2-attempt1/direct-summary.json) |
| v1-r3/direct | 382287 | 68984 | 6 | 0.033 | 0.04 | 0.057 | 0 | 35.000718 | 35.0980833 | [raw](dataset/v1-r3-attempt1/direct-summary.json) |
| v1-r3/proxy | 239502 | 41915 | 6 | 0.079 | 0.101 | 0.171 | 0 | 35.000676 | 35.0651617 | [raw](dataset/v1-r3-attempt1/proxy-summary.json) |
| v25-r1/proxy | 580557 | 108868 | 13 | 1.058 | 2.408 | 3.38544 | 0.001 | 35.001337 | 35.3043917 | [raw](dataset/v25-r1-attempt1/proxy-summary.json) |
| v25-r1/direct | 1325377 | 215562 | 50 | 0.316 | 1.152 | 1.945 | 0 | 35.000952 | 35.4698153 | [raw](dataset/v25-r1-attempt1/direct-summary.json) |
| v25-r2/direct | 1063887 | 212853 | 16 | 0.377 | 1.451 | 2.752 | 0.001 | 35.001293 | 35.5309779 | [raw](dataset/v25-r2-attempt1/direct-summary.json) |
| v25-r2/proxy | 463106 | 75728 | 6 | 1.339 | 3.016 | 4.173 | 0.004 | 35.00415 | 35.2661071 | [raw](dataset/v25-r2-attempt1/proxy-summary.json) |
| v25-r3/proxy | 488401 | 80620 | 21 | 1.268 | 2.786 | 3.779 | 0.001 | 35.001116 | 35.2364163 | [raw](dataset/v25-r3-attempt1/proxy-summary.json) |
| v25-r3/direct | 1166318 | 201395 | 454 | 0.353 | 1.305 | 2.272 | 0.001 | 35.009106 | 35.4490475 | [raw](dataset/v25-r3-attempt1/direct-summary.json) |
| v100-r1/direct | 1058304 | 179763 | 208 | 0.985 | 4.56 | 8.202 | 0.006 | 35.006196 | 35.5074708 | [raw](dataset/v100-r1-attempt1/direct-summary.json) |
| v100-r1/proxy | 606125 | 104698 | 26 | 3.379 | 9.9528 | 15.228 | 0.004 | 35.003943 | 35.3423632 | [raw](dataset/v100-r1-attempt1/proxy-summary.json) |
| v100-r2/proxy | 544187 | 82071 | 143 | 3.877 | 11.374 | 18.199 | 0.017 | 35.017111 | 35.3061089 | [raw](dataset/v100-r2-attempt1/proxy-summary.json) |
| v100-r2/direct | 1137951 | 206953 | 43 | 0.952 | 4.221 | 7.101 | 0.002 | 35.001884 | 35.523793 | [raw](dataset/v100-r2-attempt1/direct-summary.json) |
| v100-r3/direct | 839335 | 180496 | 16 | 1.202 | 6.017 | 11.15266 | 0.002 | 35.002772 | 35.4851685 | [raw](dataset/v100-r3-attempt1/direct-summary.json) |
| v100-r3/proxy | 523436 | 69540 | 20 | 3.876 | 11.444 | 17.79465 | 0.004 | 35.004123 | 35.2935768 | [raw](dataset/v100-r3-attempt1/proxy-summary.json) |

All eighteen path trials had zero failed checks and unexpected HTTP errors. Their global requests, iterations, start/completion counters and phase populations reconciled. Maximum measured completion tail was 0.017 seconds. Pair reconciliation includes the two extra outside-window fixture-byte requests; the proxy observed one of them.

## Fixed-count and retry cohorts

Fresh N=5 correctness cohort: 1,000 framing-complete bounded responses, 200 synthetic 503 actions, 800 upstream calls, 1,000 terminal/histogram observations, zero upstream errors; status/marker/body hashes reconciled. correctness/requests.json and collection.json preserve complete client evidence.

| Mode/rep | Logical operations | Successes/failures | Application attempts | Physical requests | Physical/logical | Upstream calls | Synthetic actions | All-outcome operation p95 s | Deadline exhaustion | Client exit |
| --- | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| none/1 | 1000 | 800/200 | 1000 | 1000 | 1 | 800 | 200 | 0.00020504105 | 0 | 1 |
| retry/1 | 1000 | 1000/0 | 1249 | 1249 | 1.249 | 1000 | 249 | 0.10318563335 | 0 | 0 |
| none/2 | 1000 | 800/200 | 1000 | 1000 | 1 | 800 | 200 | 0.0001565455 | 0 | 1 |
| retry/2 | 1000 | 1000/0 | 1249 | 1249 | 1.249 | 1000 | 249 | 0.10301183685 | 0 | 0 |
| none/3 | 1000 | 800/200 | 1000 | 1000 | 1 | 800 | 200 | 0.0001427941 | 0 | 1 |
| retry/3 | 1000 | 1000/0 | 1249 | 1249 | 1.249 | 1000 | 249 | 0.1030514649 | 0 | 0 |

Every trial has 1,000 raw logical-operation records with histories and final outcomes/statuses. Three expected no-retry exits were 1; retry exits were 0. Application attempts equaled observed proxy physical requests in these controlled cohorts; no replay discrepancy or policy expiry occurred. Retry quantiles include all outcomes and the existing backoff/body-cleanup policy. Mode schedules differ as retries consume physical N=5 positions; these cohorts do not compare identical faults per logical operation or establish general availability.

## Controls

Zero-delay elapsed seconds: 0.00068008400001, 0.000346250000007, 0.000316542000064.

50 ms fixed-delay elapsed seconds: 0.052079542, 0.052034417, 0.0522785840001. Three delay actions, three successful observations/calls; zero-delay had no action.

With a 200 ms forwarding budget against the 3-second fixture wait, /slow produced complete unmarked pre-header 504; /partial retained initial 200 and incomplete transfer. Accounting retained one upstream_timeout, one incomplete_response, two timeout events, two cancelled upstream waits and no active work. These are correctness controls, not overhead samples.

## Validation, ownership and boundaries

Collector exited 0; plain summarize and summarize --require-formal each exited 0 and matched derived.json. The separate independent_recalculate.py imports no harness calculation functions. It checked all 152 dataset file identities, plan/order, request populations, timing, checks/thresholds, paired differences/ratios/medians/ranges, raw retry histories and all-outcome type-7 quantiles, fixed-count client evidence, controls and process exits. Its numbers match both retained derived.json and recalculated.json. Validation process evidence is in validation-results.json; independent-summary.json preserves every trial and control at full precision.

All 62 recorded campaign commands finished: 59 exits 0 and three expected no-retry exits 1. Proxy/fixture exits were 0. No child stderr file contained output, no collection/cleanup error or harness kill occurred. All 64 recorded owned PIDs (including wrapper harness and caffeinate) were absent during final read-only inspection. Ports 50167/50168/50169 rebound successfully and those probe listeners were closed. Only task-owned resources were cleaned up.

No favorable replacements, retry campaigns, source edits, tracked result imports, P10 work, commits, pushes, workflow dispatches or publication occurred. Preflight blockage retained evidence but created no timed trial; the complete formal campaign is attempt 1. The final checkout is clean at the measured SHA.

Limitations: three local paired repetitions, shared-machine desktop load and unmonitored disturbances; closed-loop rates vary with latency and client/fixture resource contention. k6 http_req_duration excludes initial DNS/connection/blocked time and is not logical-operation or proxy-handler duration. Independent overhead arithmetic uses retained k6 aggregate percentiles; individual latency samples were not recorded, so those quantiles cannot be independently re-estimated from samples. Retry type-7 quantiles were recalculated from individual logical-operation durations. No pooled percentile, maximum capacity, fixed-arrival SLA, universal overhead, Linux performance or container performance claim is supported. Native Linux containers, committed-image provenance and hosted Docker gates remain pending.

Historical collection handoff: independent dataset review and a subsequent textual import were separately authorized. The complete original bundle is now preserved locally outside temporary storage and Git. This portable copy retains collection-time statements above; no release or registry publication is authorized.
