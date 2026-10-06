#!/usr/bin/env python3
"""P09 local benchmark preparation and separately authorized formal collection."""
import argparse
import hashlib
import http.client
import json
import math
import os
from pathlib import Path
import platform
import re
import signal
import socket
import statistics
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parent.parent
K6_VERSION = '2.3.0'
GO_VERSION = 'go1.27.1'
PYTHON_VERSION = '3.9.6'
K6_MAC_SHA256 = 'efb8282e24ffe18f54ce3679eaa71d72e0645bbb97b14e2a05cc8b192abf48e3'
SCHEMA = 1
PROTOCOL = 'p09-v1'
BODY = b'0123456789abcdef' * 64
CONDITIONS = {'logging': 'INFO access enabled; construction/attempts remain; blocking regular-file stderr skipped',
              'capture': False, 'metrics': 'enabled; only outside timed windows',
              'execution': 'native', 'http': 'HTTP/1.1', 'keep_alive': True,
              'request_timeout_seconds': 2, 'graceful_stop_seconds': 3,
              'fixture_logging': 'none', 'retry_concurrency': 1,
              'retry_policy': '5s operation, 1s attempt, 100/200ms backoff, 1/3 attempts, no redirects'}


def require(condition, message):
    if not condition:
        raise ValueError(message)


class CollectionCancelled(ValueError):
    pass


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def write_json(path, data):
    # Exclusive creation makes raw evidence immutable to this harness.
    with Path(path).open('x') as stream:
        json.dump(data, stream, indent=2, sort_keys=True, allow_nan=False)
        stream.write('\n')


def load_json(path):
    return json.loads(Path(path).read_text(), parse_constant=lambda s: (_ for _ in ()).throw(ValueError('nonfinite JSON')))


def portable_manifest(value):
    # Keep command arguments and identities without workstation/user prefixes.
    if isinstance(value, dict):
        return {k: portable_manifest(v) for k, v in value.items()}
    if isinstance(value, list):
        return [portable_manifest(v) for v in value]
    if isinstance(value, str):
        return value.replace(str(ROOT), '${SOURCE_ROOT}').replace(str(Path.home()), '${HOME}')
    return value


def git(root, *args):
    return subprocess.check_output(['git', '-C', str(root), *args], text=True,
                                   env={**os.environ, 'GIT_OPTIONAL_LOCKS': '0'}, timeout=5).strip()


def source(root=ROOT):
    tracked = git(root, 'ls-files', '-z').split('\0')
    untracked = git(root, 'ls-files', '--others', '--exclude-standard', '-z').split('\0')
    names = sorted(set(n for n in tracked + untracked if n))
    entries = {}
    for entry in git(root, 'ls-tree', '-rz', 'HEAD').split('\0'):
        if entry:
            metadata, name = entry.split('\t', 1)
            mode, kind, blob = metadata.split()
            entries[name] = (mode, blob)
    committed = len(names) == len(entries)
    for name in names:
        data = (root / name).read_bytes()
        blob = hashlib.sha1(b'blob ' + str(len(data)).encode() + b'\0' + data).hexdigest()
        mode = '100755' if (root / name).stat().st_mode & 0o111 else '100644'
        committed = committed and entries.get(name) == (mode, blob)
    return {'sha': git(root, 'rev-parse', 'HEAD'), 'committed_match': committed,
            'status': git(root, 'status', '--porcelain=v1', '--untracked-files=all'),
            'inputs': {n: digest(root / n) for n in names}}


def formal_source(state, expected):
    require(expected is not None and re.fullmatch('[0-9a-f]{40}', expected), 'expected full SHA required')
    require(state['sha'] == expected, 'HEAD differs from expected SHA')
    require(state['status'] == '', 'formal collection requires clean staged/unstaged/untracked state')
    require(state.get('committed_match') is True, 'source bytes/modes differ from committed inputs')
    for name in ['benchmarks/harness.py', 'benchmarks/workload.js', 'benchmarks/empty.yaml',
                 'benchmarks/nth.yaml', 'benchmarks/delay.yaml', 'examples/upstream/main.go',
                 'examples/retry-client/client.go', 'cmd/faultproxy/cli.go', 'go.mod', 'go.sum']:
        require(name in state['inputs'], 'missing committed input: ' + name)


def output_path(raw, root=ROOT):
    p = Path(raw).resolve()
    require(p != root.resolve() and root.resolve() not in p.parents, 'output must resolve outside checkout')
    require(not p.exists(), 'output exists; never overwrite a dataset')
    require(p.parent.is_dir(), 'output parent must exist')
    return p


def plan(kind):
    require(kind in ['smoke', 'formal'], 'unknown dataset kind')
    formal = kind == 'formal'
    pairs = []
    order = 0
    for vu in ([1, 25, 100] if formal else [1]):
        for repetition in range(1, 4 if formal else 2):
            paths = ['direct', 'proxy'] if order % 2 == 0 else ['proxy', 'direct']
            pairs.append({'id': 'v%d-r%d' % (vu, repetition), 'vu': vu, 'repetition': repetition,
                          'paths': paths, 'order': order, 'warm_ms': 5000 if formal else 500,
                          'measure_ms': 30000 if formal else 500, 'attempt': 1})
            order += 1
    return {'kind': kind, 'pairs': pairs, 'correctness_requests': 1000 if formal else 20,
            'retry_operations': 1000 if formal else 10, 'retry_repetitions': 3 if formal else 1,
            'delay_requests_per_condition': 3, 'timeout_requests_per_route': 1}


def number(v, positive=False):
    require(isinstance(v, (int, float)) and not isinstance(v, bool) and math.isfinite(v)
            and (v > 0 if positive else v >= 0), 'invalid numeric value')
    return v


def count(v):
    number(v)
    require(int(v) == v, 'noninteger count')
    return int(v)


def retry_summary(records, n, mode):
    require(mode in ['none', 'retry'], 'unknown retry mode')
    limit = 1 if mode == 'none' else 3
    require(len(records) == n and [r['operation_id'] for r in records] == list(range(1, n + 1)), 'missing/duplicate logical records')
    for r in records:
        number(r['duration_seconds'])
        a = count(r['attempts'])
        require(0 <= a <= limit and len(r['history']) == a, 'attempt/history mismatch')
        require(isinstance(r['deadline_exhausted'], bool), 'missing deadline flag')
        require(r['outcome'] in ['success', 'http_status', 'attempt_deadline', 'operation_deadline']
                and r['deadline_exhausted'] == (r['outcome'] == 'operation_deadline'), 'unexpected logical failure')
        for attempt in r['history']:
            status = count(attempt.get('status', 0))
            require(isinstance(attempt['injected'], bool), 'invalid injected flag')
            require((attempt['outcome'] == 'success' and status == 200 and not attempt['injected'])
                    or (attempt['outcome'] == 'http_status' and status == 503 and attempt['injected'])
                    or (attempt['outcome'] == 'attempt_deadline' and status in [0, 200, 503]
                        and attempt['injected'] == (status == 503)), 'unexpected retry attempt')
        require(all(attempt['outcome'] != 'success' for attempt in r['history'][:-1]), 'retry continued after success')
        last = r['history'][-1] if a else None
        require(count(r.get('status', 0)) == (last.get('status', 0) if last else 0), 'final/terminal status mismatch')
        if r['outcome'] != 'operation_deadline':
            require(last is not None and r['outcome'] == last['outcome'], 'final/terminal outcome mismatch')
            require(r['outcome'] == 'success' or a == limit, 'retry ended before attempt exhaustion')
        # Operation expiry may override an attempt, happen during backoff, or
        # precede the first attempt; retain the last status but count a failure.
    durations = sorted(r['duration_seconds'] for r in records)
    # Hyndman-Fan type 7: h=(n-1)*q, linear interpolation, all outcomes.
    h = (n - 1) * .95
    lo, hi = math.floor(h), math.ceil(h)
    p95 = durations[lo] + (h - lo) * (durations[hi] - durations[lo])
    successes = sum(r['outcome'] == 'success' for r in records)
    return {'successes': successes, 'failures': n - successes, 'success_rate': successes / n,
            'application_attempts': sum(r['attempts'] for r in records),
            'p95_operation_seconds': p95, 'deadline_exhaustions': sum(r['deadline_exhausted'] for r in records)}


def overhead(raw, pair):
    metrics = raw['metrics']
    def values(name):
        return metrics[name]['values']
    def c(name, phase):
        return count(values(name + '{phase:' + phase + '}')['count'])
    measured = c('cohort_completed', 'measure')
    require(measured > 0 and c('cohort_started', 'measure') == measured, 'interrupted measured cohort')
    warm = c('cohort_completed', 'warmup')
    require(warm > 0 and c('cohort_started', 'warmup') == warm, 'interrupted warmup')
    tail = c('cohort_completed', 'tail')
    require(c('cohort_started', 'tail') == tail, 'interrupted tail-start cohort')
    require(warm + measured + tail == count(values('cohort_started')['count'])
            == count(values('cohort_completed')['count']), 'phase/global population mismatch')
    require(count(values('cohort_started')['count']) == count(values('iterations')['count'])
            == count(values('cohort_completed')['count']), 'interrupted iterations')
    require(count(values('http_reqs')['count']) == count(values('cohort_completed')['count']), 'unexpected HTTP count')
    require(values('checks')['fails'] == 0 and values('http_req_failed')['passes'] == 0,
            'failed HTTP/check observations')
    require(c('cohort_bad_check', 'measure') == c('cohort_failed', 'measure') == 0, 'failed measured observations')
    require(values('cohort_bad_check')['count'] == values('cohort_failed')['count'] == 0, 'failed warmup/tail observations')
    require(count(values('checks')['passes']) == 2 * count(values('http_reqs')['count']), 'missing checks')
    for name, metric in metrics.items():
        if name == 'dropped_iterations':
            require(metric['values']['count'] == 0, 'dropped iterations')
        for result in metric.get('thresholds', {}).values():
            require(result['ok'] is True, 'failed correctness threshold')
    latency = values('cohort_http_duration{phase:measure}')
    p50, p95, p99 = [number(latency['p(%d)' % q]) for q in [50, 95, 99]]
    require(p50 <= p95 <= p99, 'unordered quantiles')
    first = number(values('cohort_clock_ms{point:first_start}')['min'])
    last = number(values('cohort_clock_ms{point:last_finish}')['max'])
    end = pair['warm_ms'] + pair['measure_ms']
    require(pair['warm_ms'] <= first < end and last >= first, 'wrong measured boundaries')
    execution = number(raw['state']['testRunDurationMs'], positive=True)
    require(end - 1 <= execution <= end + 3100 and last <= end + 3000, 'wrong duration or unfinished drain')
    seconds = pair['measure_ms'] / 1000
    return {'count': measured, 'warmup_count': warm, 'tail_count': tail, 'total_count': warm + measured + tail,
            'window_seconds': seconds,
            'first_start_ms': first, 'last_finish_ms': last, 'drain_tail_seconds': max(0, last - end) / 1000,
            'k6_execution_seconds': execution / 1000, 'requests_per_second': measured / seconds,
            'p50_ms': p50, 'p95_ms': p95, 'p99_ms': p99, 'metric': 'k6 http_req_duration (ms)',
            'checks_passed': measured * 2, 'unexpected_errors': 0}


def paired(rows):
    groups = {}
    for r in rows:
        key = (r['pair'], r['attempt'])
        g = groups.setdefault(key, {})
        require(r['path'] not in g, 'duplicate path')
        g[r['path']] = r
    differences = {}
    pairs = []
    for key, g in groups.items():
        require(set(g) == {'direct', 'proxy'}, 'incomplete comparable pair')
        d, p = g['direct'], g['proxy']
        require(d['vu'] == p['vu'], 'mixed VUs')
        diff = p['values']['p95_ms'] - d['values']['p95_ms']
        ratio = p['values']['requests_per_second'] / number(d['values']['requests_per_second'], positive=True)
        differences.setdefault(d['vu'], []).append((diff, ratio))
        pairs.append({'pair': key[0], 'attempt': key[1], 'direct': d['values'], 'proxy': p['values'],
                      'p95_difference_ms': diff, 'throughput_ratio': ratio})
    return {'pairs': pairs, 'by_vu': {str(v): {'differences_ms': [x[0] for x in xs],
            'median_difference_ms': statistics.median(x[0] for x in xs),
            'range_difference_ms': [min(x[0] for x in xs), max(x[0] for x in xs)],
            'throughput_ratios': [x[1] for x in xs], 'median_ratio': statistics.median(x[1] for x in xs)}
            for v, xs in differences.items()}}


def child_environment():
    # No inherited k6 options, proxy credentials, Go flags, workspace or toolchain switching.
    env = {k: os.environ[k] for k in ['PATH', 'HOME', 'TMPDIR', 'SYSTEMROOT'] if k in os.environ}
    env.update(GOWORK='off', GOTOOLCHAIN='local', CGO_ENABLED='1', GOFLAGS='',
               PYTHONDONTWRITEBYTECODE='1', K6_NO_USAGE_REPORT='true', K6_WEB_DASHBOARD='false',
               K6_AUTO_EXTENSION_RESOLUTION='false', K6_TRACES_OUTPUT='none')
    return env


def command(args, cwd, env=None, timeout=10):
    p = subprocess.Popen(args, cwd=cwd, env=env or child_environment(), stdout=subprocess.PIPE,
                         stderr=subprocess.PIPE, text=True, start_new_session=True)
    try:
        stdout, stderr = p.communicate(timeout=timeout)
    except BaseException as error:
        # Build/tool descendants share this owned session. Killing only their
        # parent can leave compilers or pipe holders alive after interruption.
        try:
            try:
                os.killpg(p.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            p.communicate(timeout=3)
        except BaseException as cleanup:
            raise ValueError('tool failure: %s; cleanup: %s' % (error, cleanup)) from error
        raise
    try:
        os.killpg(p.pid, 0)
    except ProcessLookupError:
        pass
    else:
        os.killpg(p.pid, signal.SIGKILL)
        raise ValueError('tool process group survives completion: ' + args[0])
    require(p.returncode == 0, 'command exit %d: %s\n%s' % (p.returncode, args[0], stderr[:1000]))
    return stdout.strip()


def tools(k6, kind):
    p = Path(k6).resolve()
    version = command([str(p), 'version'], ROOT)
    require(re.match(r'k6 v' + re.escape(K6_VERSION) + r'\s', version), 'wrong k6 version')
    go = command(['go', 'env', 'GOVERSION', 'GOHOSTOS', 'GOHOSTARCH', 'GOOS', 'GOARCH', 'CGO_ENABLED'], ROOT).splitlines()
    require(go[0] == GO_VERSION and go[1:3] == go[3:5] and go[5] == '1', 'wrong/non-native Go/cgo')
    architecture = 'arm64' if platform.machine() == 'arm64' else 'amd64' if platform.machine() in ['x86_64', 'AMD64'] else platform.machine()
    require((go[1] + '/' + architecture) in version, 'k6 is not native')
    if kind == 'formal':
        require(platform.python_version() == PYTHON_VERSION, 'formal Python version differs from selected runtime')
        require(sys.platform == 'darwin' and architecture == 'arm64' and digest(p) == K6_MAC_SHA256,
                'formal protocol selects the verified official macOS ARM64 executable')
    return {'k6': version, 'k6_sha256': digest(p), 'go': go, 'python': platform.python_version(),
            'compiler': command(['clang' if sys.platform == 'darwin' else 'cc', '--version'], ROOT).splitlines()[0],
            'k6_path': str(p)}


def machine():
    info = {'os': platform.system(), 'os_release': platform.release(), 'architecture': platform.machine(),
            'cores': os.cpu_count(), 'cpu': 'unknown: unavailable', 'memory_bytes': 'unknown: unavailable',
            'execution': 'native; no container/emulation', 'power': 'unknown: operator records conditions'}
    if sys.platform == 'darwin':
        for key, name in [('machdep.cpu.brand_string', 'cpu'), ('hw.memsize', 'memory_bytes'), ('hw.physicalcpu', 'physical_cores')]:
            try:
                info[name] = command(['sysctl', '-n', key], ROOT)
            except (ValueError, OSError):
                pass
        try:
            info['os_version'] = command(['sw_vers', '-productVersion'], ROOT)
            power = command(['pmset', '-g', 'batt'], ROOT)
            info['power'] = re.sub(r'\(id=\d+\)', '(identifier omitted)', power)
        except (ValueError, OSError):
            pass
    return info


class Owner:
    def __init__(self, directory, ledger):
        self.directory, self.ledger, self.children = directory, ledger, []

    def launch(self, args, name):
        with (self.directory / (name + '.stdout')).open('xb') as out, (self.directory / (name + '.stderr')).open('xb') as err:
            p = subprocess.Popen(args, cwd=self.directory, env=child_environment(), stdout=out, stderr=err,
                                 start_new_session=True)
        record = {'command': args, 'name': name, 'pid': p.pid, 'exit': None}
        self.ledger.append(record)
        self.children.append((p, record))
        return p

    def wait(self, p, timeout, accepted=(0,)):
        code = p.wait(timeout=timeout)
        record = next(r for child, r in self.children if child is p)
        record['exit'] = code
        require(code in accepted, 'child %s exit %d' % (record['name'], code))
        return code

    def close(self):
        errors = []
        for p, record in reversed(self.children):
            try:
                if p.poll() is None:
                    p.send_signal(signal.SIGTERM)
                code = p.wait(timeout=8)
                record['exit'] = code
                try:
                    os.killpg(p.pid, 0)
                except ProcessLookupError:
                    pass
                else:
                    errors.append('owned process group survives: ' + record['name'])
                    os.killpg(p.pid, signal.SIGKILL)
                    record['harness_kill'] = True
                if code != 0 and not record.get('expected_nonzero'):
                    errors.append('cleanup %s exit %d' % (record['name'], code))
            except BaseException as error:
                errors.append('cleanup %s: %s' % (record['name'], error))
                try:
                    os.killpg(p.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                p.wait(timeout=3)
                record['harness_kill'] = True
                record['exit'] = p.returncode
        self.children.clear()
        return errors


def ports():
    listeners = []
    try:
        for _ in range(3):
            s = socket.socket()
            s.bind(('127.0.0.1', 0))
            listeners.append(s)
        return [s.getsockname()[1] for s in listeners]
    finally:
        for s in listeners:
            s.close()


def fetch(port, path):
    c = http.client.HTTPConnection('127.0.0.1', port, timeout=3)
    try:
        c.request('GET', path, headers={'Accept-Encoding': 'identity'})
        r = c.getresponse()
        incomplete = False
        try:
            body = r.read(65537)
            incomplete = r.length is not None and r.length != 0
        except http.client.IncompleteRead as e:
            body, incomplete = e.partial, True
        return r.status, dict(r.getheaders()), body, incomplete
    finally:
        c.close()


def ready(port, child, seconds=5):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        require(child.poll() is None, 'child exited before readiness')
        try:
            r = fetch(port, '/healthz')
            if r[0] == 200 and r[2] == b'ok\n':
                return
        except OSError:
            pass
        time.sleep(.02)
    raise ValueError('readiness timeout')


def accounting(port):
    status, _, body, incomplete = fetch(port, '/metrics')
    require(status == 200 and not incomplete, 'metrics unavailable')
    result = {'requests': 0, 'histogram': 0, 'outcomes': {}, 'actions': {}, 'errors': {}}
    for line in body.decode().splitlines():
        if not line or line.startswith('#'):
            continue
        match = re.fullmatch(r'([a-z_]+)\{(.*)\} ([0-9.eE+\-]+)', line)
        require(match is not None, 'malformed metric sample')
        name, labels, value = match.groups()
        value = float(value)
        labels = dict(re.findall(r'([a-z_]+)="([^"]*)"', labels))
        if name == 'faultproxy_request_duration_seconds_count':
            result['histogram'] += value
        for family, field, label in [('faultproxy_requests_total', 'outcomes', 'outcome'),
                                     ('faultproxy_injected_faults_total', 'actions', 'kind'),
                                     ('faultproxy_upstream_errors_total', 'errors', 'kind')]:
            if name == family:
                result[field][labels[label]] = result[field].get(labels[label], 0) + value
                if field == 'outcomes':
                    result['requests'] += value
    return result


def settled(up, admin, expected=None):
    deadline = time.monotonic() + 3
    while time.monotonic() < deadline:
        a = accounting(admin)
        s = json.loads(fetch(up, '/stats')[2])
        if s['active'] == 0 and a['histogram'] == a['requests'] and (expected is None or a['requests'] == expected):
            time.sleep(.02)
            if accounting(admin) == a and json.loads(fetch(up, '/stats')[2]) == s:
                return {'metrics': a, 'upstream': s}
        time.sleep(.02)
    raise ValueError('terminal accounting did not settle')


def build(directory, manifest):
    result = {}
    for name, package in [('proxy', './cmd/faultproxy'), ('upstream', './examples/upstream'), ('client', './examples/retry-client')]:
        target = directory / name
        args = ['go', 'build', '-o', str(target), package]
        command(args, ROOT, timeout=60)
        info = command(['go', 'version', '-m', str(target)], ROOT)
        require('-race=true' not in info and '-cover' not in info and '-gcflags' not in info, 'instrumented build')
        manifest['builds'][name] = {'sha256': digest(target), 'command': args, 'exit': 0, 'build_info': info}
        result[name] = target
    return result


def start(owner, binaries, config, endpoint):
    up, data, admin = endpoint
    upstream = owner.launch([str(binaries['upstream']), '--listen=127.0.0.1:%d' % up, '--delay=3s'], 'upstream')
    ready(up, upstream)
    args = [str(binaries['proxy']), '--upstream=http://127.0.0.1:%d' % up, '--config=' + str(config),
            '--listen=127.0.0.1:%d' % data, '--admin-listen=127.0.0.1:%d' % admin]
    command(args + ['--check-config'], owner.directory)
    proxy = owner.launch(args, 'proxy')
    ready(admin, proxy)


def stop(owner, endpoint):
    errors = owner.close()
    for port in endpoint:
        try:
            with socket.socket() as s:
                s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
                s.bind(('127.0.0.1', port))
        except OSError as error:
            errors.append('port %d not released: %s' % (port, error))
    require(not errors, '; '.join(errors))


def reconcile_nth(records, evidence, n, mode):
    summary = retry_summary(records, n, mode)
    m, s = evidence['metrics'], evidence['upstream']
    actual = count(m['requests'])
    injected = count(m['actions'].get('status', 0))
    marked = sum(h['injected'] for r in records for h in r['history'])
    policy_failure = any(r['outcome'] in ['attempt_deadline', 'operation_deadline'] for r in records)
    require(m['histogram'] == actual and s['active'] == 0 and m['errors'] == {}, 'retry terminal/upstream events mismatch')
    require(set(m['actions']) <= {'status'}, 'unexpected retry action')
    if not policy_failure:
        require(m['outcomes'] == {'upstream_response': actual - actual // 5, 'synthetic_status': actual // 5}
                and injected == actual // 5 and marked == injected, 'N=5 outcomes/actions mismatch')
        require(s == {'calls': actual - injected, 'active': 0, 'cancelled': 0}, 'upstream reconciliation mismatch')
        require(summary['application_attempts'] == actual, 'application/wire replay discrepancy invalidates controlled cohort')
    else:
        # Policy expiry is valid unsuccessful operation data. Cancelled attempts
        # can fail before admission/action/receipt; reconcile bounds explicitly.
        require(set(m['outcomes']) <= {'upstream_response', 'synthetic_status', 'client_cancelled',
                                      'downstream_error', 'incomplete_response'}
                and sum(m['outcomes'].values()) == actual, 'unexpected policy-failure outcome')
        require(marked <= injected <= actual // 5 and actual <= summary['application_attempts'], 'policy action/admission mismatch')
        require(0 <= s['cancelled'] <= s['calls'] <= actual - injected, 'policy upstream count mismatch')
        complete_upstream = sum(h['outcome'] == 'success' for r in records for h in r['history'])
        require(complete_upstream <= s['calls'], 'successful upstream observations missing')
    summary.update(physical_requests=actual, physical_attempts_per_operation=actual / n,
                   upstream_calls=s['calls'], synthetic_actions=injected, observed_client_markers=marked,
                   policy_failures=sum(r['outcome'] in ['attempt_deadline', 'operation_deadline'] for r in records))
    return summary


def validate_physical(physical, evidence, n):
    require(len(physical) == n and [r['id'] for r in physical] == list(range(1, n + 1)), 'missing physical records')
    require(all(r['ok'] and r['synthetic'] == (r['id'] % 5 == 0)
                and r['status'] == (503 if r['synthetic'] else 200)
                and r['body_sha256'] == hashlib.sha256(b'fault injected\n' if r['synthetic'] else BODY).hexdigest()
                for r in physical), 'physical mismatch')
    require(evidence['metrics'] == {'requests': n, 'histogram': n, 'outcomes': {'upstream_response': n * 4 // 5, 'synthetic_status': n // 5},
                                  'actions': {'status': n // 5}, 'errors': {}}
            and evidence['upstream'] == {'calls': n * 4 // 5, 'active': 0, 'cancelled': 0}, 'correctness evidence mismatch')


class FixedCountResponse(http.client.HTTPResponse):
    def _read_next_chunk_size(self):
        size = super()._read_next_chunk_size()
        require(size >= 0, 'invalid negative chunk size')
        return size

    def _read_and_discard_trailer(self):
        # The stdlib accepts EOF here. Require the framing terminator instead,
        # with bounded trailer bytes/lines as well as bounded body reads.
        remaining = 65536
        for _ in range(100):
            line = self.fp.readline(remaining + 1)
            require(len(line) <= remaining, 'chunked trailer exceeds 64 KiB limit')
            require(line.endswith(b'\r\n'), 'incomplete chunked trailer')
            if line == b'\r\n':
                return
            remaining -= len(line)
        raise ValueError('chunked trailer exceeds line limit')


def fixed_count_body(response, observed):
    while len(observed) <= 65536:
        # read1 preserves bytes from earlier reads on timeout/cancellation and
        # leaves at most one oversized sentinel byte in the retained evidence.
        part = response.read1(min(8192, 65537 - len(observed)))
        observed.extend(part)
        require(len(observed) <= 65536, 'fixed-count body exceeds 64 KiB limit')
        if not part:
            require(response.chunked or response.length in (None, 0), 'incomplete fixed-length response')
            response.close()
            return bytes(observed)  # Chunk terminator, declared length, or EOF.
        if not response.chunked and response.length == 0:
            response.close()  # Release the response for HTTPConnection reuse.
            return bytes(observed)


def collect_physical(directory, port, n):
    records, failure, primary, cleanup = [], None, None, None
    request_id, response = None, None
    observed, declared_length, framing = bytearray(), None, None
    connection = http.client.HTTPConnection('127.0.0.1', port, timeout=2)
    connection.response_class = FixedCountResponse
    try:
        for request_id in range(1, n + 1):
            response = None
            observed, declared_length, framing = bytearray(), None, None
            connection.request('GET', '/benchmark', headers={'Accept-Encoding': 'identity'})
            response = connection.getresponse()
            declared_length = None if response.chunked else response.length
            framing = 'chunked' if response.chunked else ('content_length' if declared_length is not None else 'connection_close')
            body = fixed_count_body(response, observed)
            synthetic = response.getheader('X-Faultproxy-Injected') == 'status'
            ok = (response.status == 503 and synthetic and body == b'fault injected\n') if request_id % 5 == 0 else (response.status == 200 and not synthetic and body == BODY)
            records.append({'id': request_id, 'status': response.status, 'synthetic': synthetic, 'ok': ok,
                            'body_sha256': hashlib.sha256(body).hexdigest()})
            require(ok, 'fixed-count response mismatch')
    except BaseException as error:
        primary = error
        failure = {'request_id': request_id, 'type': type(error).__name__, 'reason': str(error),
                   'cancelled': isinstance(error, (CollectionCancelled, KeyboardInterrupt))}
        if response is not None:
            failure['response_status'] = response.status
            failure['injected_header'] = response.getheader('X-Faultproxy-Injected') == 'status'
            failure['framing'] = framing
            failure['declared_content_length'] = declared_length
        # read1 returns chunk payload bytes separately; a chunked exception's
        # partial bytes can instead be a truncated delimiter, not body data.
        if response is not None and not response.chunked and isinstance(getattr(error, 'partial', None), bytes):
            observed.extend(error.partial[:65537 - len(observed)])
        if response is not None and (not records or records[-1]['id'] != request_id):
            failure['partial_body_sha256'] = hashlib.sha256(observed).hexdigest()
            failure['partial_body_bytes'] = len(observed)
        raise
    finally:
        try:
            connection.close()
        except BaseException as error:
            cleanup = error
        metadata = {'planned_requests': n, 'completed_responses': len(records), 'failure': failure,
                    'cleanup_error': str(cleanup) if cleanup else None,
                    'client_checks_passed': primary is None and cleanup is None and len(records) == n}
        try:
            write_json(directory / 'requests.json', records)
            write_json(directory / 'collection.json', portable_manifest(metadata))
        except BaseException as error:
            raise ValueError('fixed-count failure: %s; evidence persistence: %s' %
                             (primary or cleanup or 'none', error)) from error
        if cleanup is not None:
            raise ValueError('fixed-count failure: %s; connection cleanup: %s' %
                             (primary or 'none', cleanup)) from cleanup
    return records


def validate_overhead_counts(evidence, totals):
    proxy = 1 + totals['proxy']  # One preflight proxied byte check.
    calls = 2 + totals['proxy'] + totals['direct']
    require(evidence['metrics'] == {'requests': proxy, 'histogram': proxy,
            'outcomes': {'upstream_response': proxy}, 'actions': {}, 'errors': {}}
            and evidence['upstream'] == {'calls': calls, 'active': 0, 'cancelled': 0},
            'overhead accounting mismatch or unintended competing traffic')


def safe_raw(directory, reference):
    p = (directory / reference).resolve()
    require(p != directory.resolve() and directory.resolve() in p.parents, 'raw reference escapes dataset')
    require(p.is_file(), 'missing raw file')
    return p


def summarize(directory, formal=False):
    directory = Path(directory).resolve()
    m = load_json(directory / 'manifest.json')
    kind = 'formal' if formal else m['kind']
    require(kind in ['formal', 'smoke'] and m['kind'] == kind, 'smoke/test cannot become formal')
    require(m['schema'] == SCHEMA and m['protocol'] == PROTOCOL and m['plan'] == plan(kind), 'wrong/incomplete protocol')
    require(m['valid'] is True and not m['errors'] and not m['cleanup_errors'], 'invalid dataset')
    if kind == 'formal':
        formal_source(m['source'], m['expected_sha'])
        require(m['tools']['python'] == PYTHON_VERSION and m['tools']['k6_sha256'] == K6_MAC_SHA256
                and m['tools']['go'] == [GO_VERSION, 'darwin', 'arm64', 'darwin', 'arm64', '1'], 'mixed formal runtime')
        require(set(m['builds']) == {'proxy', 'upstream', 'client'}, 'missing formal builds')
    require(re.match(r'k6 v' + re.escape(K6_VERSION) + r'\s', m['tools']['k6']), 'mixed k6 version')
    require(m['conditions'] == CONDITIONS and m['source'] == m['final_source'], 'mixed conditions/source drift')
    require(m['tools'] == m['final_tools'], 'tool drift')
    for name in ['empty', 'nth', 'delay']:
        require(m['files'][name + '.yaml'] == m['source']['inputs']['benchmarks/' + name + '.yaml'], 'configuration copy identity mismatch')
    for name, info in m['builds'].items():
        require(m['files'][name] == info['sha256'] and '-race=true' not in info['build_info']
                and '-cover' not in info['build_info'] and '-gcflags' not in info['build_info'], 'binary identity/instrumentation mismatch')
    for name, hash in m['files'].items():
        require(digest(safe_raw(directory, name)) == hash, 'raw file identity changed')
    rows = []
    expected = [(p, path) for p in m['plan']['pairs'] for path in p['paths']]
    trials = m['overhead']
    require(len(trials) == len(expected), 'missing/extra overhead trials')
    for trial, (p, path) in zip(trials, expected):
        require(trial['pair'] == p['id'] and trial['path'] == path and trial['attempt'] == p['attempt'] and trial['vu'] == p['vu'], 'duplicate/missing/reordered pair/path')
        require(trial['source'] == m['source'] and trial['exit'] == 0 and trial['valid'], 'mixed/invalid trial')
        raw_path = safe_raw(directory, trial['raw'])
        require(trial['raw'] in m['files'], 'unhashed raw summary')
        values = overhead(load_json(raw_path), p)
        require(values == trial['values'], 'raw/derived mismatch')
        rows.append(trial)
    require(len(m['overhead_snapshots']) == len(m['plan']['pairs']), 'missing overhead reconciliation')
    for p, snapshot in zip(m['plan']['pairs'], m['overhead_snapshots']):
        require(snapshot['pair'] == p['id'], 'mixed overhead snapshot')
        totals = {r['path']: count(load_json(safe_raw(directory, r['raw']))['metrics']['http_reqs']['values']['count'])
                  for r in rows if r['pair'] == p['id']}
        validate_overhead_counts(snapshot, totals)
    retries = []
    expected_retry = [(r, mode) for r in range(1, m['plan']['retry_repetitions'] + 1) for mode in ['none', 'retry']]
    require(len(m['retry']) == len(expected_retry), 'incomplete retry plan')
    for trial, (rep, mode) in zip(m['retry'], expected_retry):
        require((trial['repetition'], trial['mode']) == (rep, mode), 'duplicate/mixed retry trial')
        require(trial['raw'] in m['files'], 'unhashed operations')
        records = [json.loads(line) for line in safe_raw(directory, trial['raw']).read_text().splitlines()]
        derived = reconcile_nth(records, trial['evidence'], m['plan']['retry_operations'], mode)
        require(derived == trial['values'], 'retry raw/derived mismatch')
        require(trial['source'] == m['source'] and trial['valid'] and trial['exit'] == (0 if derived['failures'] == 0 else 1), 'invalid retry exit/source')
        retries.append({'mode': mode, 'repetition': rep, **derived})
    correctness = m['correctness']
    require(correctness['count'] == m['plan']['correctness_requests'] and correctness['valid'], 'incomplete correctness')
    require(correctness['synthetic'] == correctness['count'] // 5 and correctness['upstream'] == correctness['count'] * 4 // 5, 'incorrect N=5 counts')
    require(correctness['raw'] in m['files'], 'unhashed correctness raw')
    physical = load_json(safe_raw(directory, correctness['raw']))
    validate_physical(physical, correctness['evidence'], correctness['count'])
    require(m['controls']['valid'] and len(m['controls']['delay']) == 2 * m['plan']['delay_requests_per_condition']
            and [(r['route'], r['status'], r['incomplete']) for r in m['controls']['timeouts']] == [('/slow', 504, False), ('/partial', 200, True)], 'incomplete controls')
    require(m['controls_raw'] in m['files'] and load_json(safe_raw(directory, m['controls_raw'])) == m['controls'], 'control raw/derived mismatch')
    for condition in ['zero', 'fixed']:
        rs = [r for r in m['controls']['delay'] if r['condition'] == condition]
        require([r['request'] for r in rs] == [1, 2, 3], 'missing/duplicate delay requests')
        for r in rs:
            number(r['duration_seconds'])
    result = {'kind': kind, 'source_sha': m['source']['sha'], 'dirty': bool(m['source']['status']),
              'overhead': paired(rows), 'retry': retries, 'correctness': correctness,
              'label': 'SMOKE ONLY, not performance evidence' if kind == 'smoke' else 'formal source-attributed workload results'}
    return result


def collect(kind, k6, output, expected=None, bad_check=False, disturbance=None):
    state = source()
    if kind == 'formal':
        formal_source(state, expected)
        require(not bad_check, 'deliberate failure is smoke-only')
        require(disturbance, 'record plugged-in/awake/resource conditions before formal collection')
    selected = tools(k6, kind)
    directory = output_path(output)
    directory.mkdir(mode=0o700)
    manifest = {'schema': SCHEMA, 'protocol': PROTOCOL, 'kind': kind, 'plan': plan(kind),
                'source': state, 'expected_sha': expected, 'tools': selected, 'conditions': CONDITIONS,
                'machine': machine(), 'operator_conditions': disturbance or 'smoke: no performance claims',
                'timestamp_utc': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()), 'timezone': 'UTC',
                'builds': {}, 'commands': [], 'overhead': [], 'retry': [], 'files': {},
                'errors': [], 'cleanup_errors': [], 'valid': False, 'invalid_pairs': []}
    active = None
    endpoint = None
    interrupted = False
    old_handlers = {}
    def cancel(signum, frame):
        nonlocal interrupted
        if not interrupted:
            interrupted = True
            raise CollectionCancelled('orchestration cancelled by signal %d' % signum)
    def unchanged():
        require(source() == state, 'source/input drift')
        require(tools(k6, kind) == selected, 'tool drift')
        for name, info in manifest['builds'].items():
            require(digest(directory / name) == info['sha256'], 'binary drift')
    def begin(name, config):
        nonlocal active
        unchanged()
        sub = directory / name
        sub.mkdir()
        active = Owner(sub, manifest['commands'])
        start(active, binaries, config, endpoint)
        return sub
    def finish():
        nonlocal active
        stop(active, endpoint)
        active = None
        unchanged()
    try:
        for sig in [signal.SIGINT, signal.SIGTERM]:
            old_handlers[sig] = signal.signal(sig, cancel)
        binaries = build(directory, manifest)
        unchanged()
        endpoint = ports()
        manifest['ports'] = dict(zip(['upstream', 'data', 'admin'], endpoint))
        up, data, admin = endpoint
        for config in ['empty', 'nth', 'delay']:
            raw = (ROOT / 'benchmarks' / (config + '.yaml')).read_bytes()
            (directory / (config + '.yaml')).write_bytes(raw)
        (directory / 'k6-config.json').write_text('{}\n')
        # Same authority map and both processes stay alive across each pair.
        for pair in manifest['plan']['pairs']:
            manifest['active_pair'] = pair['id']
            sub = begin(pair['id'] + '-attempt1', directory / 'empty.yaml')
            # These byte checks are excluded from k6 traffic and accounted separately.
            for port in [up, data]:
                status, headers, body, incomplete = fetch(port, '/benchmark')
                require(status == 200 and body == BODY and headers.get('Content-Length') == '1024'
                        and not incomplete, 'fixture direct/proxy bytes differ')
            for path in pair['paths']:
                trial = {'pair': pair['id'], 'path': path, 'vu': pair['vu'], 'attempt': 1,
                         'source': state, 'valid': False, 'exit': None}
                manifest['overhead'].append(trial)
                raw = sub / (path + '-summary.json')
                trial['raw'] = str(raw.relative_to(directory))
                args = [selected['k6_path'], '--config', str(directory / 'k6-config.json'), '--no-color',
                        'run', '--quiet', '--no-usage-report', '--include-system-env-vars=false',
                        '--new-machine-readable-summary=false', '--traces-output=none']
                for key, value in {'TARGET': 'http://127.0.0.1:%d' % (up if path == 'direct' else data),
                                   'VUS': pair['vu'], 'WARM_MS': pair['warm_ms'], 'MEASURE_MS': pair['measure_ms'],
                                   'RAW_SUMMARY': raw, 'BAD_CHECK': '1' if bad_check else '0',
                                   'SELF_TEST': '1' if kind == 'smoke' else '0'}.items():
                    args += ['--env', '%s=%s' % (key, value)]
                args.append(str(ROOT / 'benchmarks/workload.js'))
                started = time.monotonic()
                child = active.launch(args, 'k6-' + path)
                try:
                    trial['exit'] = active.wait(child, (pair['warm_ms'] + pair['measure_ms']) / 1000 + 15, accepted=(0, 99))
                    require(trial['exit'] == 0, 'k6 correctness/exit failure')
                    trial['values'] = overhead(load_json(raw), pair)
                    trial['valid'] = True
                finally:
                    trial['wall_seconds'] = time.monotonic() - started
                    if child.returncode is not None:
                        trial['exit'] = child.returncode
                        if child.returncode != 0:
                            next(r for p, r in active.children if p is child)['expected_nonzero'] = True
                unchanged()
            totals = {r['path']: count(load_json(directory / r['raw'])['metrics']['http_reqs']['values']['count'])
                      for r in manifest['overhead'] if r['pair'] == pair['id']}
            snapshot = {'pair': pair['id'], **settled(up, admin, 1 + totals['proxy'])}
            validate_overhead_counts(snapshot, totals)
            manifest.setdefault('overhead_snapshots', []).append(snapshot)
            finish()
            manifest.pop('active_pair')
        # Serial fixed physical requests, no application retries/redirects.
        sub = begin('correctness', directory / 'nth.yaml')
        n = manifest['plan']['correctness_requests']
        manifest['correctness_collection_raw'] = str((sub / 'collection.json').relative_to(directory))
        physical = collect_physical(sub, data, n)
        raw = sub / 'requests.json'
        evidence = settled(up, admin, n)
        validate_physical(physical, evidence, n)
        manifest['correctness'] = {'count': n, 'synthetic': n // 5, 'upstream': n * 4 // 5,
                                   'evidence': evidence, 'valid': True, 'concurrency': 1,
                                   'transport': 'Python HTTPConnection reused; no automatic replay/redirect/retry',
                                   'raw': str(raw.relative_to(directory))}
        finish()
        for rep in range(1, manifest['plan']['retry_repetitions'] + 1):
            for mode in ['none', 'retry']:
                sub = begin('retry-%s-r%d' % (mode, rep), directory / 'nth.yaml')
                n = manifest['plan']['retry_operations']
                args = [str(binaries['client']), '--url=http://127.0.0.1:%d/benchmark' % data,
                        '--mode=' + mode, '--operations=%d' % n]
                child = active.launch(args, 'operations')
                code = active.wait(child, n * 5 + 5, accepted=(0, 1))
                next(r for p, r in active.children if p is child)['expected_nonzero'] = code == 1
                raw = sub / 'operations.stdout'
                records = [json.loads(line) for line in raw.read_text().splitlines()]
                evidence = settled(up, admin)
                values = reconcile_nth(records, evidence, n, mode)
                require(code == (0 if values['failures'] == 0 else 1), 'retry exit mismatch')
                manifest['retry'].append({'mode': mode, 'repetition': rep, 'source': state, 'valid': True,
                                          'exit': code, 'values': values, 'evidence': evidence,
                                          'raw': str(raw.relative_to(directory))})
                finish()
        controls = {'delay': [], 'timeouts': [], 'valid': False}
        manifest['controls'] = controls
        for condition in ['zero', 'fixed']:
            begin('delay-' + condition, directory / ('empty.yaml' if condition == 'zero' else 'delay.yaml'))
            for i in range(manifest['plan']['delay_requests_per_condition']):
                start_time = time.monotonic()
                status, _, body, incomplete = fetch(data, '/benchmark')
                elapsed = time.monotonic() - start_time
                require(status == 200 and body == BODY and not incomplete, 'delay body mismatch')
                controls['delay'].append({'condition': condition, 'request': i + 1, 'duration_seconds': elapsed})
            controls[condition + '_evidence'] = settled(up, admin, 3)
            require(controls[condition + '_evidence']['metrics']['actions'] == ({} if condition == 'zero' else {'delay': 3}), 'delay action mismatch')
            finish()
        begin('timeouts', directory / 'delay.yaml')
        for route, status, incomplete, body in [('/slow', 504, False, b'gateway timeout\n'), ('/partial', 200, True, b'prefix\n')]:
            r = fetch(data, route)
            require((r[0], r[2], r[3]) == (status, body, incomplete), 'timeout/incomplete mismatch')
            controls['timeouts'].append({'route': route, 'status': r[0], 'incomplete': r[3]})
        controls['timeout_evidence'] = settled(up, admin, 2)
        require(controls['timeout_evidence']['metrics']['outcomes'] == {'upstream_timeout': 1, 'incomplete_response': 1}
                and controls['timeout_evidence']['metrics']['errors'] == {'timeout': 2}, 'timeout causality mismatch')
        finish()
        controls['valid'] = True
        write_json(directory / 'controls.json', controls)
        manifest['controls_raw'] = 'controls.json'
        manifest['valid'] = True
    except BaseException as error:
        manifest['errors'].append(str(error))
        if len(manifest['overhead']) < 2 * len(manifest['plan']['pairs']) or any(not r['valid'] for r in manifest['overhead']):
            manifest['invalid_pairs'] = sorted(set(r['pair'] for r in manifest['overhead'] if not r['valid'])
                                               | ({manifest['active_pair']} if 'active_pair' in manifest else set()))
            for r in manifest['overhead']:
                if r['pair'] in manifest['invalid_pairs']:
                    r['valid'] = False
        manifest['valid'] = False
    finally:
        # Ignore repeated signals only during bounded owned teardown.
        for sig in old_handlers:
            signal.signal(sig, signal.SIG_IGN)
        if active is not None:
            manifest['cleanup_errors'].extend(active.close())
            if endpoint is not None:
                try:
                    stop(active, endpoint)
                except BaseException as error:
                    manifest['cleanup_errors'].append(str(error))
        try:
            manifest['final_source'] = source()
            manifest['final_tools'] = tools(k6, kind)
            require(manifest['final_source'] == state and manifest['final_tools'] == selected, 'source/tool drift at completion')
        except BaseException as error:
            manifest['errors'].append(str(error))
        manifest['valid'] = manifest['valid'] and not manifest['errors'] and not manifest['cleanup_errors']
        for p in directory.rglob('*'):
            if p.is_file():
                manifest['files'][str(p.relative_to(directory))] = digest(p)
        write_json(directory / 'manifest.json', portable_manifest(manifest))
        for sig, handler in old_handlers.items():
            signal.signal(sig, handler)
    if manifest['valid']:
        try:
            report = summarize(directory, formal=kind == 'formal')
            write_json(directory / 'derived.json', report)
        except BaseException as error:
            write_json(directory / 'validation-failure.json', {'valid': False, 'reason': str(error)})
            print('dataset validation failed:', error, file=sys.stderr)
            return 1
        print('PASS:', kind, 'dataset validated; children reaped and ports released:', directory)
        return 0
    print('INVALID:', directory, '; '.join(manifest['errors'] + manifest['cleanup_errors']), file=sys.stderr)
    return 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['plan', 'smoke', 'formal', 'summarize'])
    parser.add_argument('--k6', help='explicit selected-version native executable')
    parser.add_argument('--output', help='NEW external runtime directory with existing parent')
    parser.add_argument('--expected-sha')
    parser.add_argument('--bad-check', action='store_true', help='deliberate failed-check smoke only')
    parser.add_argument('--conditions', help='operator power/awake/resource conditions for formal collection')
    parser.add_argument('--dataset', help='existing external dataset to validate')
    parser.add_argument('--require-formal', action='store_true')
    args = parser.parse_args()
    try:
        if args.action == 'plan':
            if args.expected_sha:
                formal_source(source(), args.expected_sha)
            print(json.dumps(plan('formal'), indent=2))
            return 0
        if args.action == 'summarize':
            require(args.dataset, '--dataset required')
            print(json.dumps(summarize(args.dataset, args.require_formal), indent=2, allow_nan=False))
            return 0
        require(args.k6 and args.output, '--k6 and --output required')
        return collect(args.action, args.k6, args.output, args.expected_sha, args.bad_check, args.conditions)
    except (ValueError, OSError, subprocess.SubprocessError, KeyError) as error:
        print('benchmark:', error, file=sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
