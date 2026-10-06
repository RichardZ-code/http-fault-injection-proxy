# P08 independent verification record

Audited source: `68fd054b16dd27b9676968e83d3be9dbca5933dd` (`docs: record passing P07 native CI`). The independent audit reported no actionable correctness findings and no required production corrections. This record summarizes that audit against the [accepted design](design.md) and [decisions](design-decisions.md); it does not expand the runtime contract.

## Attribution and source preservation

The user supplied the independent audit's verdict, environment, commands and results. Its temporary evidence was still available for read-only inspection during this documentation handoff. Inspected the source manifest/preservation record, ordinary/race JSON event logs, production demo summaries, scanner version/database metadata and both complete vulnerability JSON streams. These artifacts corroborate the source identities, test/helper counts, demonstrations and zero finding records below; the audit report supplies the command exit-status conclusions and review verdict. No test campaign, scanner, build or container was rerun for this handoff.

Before editing, local HEAD and cached origin/main were the audited SHA and the checkout was clean. All 62 tracked files matched the audit manifest's SHA-256 values, Git blob identities and executable modes. The auditor separately recorded an exact disposable export of those 62 files and modes, with no checkout/export differences or export extras at completion. The preceding implementation commit is `f9409004a19ed1e2cf68968b9124ef0bbd9e0392`; the audited commit changes only the P07 progress record relative to it.

The temporary evidence is not durably archived or included in the repository. Filenames such as `source-manifest.json`, `preservation.json`, `ordinary.jsonl`, `race.jsonl`, `demo-summary.json`, `vuln-source.log` and `vuln-linux-static.log` identify what was inspected, not guaranteed future downloads. The audit report remains the explicit attribution if those disposable artifacts disappear. This handoff changes documentation only and does not imply independent audit of the new documentation bytes.

## Native checks and production demonstrations

Audit environment, as reported: macOS 26.6.2 ARM64, Go 1.27.1, Apple Clang 21.0.0 and Python 3.9.6. The auditor reported passing formatting, module tidy/integrity, vet, proxy/fixture/retry-client builds and whitespace checks. Saved formatting/tidy/vet/build logs contain no diagnostics; module verification reports all modules verified.

| Audit execution | Observed saved evidence |
| --- | --- |
| Full uncached ordinary suite | 292 passing Go tests/subtests across eight passing packages; all four Python helper checks executed; no failures or skips |
| Full uncached race suite | 292 passing Go tests/subtests across eight passing packages; all four Python helper checks executed; no failures, skips or race reports |
| Six-request cohort | Statuses 200,200,503,200,200,503; six terminal request/histogram observations, two status actions, four upstream calls, zero upstream errors |
| Retry cohort | Six successful logical operations, eight application attempts, six upstream calls; client/proxy/upstream accounting reconciled |
| Separate production cohorts in each suite | Pass-through, no-retry, retry, restart and delay/timeout cohorts; 1+6+8+6+4 = 25 parsed access records per execution, zero malformed lines; process reaping and port rebinding reported |
| Undrained stderr | Ordinary executable exited 1 in 5.004 s while stderr remained full; child reaped and both ports released without a harness kill |
| Capture-helper forced shutdown | Proxy exit 1 preserved in 5.028 s ordinarily and 5.035 s under the race parent; proxy/writer/launcher reaped and both ports released without a harness kill |

The four Python checks cover owned descriptor flags/status/reaping, existing-file preservation, output failure and stalled-output draining. Executable children are ordinary builds even under race-instrumented Go test parents; Python processes are not race-instrumented. The race suite exercises in-process Go code. Passing tests are evidence, not proof of race freedom or universal cleanup. These small cohorts establish correctness observations, not benchmark results.

## Audited contract coverage

The independent review reported the following contracts coherent, supported by the native tests and production demonstrations. No production correction was requested.

| Contract area | Reviewed coverage and distinctions |
| --- | --- |
| Strict configuration and startup | Complete bounded YAML/file admission; recursive unknown/duplicate/type/schema rejection; shared startup/config-check validation before binding; config-check without listeners or upstream contact; invalid input versus runtime exit status and partial-bind cleanup |
| HTTP, headers and body admission | Fixed HTTP origin/Host, supported HTTP/1.1 methods and target/path/query handling; hop-by-hop stripping and reserved/forwarded metadata sanitation including 1xx/trailers; bounded complete body admission before allocation; malformed/oversized/cancelled inputs without upstream effects; bodyless responses and admin isolation |
| Rule allocation and concurrency | First match without fall-through; synchronized per-rule counters and independent seeded PCG streams; exact every-N allocation, exhaustion and immutable decisions; cancellable delay outside locks; consumed allocation after cancellation; restart/serial reproducibility without promising concurrent client ordering |
| Deadlines and causal outcomes | Forwarding budget starts after admission/allocation/delay and covers acquisition, headers, body, downstream transfer and checked final flush; real upstream 503, transport failure, pre-header 504 and post-header incomplete response remain distinct; original failure causality survives delayed diagnostics and downstream delivery failure; retained finalization deadlines and keep-alive cleanup |
| Shutdown and ownership | One shared 5-second drain plus at most 1-second forced cleanup; SIGINT/SIGTERM, serving/close/join failures, owned request/body/dial/callback cleanup and listener release; undrained stderr cannot renew the budget; shared descriptor status flags are never changed |
| Metrics and request logging | Four private-registry families, bounded labels and 23,230-series bound; terminal/duration accounting separate from started actions and deduplicated upstream events; scrape isolation/concurrency; fixed causal vocabulary and privacy; handler-observable completion excludes later server framing/client receipt |
| Capture | Owned nonblocking pipes, continuous draining, signal forwarding, child exit preservation/reaping, exclusive files and capture/output failure handling; separate at-most-0.5-second writer allowance follows proxy reaping; dropped chunks, partial records and tail loss remain possible, and exit 0 does not certify capture completeness |
| Retry client and fixture | GET-only shared client/transport; one or at most three application attempts, 5-second operation/1-second attempt bounds and cancellable 100/200 ms backoff; selective retries, no redirects, bounded body cleanup and joined dials; fixture counts/cancellation/partial transfers; reset cohorts reconcile application attempts separately from upstream traffic |

Successful handler observation does not certify post-handler chunk/trailer writes or client consumption. The auditor reported checked final flush, retained deadlines, keep-alive reuse, pre-header/post-header behavior and cleanup passing as distinct checks.

## Go vulnerability analysis

The auditor used govulncheck v1.8.0 with Go 1.27.1 source analysis. Both commands reportedly exited 0. This handoff parsed every top-level JSON record in each saved stream and explicitly found zero `finding` records; a zero exit alone was not used as the finding count.

| Scan | Audit command | Approximate completion (UTC) | Findings |
| --- | --- | --- | --- |
| Native, including tests | `govulncheck -json -test ./...` | 2026-10-06 01:37:29 | 0 finding records |
| Linux amd64 static target | `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 govulncheck -json ./...` | 2026-10-06 01:41:09 | 0 finding records |

Both saved configurations identify symbol-level source scans, scanner v1.8.0, Go 1.27.1 and [vuln.go.dev](https://vuln.go.dev) database modification time 2026-10-01 20:24:15 UTC. The Linux scan analyzes that target's source; it is not Linux execution or container testing. These results describe the recorded source/tool/database snapshot. They exclude Python and container OS vulnerability analysis and do not prove absence of vulnerabilities or future advisories.

## Evidence boundaries and remaining gates

P07 [CI run 37387567046](https://github.com/RichardZ-code/http-fault-injection-proxy/actions/runs/37387567046) is previously recorded/reported evidence for `f9409004a19ed1e2cf68968b9124ef0bbd9e0392`, attempt 1: both native Linux amd64 and macOS ARM64 Go jobs passed. The auditor could not access those hosted logs. Preserve the [prior CI record](progress.md#p07-hosted-ci-completion) as earlier verification, not independently reverified P08 evidence or CI on the audited documentation commit. No new hosted run was inspected or dispatched for this handoff.

The auditor inspected existing Docker images and historical results without rebuilding or executing them. Their local image IDs retain the [P07 dirty-source provenance](progress.md#p07-docker-continuation-results) and linux/amd64 emulation on Apple Silicon attribution. Neither the native audit nor the Linux static vulnerability scan establishes native Linux container behavior. Native Linux containers, committed-source image provenance and hosted Docker checks remain pending; the existing native workflow contains no Docker checks. Release/consumer verification remains later-phase work.

The P08 audit is closed with no actionable correctness findings. This documentation handoff is ready for commit review after its consistency/link/whitespace checks. The audited production source is unchanged; P09 measurements and later phases are not started. This record does not authorize staging, commit, push, workflow dispatch, asset rebuild or publication. Phase status and handoff checks are recorded in [progress](progress.md#p08-independent-audit-and-documentation-handoff).
