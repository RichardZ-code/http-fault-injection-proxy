import http from 'k6/http';
import {check} from 'k6';
import exec from 'k6/execution';
import {Counter, Trend} from 'k6/metrics';

const warm = Number(__ENV.WARM_MS);
const window = Number(__ENV.MEASURE_MS);
const expectedBody = '0123456789abcdef'.repeat(64);
const started = new Counter('cohort_started');
const completed = new Counter('cohort_completed');
const failed = new Counter('cohort_failed');
const timing = new Trend('cohort_clock_ms');
const duration = new Trend('cohort_http_duration', true);
const checked = new Counter('cohort_checked');
const badCheck = new Counter('cohort_bad_check');

export function phaseAt(start, warm, window) {
  return start < warm ? 'warmup' : start < warm + window ? 'measure' : 'tail';
}

// Bounded smoke also exercises the actual JS boundary function without traffic.
if (__ENV.SELF_TEST === '1') {
  const cases = [[0, 'warmup'], [4999, 'warmup'], [5000, 'measure'],
    [34999, 'measure'], [35000, 'tail']];
  for (const [start, expected] of cases) {
    if (phaseAt(start, 5000, 30000) !== expected) throw new Error('phase boundary failed');
  }
}

export const options = {
  scenarios: {load: {executor: 'constant-vus', vus: Number(__ENV.VUS),
    duration: `${warm + window}ms`, gracefulStop: '3s'}},
  noConnectionReuse: false, noVUConnectionReuse: false,
  maxRedirects: 0, discardResponseBodies: false,
  summaryTrendStats: ['min', 'max', 'p(50)', 'p(95)', 'p(99)'],
  thresholds: {
    'cohort_http_duration{phase:measure}': ['max>=0'],
    'cohort_clock_ms{point:first_start}': ['min>=0'],
    'cohort_clock_ms{point:last_finish}': ['max>=0'],
    'cohort_started{phase:measure}': ['count>0'],
    'cohort_completed{phase:measure}': ['count>0'],
    'cohort_started{phase:warmup}': ['count>0'],
    'cohort_completed{phase:warmup}': ['count>0'],
    'cohort_started{phase:tail}': ['count>=0'],
    'cohort_completed{phase:tail}': ['count>=0'],
    'cohort_failed{phase:measure}': ['count==0'],
    'cohort_bad_check{phase:measure}': ['count==0'],
    checks: ['rate==1'], http_req_failed: ['rate==0'],
  },
};

export default function () {
  // scenario.startTime is a common origin, not a clock initialized by each VU.
  const clock = () => Date.now() - exec.scenario.startTime;
  const start = clock();
  const phase = phaseAt(start, warm, window);
  const tags = {phase};
  started.add(1, tags);
  if (phase === 'measure') timing.add(start, {point: 'first_start'});
  const r = http.get(`${__ENV.TARGET}/benchmark`, {
    headers: {'Accept': 'text/plain', 'Accept-Encoding': 'identity', 'X-Benchmark': 'v1'},
    redirects: 0, timeout: '2s', tags,
  });
  const ok = check(r, {
    'status and bytes': r => r.status === 200 && r.body === expectedBody && __ENV.BAD_CHECK !== '1',
    'HTTP/1.1 and headers': r => r.proto === 'HTTP/1.1' && r.headers['Content-Length'] === '1024'
      && r.headers['Content-Type'] === 'text/plain; charset=utf-8',
  }, tags);
  checked.add(2, tags);
  badCheck.add(ok ? 0 : 1, tags);
  failed.add(r.error_code || r.status !== 200 ? 1 : 0, tags);
  duration.add(r.timings.duration, tags);
  completed.add(1, tags);
  if (phase === 'measure') timing.add(clock(), {point: 'last_finish'});
}

export function handleSummary(data) {
  // Preserve the complete selected-version object without altering raw metrics.
  return {[__ENV.RAW_SUMMARY]: JSON.stringify(data)};
}
