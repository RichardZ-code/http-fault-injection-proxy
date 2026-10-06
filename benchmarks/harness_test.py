#!/usr/bin/env python3
"""Arithmetic, owned-process and loopback framing tests, no performance traffic."""
import copy
from contextlib import contextmanager
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest import mock

import harness as h


@contextmanager
def loopback_response(wire, hold_open=False):
    """One owned real HTTP connection; EOF and all teardown are explicit."""
    listener = socket.socket()
    # Linux requires reuse on both the original bound socket and the rebind.
    listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    listener.bind(('127.0.0.1', 0))
    listener.listen(1)
    listener.settimeout(3)
    port = listener.getsockname()[1]
    errors = []
    release = threading.Event()
    def serve():
        try:
            connection, _ = listener.accept()
            listener.close()
            with connection:
                connection.settimeout(3)
                for message in wire if isinstance(wire, list) else [wire]:
                    request = b''
                    while b'\r\n\r\n' not in request:
                        part = connection.recv(4096)
                        if not part or len(request) + len(part) > 32768:
                            raise ValueError('missing/oversized fixture request')
                        request += part
                    connection.sendall(message)
                if hold_open:
                    release.wait(3)
                connection.shutdown(socket.SHUT_WR)
        except BaseException as error:
            errors.append(error)
        finally:
            listener.close()
    worker = threading.Thread(target=serve)
    worker.start()
    try:
        yield port
    finally:
        release.set()
        listener.close()
        worker.join(4)
        if worker.is_alive():
            raise AssertionError('loopback fixture did not terminate')
        if errors:
            raise AssertionError('loopback fixture failed: %s' % errors)
        with socket.socket() as released:
            released.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            released.bind(('127.0.0.1', port))


def raw_summary():
    # Explicit independent arithmetic fixture: two measured requests, one warmup.
    metrics = {}
    def counter(name, value):
        metrics[name] = {'type': 'counter', 'values': {'count': value}}
    for phase, n in [('warmup', 1), ('measure', 2), ('tail', 0)]:
        for name in ['cohort_started', 'cohort_completed']:
            counter(name + '{phase:' + phase + '}', n)
        for name in ['cohort_bad_check', 'cohort_failed']:
            counter(name + '{phase:' + phase + '}', 0)
    for name in ['cohort_started', 'cohort_completed', 'iterations', 'http_reqs']:
        counter(name, 3)
    for name in ['cohort_bad_check', 'cohort_failed', 'dropped_iterations']:
        counter(name, 0)
    metrics['checks'] = {'values': {'passes': 6, 'fails': 0}}
    metrics['http_req_failed'] = {'values': {'passes': 0, 'fails': 3}}
    metrics['cohort_http_duration{phase:measure}'] = {'values': {'p(50)': 10, 'p(95)': 19, 'p(99)': 19.8}}
    metrics['cohort_clock_ms{point:first_start}'] = {'values': {'min': 501}}
    metrics['cohort_clock_ms{point:last_finish}'] = {'values': {'max': 1025}}
    return {'metrics': metrics, 'state': {'testRunDurationMs': 1026}}


def operation(i, outcome='success', duration=1):
    return {'operation_id': i, 'attempts': 1, 'outcome': outcome,
            'status': 503 if outcome == 'http_status' else 200 if outcome == 'success' else 0,
            'deadline_exhausted': False, 'duration_seconds': duration,
            'history': [{'outcome': outcome, 'status': 503 if outcome == 'http_status' else 200 if outcome == 'success' else 0,
                         'injected': outcome == 'http_status'}]}


def retry_population(n, mode):
    records, physical = [], 0
    for i in range(1, n + 1):
        history = []
        for _ in range(1 if mode == 'none' else 3):
            physical += 1
            a = operation(i, 'http_status' if physical % 5 == 0 else 'success')['history'][0]
            history.append(a)
            if a['outcome'] == 'success':
                break
        records.append({'operation_id': i, 'attempts': len(history), 'outcome': history[-1]['outcome'],
                        'status': history[-1]['status'], 'deadline_exhausted': False,
                        'duration_seconds': i / n, 'history': history})
    injected = physical // 5
    evidence = {'metrics': {'requests': physical, 'histogram': physical,
                'outcomes': {'upstream_response': physical - injected, 'synthetic_status': injected},
                'actions': {'status': injected}, 'errors': {}},
                'upstream': {'calls': physical - injected, 'active': 0, 'cancelled': 0}}
    return records, evidence


def synthetic_dataset(directory, kind='smoke'):
    p = h.plan(kind)
    state = {'sha': 'a' * 40, 'status': '?? benchmark preparation', 'inputs': {'test': 'b' * 64}}
    tools = {'k6': 'k6 v2.3.0 (test arithmetic)', 'python': '3.9.6'}
    m = {'schema': 1, 'protocol': 'p09-v1', 'kind': kind, 'plan': p, 'valid': True,
         'errors': [], 'cleanup_errors': [], 'source': state, 'final_source': state,
         'builds': {}, 'tools': tools, 'final_tools': tools, 'conditions': h.CONDITIONS, 'files': {},
         'overhead': [], 'retry': [], 'controls': {'valid': True, 'delay': [{'condition': c, 'request': i, 'duration_seconds': .05}
         for c in ['zero', 'fixed'] for i in [1, 2, 3]],
         'timeouts': [{'route': '/slow', 'status': 504, 'incomplete': False},
                      {'route': '/partial', 'status': 200, 'incomplete': True}]}}
    m['overhead_snapshots'] = []
    for pair in p['pairs']:
        for path in pair['paths']:
            raw = raw_summary()
            if kind == 'formal':
                raw['metrics']['cohort_clock_ms{point:first_start}']['values']['min'] = 5001
                raw['metrics']['cohort_clock_ms{point:last_finish}']['values']['max'] = 35025
                raw['state']['testRunDurationMs'] = 35026
            name = (pair['id'] + '-' if kind == 'formal' else '') + path + '.json'
            h.write_json(directory / name, raw)
            m['overhead'].append({'pair': pair['id'], 'attempt': 1, 'path': path, 'vu': pair['vu'],
                                 'source': state, 'exit': 0, 'valid': True, 'raw': name,
                                 'values': h.overhead(raw, pair)})
        m['overhead_snapshots'].append({'pair': pair['id'], 'metrics': {'requests': 4, 'histogram': 4,
             'outcomes': {'upstream_response': 4}, 'actions': {}, 'errors': {}},
             'upstream': {'calls': 8, 'active': 0, 'cancelled': 0}})
    for repetition in range(1, p['retry_repetitions'] + 1):
        for mode in ['none', 'retry']:
            records, e = retry_population(p['retry_operations'], mode)
            name = mode + ('-r%d' % repetition if kind == 'formal' else '') + '.jsonl'
            (directory / name).write_text(''.join(json.dumps(r) + '\n' for r in records))
            m['retry'].append({'repetition': repetition, 'mode': mode, 'raw': name, 'source': state,
                               'exit': 0 if mode == 'retry' else 1, 'valid': True, 'evidence': e,
                               'values': h.reconcile_nth(records, e, p['retry_operations'], mode)})
    n = p['correctness_requests']
    physical = [{'id': i, 'ok': True, 'synthetic': i % 5 == 0, 'status': 503 if i % 5 == 0 else 200,
                 'body_sha256': h.hashlib.sha256(b'fault injected\n' if i % 5 == 0 else h.BODY).hexdigest()} for i in range(1, n + 1)]
    h.write_json(directory / 'physical.json', physical)
    m['correctness'] = {'count': n, 'synthetic': n // 5, 'upstream': n * 4 // 5, 'valid': True, 'raw': 'physical.json',
        'evidence': {'metrics': {'requests': n, 'histogram': n, 'outcomes': {'upstream_response': n * 4 // 5, 'synthetic_status': n // 5},
        'actions': {'status': n // 5}, 'errors': {}}, 'upstream': {'calls': n * 4 // 5, 'active': 0, 'cancelled': 0}}}
    h.write_json(directory / 'controls.json', m['controls'])
    m['controls_raw'] = 'controls.json'
    for name in ['empty', 'nth', 'delay']:
        cfg = directory / (name + '.yaml')
        cfg.write_text('calculation-test config')
        state['inputs']['benchmarks/' + name + '.yaml'] = h.digest(cfg)
    if kind == 'formal':
        # Synthetic parser fixtures only, never a collected or imported dataset.
        state.update(status='', committed_match=True)
        for name in ['benchmarks/harness.py', 'benchmarks/workload.js', 'examples/upstream/main.go',
                     'examples/retry-client/client.go', 'cmd/faultproxy/cli.go', 'go.mod', 'go.sum']:
            state['inputs'][name] = 'b' * 64
        m['expected_sha'] = state['sha']
        tools.update(go=[h.GO_VERSION, 'darwin', 'arm64', 'darwin', 'arm64', '1'], k6_sha256=h.K6_MAC_SHA256)
        for name in ['proxy', 'upstream', 'client']:
            (directory / name).write_bytes(b'synthetic parser fixture, not an executable')
            m['builds'][name] = {'sha256': h.digest(directory / name), 'build_info': 'synthetic ordinary build identity'}
    for f in directory.iterdir():
        m['files'][f.name] = h.digest(f)
    return m


class Calculations(unittest.TestCase):
    def test_formal_provenance_is_always_required(self):
        with tempfile.TemporaryDirectory() as d:
            directory = Path(d)
            baseline = synthetic_dataset(directory, 'formal')
            manifest = directory / 'manifest.json'
            manifest.write_text(json.dumps(baseline))
            for flags in [[], ['--require-formal']]:
                command = [sys.executable, str(h.ROOT / 'benchmarks/harness.py'), 'summarize', '--dataset', d] + flags
                result = subprocess.run(command, capture_output=True, text=True, timeout=5)
                self.assertEqual(result.returncode, 0, result.stderr)
            mutations = [('dirty', lambda m: m['source'].update(status=' M source')),
                         ('SHA', lambda m: m.update(expected_sha='b' * 40)),
                         ('builds', lambda m: m.update(builds={})),
                         ('runtime', lambda m: m['tools'].update(go=['wrong'])),
                         ('tool', lambda m: m['tools'].update(k6_sha256='b' * 64))]
            for name, mutate in mutations:
                broken = copy.deepcopy(baseline)
                mutate(broken)
                manifest.write_text(json.dumps(broken))
                for flags in [[], ['--require-formal']]:
                    with self.subTest(provenance=name, flags=flags):
                        result = subprocess.run([sys.executable, str(h.ROOT / 'benchmarks/harness.py'),
                            'summarize', '--dataset', d] + flags, capture_output=True, text=True, timeout=5)
                        self.assertEqual(result.returncode, 1, 'invalid formal provenance was accepted')

    def test_phase_populations_reconcile(self):
        pair = h.plan('smoke')['pairs'][0]
        raw = raw_summary()
        for metric in ['cohort_started', 'cohort_completed']:
            raw['metrics'][metric + '{phase:measure}']['values']['count'] = 100
        with self.assertRaisesRegex(ValueError, 'population'):
            h.overhead(raw, pair)
        raw = raw_summary()
        raw['metrics']['cohort_completed{phase:tail}']['values']['count'] = 1
        with self.assertRaises(ValueError):
            h.overhead(raw, pair)
        raw = raw_summary()
        for metric in ['cohort_started', 'cohort_completed']:
            raw['metrics'][metric + '{phase:tail}']['values']['count'] = 1
        with self.assertRaisesRegex(ValueError, 'population'):
            h.overhead(raw, pair)

    def test_valid_boundary_and_tail_populations(self):
        pair = h.plan('smoke')['pairs'][0]
        raw = raw_summary()
        # A measured-start request finishing at 1025ms stays measured, not tail.
        raw['metrics']['cohort_clock_ms{point:first_start}']['values']['min'] = 500
        self.assertEqual(h.overhead(raw, pair)['count'], 2)
        # An additional tail-start request has a separate start/completion pair.
        for metric in ['cohort_started', 'cohort_completed']:
            raw['metrics'][metric + '{phase:tail}']['values']['count'] = 1
        for metric in ['cohort_started', 'cohort_completed', 'iterations', 'http_reqs']:
            raw['metrics'][metric]['values']['count'] = 4
        raw['metrics']['checks']['values']['passes'] = 8
        raw['metrics']['http_req_failed']['values']['fails'] = 4
        result = h.overhead(raw, pair)
        self.assertEqual((result['count'], result['tail_count'], result['total_count']), (2, 1, 4))
        self.assertEqual((result['requests_per_second'], result['drain_tail_seconds']), (4, .025))

    def test_retry_terminal_history_consistency(self):
        records, evidence = retry_population(10, 'none')
        for r in records:
            r.update(outcome='success', status=200)
        with self.assertRaises(ValueError):
            h.reconcile_nth(records, evidence, 10, 'none')
        mismatched = operation(1)
        mismatched['status'] = 503
        with self.assertRaises(ValueError):
            h.retry_summary([mismatched], 1, 'none')
        resumed = operation(1)
        resumed.update(attempts=2, history=[resumed['history'][0], resumed['history'][0]])
        with self.assertRaises(ValueError):
            h.retry_summary([resumed], 1, 'retry')
        premature = operation(1, 'http_status')
        with self.assertRaises(ValueError):
            h.retry_summary([premature], 1, 'retry')
        with self.assertRaises(ValueError):
            h.retry_summary([operation(1, 'attempt_deadline')], 1, 'retry')
        wrong_flag = operation(1)
        wrong_flag['deadline_exhausted'] = True
        with self.assertRaises(ValueError):
            h.retry_summary([wrong_flag], 1, 'none')

    def test_valid_retry_deadline_transitions(self):
        success = operation(1)
        success.update(attempts=2, history=[operation(1, 'http_status')['history'][0], success['history'][0]])
        self.assertEqual(h.retry_summary([success], 1, 'retry')['successes'], 1)
        exhausted = operation(1, 'attempt_deadline')
        exhausted.update(attempts=3, history=exhausted['history'] * 3)
        self.assertEqual(h.retry_summary([exhausted], 1, 'retry')['failures'], 1)
        exhausted_status = operation(1, 'http_status')
        exhausted_status.update(attempts=3, history=exhausted_status['history'] * 3)
        self.assertEqual(h.retry_summary([exhausted_status], 1, 'retry')['failures'], 1)
        after_headers = operation(1, 'attempt_deadline')
        after_headers['status'] = after_headers['history'][0]['status'] = 200
        self.assertEqual(h.retry_summary([after_headers], 1, 'none')['failures'], 1)
        for terminal in ['success', 'http_status', 'attempt_deadline']:
            r = operation(1, terminal)
            r.update(outcome='operation_deadline', deadline_exhausted=True)
            self.assertEqual(h.retry_summary([r], 1, 'retry')['deadline_exhaustions'], 1)
        before_attempt = operation(1)
        before_attempt.update(outcome='operation_deadline', deadline_exhausted=True, status=0, attempts=0, history=[])
        self.assertEqual(h.retry_summary([before_attempt], 1, 'none')['failures'], 1)

    def test_exact_plan_and_membership(self):
        p = h.plan('formal')
        self.assertEqual([(r['vu'], r['repetition']) for r in p['pairs']],
                         [(v, r) for v in [1, 25, 100] for r in [1, 2, 3]])
        self.assertEqual(len(p['pairs']) * 2, 18)
        for i, pair in enumerate(p['pairs']):
            self.assertEqual(pair['paths'], ['direct', 'proxy'] if i % 2 == 0 else ['proxy', 'direct'])
            self.assertEqual((pair['warm_ms'], pair['measure_ms']), (5000, 30000))
        self.assertEqual((p['correctness_requests'], p['retry_operations'], p['retry_repetitions']), (1000, 1000, 3))

    def test_raw_units_tail_denominator_and_failures(self):
        p = h.plan('smoke')['pairs'][0]
        raw = raw_summary()
        r = h.overhead(raw, p)
        self.assertEqual((r['count'], r['requests_per_second'], r['window_seconds']), (2, 4, .5))
        self.assertEqual((r['p50_ms'], r['p95_ms'], r['p99_ms']), (10, 19, 19.8))
        self.assertEqual(r['drain_tail_seconds'], .025)
        for mut in [lambda x: x['metrics']['checks']['values'].update(fails=1),
                    lambda x: x['metrics']['cohort_started{phase:measure}']['values'].update(count=3),
                    lambda x: x['metrics']['cohort_http_duration{phase:measure}']['values'].pop('p(99)'),
                    lambda x: x['metrics']['cohort_http_duration{phase:measure}']['values'].update({'p(95)': float('nan')}),
                    lambda x: x['metrics']['dropped_iterations']['values'].update(count=1),
                    lambda x: x['metrics']['iterations']['values'].update(count=2),
                    lambda x: x['metrics']['cohort_clock_ms{point:first_start}']['values'].update(min=499),
                    lambda x: x['metrics']['cohort_clock_ms{point:first_start}']['values'].update(min=1000)]:
            bad = copy.deepcopy(raw)
            mut(bad)
            with self.assertRaises((ValueError, KeyError)):
                h.overhead(bad, p)

    def test_pair_arithmetic_is_not_difference_of_medians(self):
        rows = []
        for i, (d, p) in enumerate([(0, 100), (100, 101), (101, 0)]):
            for path, latency in [('direct', d), ('proxy', p)]:
                rows.append({'pair': str(i), 'attempt': 1, 'vu': 1, 'path': path,
                             'values': {'p95_ms': latency, 'requests_per_second': 10 if path == 'direct' else 5}})
        result = h.paired(rows)['by_vu']['1']
        self.assertEqual(result['differences_ms'], [100, 1, -101])
        self.assertEqual(result['median_difference_ms'], 1)
        self.assertEqual(result['range_difference_ms'], [-101, 100])
        self.assertEqual(result['median_ratio'], .5)
        self.assertNotEqual(result['median_difference_ms'], 0)  # Difference of independent medians.
        with self.assertRaises(ValueError):
            h.paired(rows[:-1])
        with self.assertRaises(ValueError):
            h.paired(rows + [rows[0]])
        rows[0]['values']['requests_per_second'] = 0
        with self.assertRaises(ValueError):
            h.paired(rows)
        # A replacement cannot splice one member of another attempt.
        rows[0]['values']['requests_per_second'] = 10
        rows[1]['attempt'] = 2
        with self.assertRaises(ValueError):
            h.paired(rows)

    def test_retry_failed_operation_type7(self):
        records = [operation(1, duration=1), operation(2, 'http_status', 9)]
        self.assertAlmostEqual(h.retry_summary(records, 2, 'none')['p95_operation_seconds'], 8.6)
        self.assertEqual(h.retry_summary(records, 2, 'none')['success_rate'], .5)
        self.assertEqual(h.retry_summary([operation(1, duration=3)], 1, 'none')['p95_operation_seconds'], 3)
        for bad in [records[:1], [records[0], records[0]]]:
            with self.assertRaises(ValueError):
                h.retry_summary(bad, 2, 'none')

    def test_retry_policy_failure_is_data(self):
        records = [operation(1, duration=.1), operation(2, 'attempt_deadline', 1.1)]
        records[1]['history'] = [{'outcome': 'attempt_deadline', 'injected': False}]
        evidence = {'metrics': {'requests': 2, 'histogram': 2, 'actions': {}, 'errors': {},
                               'outcomes': {'upstream_response': 1, 'client_cancelled': 1}},
                    'upstream': {'calls': 2, 'active': 0, 'cancelled': 1}}
        r = h.reconcile_nth(records, evidence, 2, 'none')
        self.assertEqual((r['failures'], r['policy_failures'], r['physical_requests']), (1, 1, 2))
        self.assertAlmostEqual(r['p95_operation_seconds'], 1.05)
        records[1]['outcome'] = 'operation_deadline'
        records[1]['deadline_exhausted'] = True
        self.assertEqual(h.reconcile_nth(records, evidence, 2, 'retry')['deadline_exhaustions'], 1)
        records[1]['history'][0]['outcome'] = 'transport_error'
        with self.assertRaises(ValueError):
            h.reconcile_nth(records, evidence, 2, 'retry')

    def test_dataset_rejection(self):
        with tempfile.TemporaryDirectory() as d:
            directory = Path(d)
            m = synthetic_dataset(directory)
            manifest = directory / 'manifest.json'
            manifest.write_text(json.dumps(m))
            self.assertEqual(h.summarize(directory)['kind'], 'smoke')
            with self.assertRaises(ValueError):
                h.summarize(directory, formal=True)
            mutations = [lambda x: x.update(kind='formal'), lambda x: x.update(valid=False),
                         lambda x: x['overhead'].pop(), lambda x: x['overhead'].append(x['overhead'][0]),
                         lambda x: x['overhead'][1].update(path='direct'),
                         lambda x: x['overhead'][0]['source'].update(sha='b' * 40),
                         lambda x: x['tools'].update(k6='k6 v1.8.1'),
                         lambda x: x['conditions'].update(capture=True),
                         lambda x: x['overhead'][0]['values'].update(p95_ms=42),
                         lambda x: x['overhead_snapshots'][0]['upstream'].update(calls=9),
                         lambda x: x['retry'].pop(),
                         lambda x: x['retry'][1].update(mode='none'),
                         lambda x: x['files'].update({'absent.json': 'a' * 64})]
            for mutate in mutations:
                bad = json.loads(json.dumps(m))
                mutate(bad)
                manifest.write_text(json.dumps(bad))
                with self.assertRaises((ValueError, KeyError)):
                    h.summarize(directory)
            manifest.write_text(json.dumps(m))
            (directory / 'direct.json').write_text('{')
            with self.assertRaises(ValueError):
                h.summarize(directory)


class GuardsAndOwnership(unittest.TestCase):
    def test_real_fixed_length_keep_alive_reuse(self):
        wire = b'HTTP/1.1 200 OK\r\nContent-Length: 1024\r\n\r\n' + h.BODY
        with tempfile.TemporaryDirectory() as d, loopback_response([wire, wire]) as port:
            records = h.collect_physical(Path(d), port, 2)
            self.assertEqual([r['id'] for r in records], [1, 2])
            self.assertTrue(all(r['ok'] for r in records))
            self.assertEqual(h.load_json(Path(d) / 'collection.json')['completed_responses'], 2)

    def test_real_read_timeout_keeps_observed_prefix(self):
        wire = b'HTTP/1.1 200 OK\r\nContent-Length: 1024\r\n\r\nprefix'
        with tempfile.TemporaryDirectory() as d, loopback_response(wire, hold_open=True) as port:
            with self.assertRaises(socket.timeout):
                h.collect_physical(Path(d), port, 1)
            metadata = h.load_json(Path(d) / 'collection.json')
            self.assertEqual(metadata['completed_responses'], 0)
            self.assertEqual(metadata['failure']['partial_body_bytes'], 6)
            self.assertEqual(metadata['failure']['declared_content_length'], 1024)
            self.assertEqual(metadata['failure']['partial_body_sha256'], h.hashlib.sha256(b'prefix').hexdigest())

    def test_real_complete_response_framing_and_body_boundary(self):
        for body in [h.BODY, b'x' * 65536]:
            messages = [
                b'Content-Length: %d\r\n\r\n' % len(body) + body,
                b'Transfer-Encoding: chunked\r\n\r\n%x\r\n' % len(body) + body + b'\r\n0\r\nChecked: yes\r\n\r\n',
                b'Connection: close\r\n\r\n' + body,
            ]
            for framing, message in zip(['content_length', 'chunked', 'connection_close'], messages):
                with self.subTest(framing=framing, bytes=len(body)), tempfile.TemporaryDirectory() as d, \
                     loopback_response(b'HTTP/1.1 200 OK\r\n' + message) as port, mock.patch.object(h, 'BODY', body):
                    records = h.collect_physical(Path(d), port, 1)
                    self.assertEqual(len(records), 1)
                    self.assertTrue(records[0]['ok'])
                    self.assertEqual(records[0]['body_sha256'], h.hashlib.sha256(body).hexdigest())
                    metadata = h.load_json(Path(d) / 'collection.json')
                    self.assertEqual(metadata['completed_responses'], 1)
                    self.assertTrue(metadata['client_checks_passed'])
                    self.assertIsNone(metadata['failure'])

    def test_real_chunked_truncation_keeps_partial_evidence(self):
        for body in [b'400\r\nprefix', b'6\r\nprefix\r', b'6\r\nprefix\r\n', b'6\r\nprefix\r\n0\r\n',
                     b'6\r\nprefix\r\n0\r\nChecked: yes\r\n']:
            with self.subTest(wire=body), tempfile.TemporaryDirectory() as d, \
                 loopback_response(b'HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n' + body) as port:
                with self.assertRaises((ValueError, h.http.client.IncompleteRead)):
                    h.collect_physical(Path(d), port, 1)
                self.assertEqual(h.load_json(Path(d) / 'requests.json'), [])
                metadata = h.load_json(Path(d) / 'collection.json')
                self.assertEqual(metadata['completed_responses'], 0)
                self.assertFalse(metadata['client_checks_passed'])
                failure = metadata['failure']
                self.assertEqual(failure['framing'], 'chunked')
                self.assertIsNone(failure['declared_content_length'])
                self.assertEqual(failure['partial_body_bytes'], 6)
                self.assertEqual(failure['partial_body_sha256'], h.hashlib.sha256(b'prefix').hexdigest())

    def test_real_oversized_responses_keep_bounded_evidence(self):
        for size in [65537, 65538]:
            body = b'x' * size
            messages = [
                b'Content-Length: %d\r\n\r\n' % size + body,
                b'Transfer-Encoding: chunked\r\n\r\n%x\r\n' % size + body + b'\r\n0\r\n\r\n',
                b'Connection: close\r\n\r\n' + body,
            ]
            for framing, message in zip(['content_length', 'chunked', 'connection_close'], messages):
                with self.subTest(framing=framing, size=size), tempfile.TemporaryDirectory() as d, \
                     loopback_response(b'HTTP/1.1 200 OK\r\n' + message) as port:
                    with self.assertRaisesRegex(ValueError, 'body exceeds 64 KiB'):
                        h.collect_physical(Path(d), port, 1)
                    self.assertEqual(h.load_json(Path(d) / 'requests.json'), [])
                    metadata = h.load_json(Path(d) / 'collection.json')
                    self.assertEqual(metadata['completed_responses'], 0)
                    self.assertFalse(metadata['client_checks_passed'])
                    failure = metadata['failure']
                    self.assertEqual(failure['framing'], framing)
                    self.assertEqual(failure['declared_content_length'], size if framing == 'content_length' else None)
                    self.assertEqual(failure['partial_body_bytes'], 65537)
                    self.assertEqual(failure['partial_body_sha256'], h.hashlib.sha256(body[:65537]).hexdigest())
        # A negative chunk size must not turn read1's positive bound into -1.
        wire = b'HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n-1\r\n' + b'x' * 65538
        with tempfile.TemporaryDirectory() as d, loopback_response(wire) as port:
            # HTTPResponse wraps invalid chunk-size ValueError as IncompleteRead.
            with self.assertRaises(h.http.client.IncompleteRead):
                h.collect_physical(Path(d), port, 1)
            metadata = h.load_json(Path(d) / 'collection.json')
            self.assertEqual(metadata['completed_responses'], 0)
            self.assertEqual(metadata['failure']['partial_body_bytes'], 0)

    def test_real_chunked_trailer_limits(self):
        for trailer in [b'x' * 65537, b'Checked: yes\r\n' * 100 + b'\r\n']:
            wire = (b'HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n400\r\n' + h.BODY +
                    b'\r\n0\r\n' + trailer)
            with self.subTest(trailer_bytes=len(trailer)), tempfile.TemporaryDirectory() as d, loopback_response(wire) as port:
                with self.assertRaisesRegex(ValueError, 'trailer exceeds'):
                    h.collect_physical(Path(d), port, 1)
                metadata = h.load_json(Path(d) / 'collection.json')
                self.assertEqual(metadata['completed_responses'], 0)
                self.assertEqual(metadata['failure']['partial_body_bytes'], len(h.BODY))
                self.assertEqual(metadata['failure']['partial_body_sha256'], h.hashlib.sha256(h.BODY).hexdigest())

    def test_real_fixed_length_truncation_invalidates_collection(self):
        wire = b'HTTP/1.1 200 OK\r\nContent-Length: 1024\r\nConnection: close\r\n\r\nprefix'
        with tempfile.TemporaryDirectory() as d, loopback_response(wire) as port:
            output = Path(d) / 'dataset'
            state = {'sha': 'a' * 40, 'status': '?? synthetic collection test', 'inputs': {}}
            selected = {'k6': 'k6 v2.3.0 (synthetic)', 'k6_path': '/missing'}
            reduced = h.plan('smoke')
            reduced.update(pairs=[], correctness_requests=1)
            with mock.patch.object(h, 'source', return_value=state), mock.patch.object(h, 'tools', return_value=selected), \
                 mock.patch.object(h, 'machine', return_value={}), mock.patch.object(h, 'build', return_value={}), \
                 mock.patch.object(h, 'start'), mock.patch.object(h, 'ports', return_value=[0, port, 0]), \
                 mock.patch.object(h, 'plan', return_value=reduced):
                self.assertEqual(h.collect('smoke', '/missing', output), 1)
            records = h.load_json(output / 'correctness/requests.json')
            self.assertEqual(records, [], 'truncated body was classified as completed')
            metadata = h.load_json(output / 'correctness/collection.json')
            self.assertEqual(metadata['completed_responses'], 0)
            self.assertFalse(metadata['client_checks_passed'])
            failure = metadata['failure']
            self.assertEqual(failure['declared_content_length'], 1024)
            self.assertEqual(failure['partial_body_bytes'], 6)
            self.assertEqual(failure['partial_body_sha256'], h.hashlib.sha256(b'prefix').hexdigest())
            self.assertEqual(failure['framing'], 'content_length')
            self.assertEqual(failure['response_status'], 200)
            self.assertIn('incomplete', failure['reason'])
            manifest = h.load_json(output / 'manifest.json')
            self.assertFalse(manifest['valid'])
            self.assertFalse(manifest['cleanup_errors'])
            self.assertFalse((output / 'derived.json').exists())
            for name in ['correctness/requests.json', 'correctness/collection.json']:
                self.assertEqual(manifest['files'][name], h.digest(output / name))
            for formal in [False, True]:
                with self.assertRaises(ValueError):
                    h.summarize(output, formal)

    def test_partial_fixed_count_failure_is_not_a_completed_record(self):
        with tempfile.TemporaryDirectory() as d:
            response = mock.Mock(status=200)
            response.chunked, response.length = False, 1024
            response.getheader.return_value = ''
            response.read1.side_effect = h.http.client.IncompleteRead(b'prefix', 1018)
            connection = mock.Mock()
            connection.getresponse.return_value = response
            with mock.patch.object(h.http.client, 'HTTPConnection', return_value=connection):
                with self.assertRaises(h.http.client.IncompleteRead):
                    h.collect_physical(Path(d), 1234, 3)
            self.assertEqual(h.load_json(Path(d) / 'requests.json'), [])
            metadata = h.load_json(Path(d) / 'collection.json')
            self.assertEqual(metadata['failure']['partial_body_sha256'], h.hashlib.sha256(b'prefix').hexdigest())
            self.assertEqual(metadata['failure']['partial_body_bytes'], 6)
            self.assertEqual(metadata['failure']['response_status'], 200)
            self.assertFalse(metadata['client_checks_passed'])
            connection.close.assert_called_once()

    def test_fixed_count_failure_keeps_available_records(self):
        for cancel in [False, True]:
            with self.subTest(cancellation=cancel), tempfile.TemporaryDirectory() as d:
                output = Path(d) / 'dataset'
                state = {'sha': 'a' * 40, 'status': '?? synthetic collection test', 'inputs': {}}
                selected = {'k6': 'k6 v2.3.0 (synthetic)', 'k6_path': '/missing'}
                reduced = h.plan('smoke')
                reduced.update(pairs=[], correctness_requests=3)
                first = mock.Mock(status=200)
                first.chunked, first.length = False, 0
                first.getheader.return_value = ''
                first.read1.return_value = h.BODY
                second = mock.Mock(status=200 if cancel else 502)
                second.chunked, second.length = False, 0
                second.getheader.return_value = ''
                def cancelled_read(*args):
                    os.kill(os.getpid(), signal.SIGTERM)
                if cancel:
                    second.length = 1024
                    def partial_then_cancel(*args):
                        if second.read1.call_count == 1:
                            return b'prefix'
                        cancelled_read()
                    second.read1.side_effect = partial_then_cancel
                second.read1.return_value = b'unexpected'
                connection = mock.Mock()
                connection.getresponse.side_effect = [first, second]
                with mock.patch.object(h, 'source', return_value=state), mock.patch.object(h, 'tools', return_value=selected), \
                     mock.patch.object(h, 'machine', return_value={}), mock.patch.object(h, 'build', return_value={}), \
                     mock.patch.object(h, 'start'), mock.patch.object(h, 'ports', return_value=[0, 0, 0]), \
                     mock.patch.object(h, 'plan', return_value=reduced), \
                     mock.patch.object(h.http.client, 'HTTPConnection', return_value=connection):
                    self.assertEqual(h.collect('smoke', '/missing', output), 1)
                manifest = h.load_json(output / 'manifest.json')
                self.assertFalse(manifest['valid'])
                self.assertFalse(manifest['cleanup_errors'])
                self.assertFalse((output / 'derived.json').exists())
                records_path = output / 'correctness/requests.json'
                self.assertTrue(records_path.exists(), 'available client records were discarded')
                records = h.load_json(records_path)
                self.assertEqual([r['id'] for r in records], [1] if cancel else [1, 2])
                self.assertEqual(records[0]['body_sha256'], h.hashlib.sha256(h.BODY).hexdigest())
                if not cancel:
                    self.assertEqual(records[-1]['status'], 502)
                    self.assertFalse(records[-1]['ok'])
                    self.assertEqual(records[-1]['body_sha256'], h.hashlib.sha256(b'unexpected').hexdigest())
                meta = h.load_json(output / 'correctness/collection.json')
                self.assertEqual(meta['failure']['request_id'], 2)
                self.assertEqual(meta['failure']['cancelled'], cancel)
                self.assertEqual(meta['failure']['response_status'], 200 if cancel else 502)
                if cancel:
                    self.assertEqual(meta['failure']['partial_body_bytes'], 6)
                    self.assertEqual(meta['failure']['partial_body_sha256'], h.hashlib.sha256(b'prefix').hexdigest())
                    self.assertEqual(meta['failure']['declared_content_length'], 1024)
                self.assertFalse(meta['client_checks_passed'])
                self.assertEqual((meta['planned_requests'], meta['completed_responses']), (3, len(records)))
                for name in ['correctness/requests.json', 'correctness/collection.json']:
                    self.assertEqual(manifest['files'][name], h.hashlib.sha256((output / name).read_bytes()).hexdigest())
                self.assertEqual(manifest['correctness_collection_raw'], 'correctness/collection.json')
                connection.close.assert_called_once()
                self.assertEqual(connection.request.call_count, 2)
                with self.assertRaises(ValueError):
                    h.summarize(output)

    def test_tool_timeout_cleans_descendants(self):
        with tempfile.TemporaryDirectory() as d:
            pidfile = Path(d) / 'tool-pids'
            script = ('import os,pathlib,subprocess,sys,time; '
                      'p=subprocess.Popen([sys.executable,"-c","import time; time.sleep(30)"]); '
                      'pathlib.Path(sys.argv[1]).write_text(str(os.getpid())+" "+str(p.pid)); time.sleep(30)')
            pids = []
            try:
                with self.assertRaises(subprocess.TimeoutExpired):
                    h.command([sys.executable, '-c', script, str(pidfile)], Path(d), timeout=.2)
                pids = [int(n) for n in pidfile.read_text().split()]
                deadline = h.time.monotonic() + .5
                alive = True
                while h.time.monotonic() < deadline:
                    try:
                        os.kill(pids[1], 0)
                    except ProcessLookupError:
                        alive = False
                        break
                    h.time.sleep(.01)
                self.assertFalse(alive, 'timed-out tool left its descendant running')
            finally:
                if pidfile.exists() and not pids:
                    pids = [int(n) for n in pidfile.read_text().split()]
                for pid in pids:
                    try:
                        os.kill(pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass

    def test_manifest_path_privacy(self):
        original = {'commands': [{'command': [str(h.ROOT / 'benchmarks/workload.js')]}],
                    'tools': {'k6_path': str(Path.home() / 'tools/k6')}, 'sha': 'a' * 40, 'exit': 0}
        result = h.portable_manifest(original)
        self.assertEqual(result['commands'][0]['command'], ['${SOURCE_ROOT}/benchmarks/workload.js'])
        self.assertEqual(result['tools']['k6_path'], '${HOME}/tools/k6')
        self.assertEqual((result['sha'], result['exit']), ('a' * 40, 0))
        self.assertEqual(original['commands'][0]['command'], [str(h.ROOT / 'benchmarks/workload.js')])

    def test_source_guards_without_repository_mutation(self):
        names = ['benchmarks/harness.py', 'benchmarks/workload.js', 'benchmarks/empty.yaml',
                 'benchmarks/nth.yaml', 'benchmarks/delay.yaml', 'examples/upstream/main.go',
                 'examples/retry-client/client.go', 'cmd/faultproxy/cli.go', 'go.mod', 'go.sum']
        state = {'sha': 'a' * 40, 'status': '', 'committed_match': True, 'inputs': dict.fromkeys(names, 'b' * 64)}
        h.formal_source(state, 'a' * 40)
        for expected in [None, 'main', 'a' * 39, 'b' * 40]:
            with self.assertRaises(ValueError):
                h.formal_source(state, expected)
        for status in [' M tracked', 'M  staged', '?? untracked']:
            with self.assertRaises(ValueError):
                h.formal_source({**state, 'status': status}, 'a' * 40)
        with self.assertRaises(ValueError):
            h.formal_source({**state, 'inputs': {}}, 'a' * 40)
        with self.assertRaises(ValueError):
            h.formal_source({**state, 'committed_match': False}, 'a' * 40)

    def test_external_and_no_overwrite(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d) / 'root'
            root.mkdir()
            link = Path(d) / 'link'
            link.symlink_to(root, target_is_directory=True)
            for p in [root / 'output', link / 'output', root]:
                with self.assertRaises(ValueError):
                    h.output_path(p, root)
            p = Path(d) / 'new'
            self.assertEqual(h.output_path(p, root), p.resolve())
            p.mkdir()
            with self.assertRaises(ValueError):
                h.output_path(p, root)
            with self.assertRaises(ValueError):
                h.safe_raw(p, '../outside')

    def test_missing_wrong_tool_no_traffic(self):
        with self.assertRaises(OSError):
            h.tools('/no/such/k6', 'smoke')
        with mock.patch.object(h, 'command', return_value='k6 v1.8.1'):
            with self.assertRaises(ValueError):
                h.tools('/usr/bin/true', 'smoke')
        with mock.patch.object(h, 'source', return_value={'sha': 'a' * 40, 'status': ' M file', 'inputs': {}}), mock.patch.object(h, 'tools') as tools:
            with self.assertRaises(ValueError):
                h.collect('formal', '/absent', '/absent', 'a' * 40)
            tools.assert_not_called()

    def test_child_failure_and_cleanup(self):
        with tempfile.TemporaryDirectory() as d:
            ledger = []
            owner = h.Owner(Path(d), ledger)
            child = owner.launch([sys.executable, '-c', 'raise SystemExit(7)'], 'failure')
            with self.assertRaises(ValueError):
                owner.wait(child, 3)
            self.assertEqual(ledger[0]['exit'], 7)
            self.assertTrue(owner.close())  # Nonzero is retained, not silently clean.
            self.assertIsNotNone(child.returncode)
            child = owner.launch([sys.executable, '-c', 'import time; time.sleep(30)'], 'cancel')
            self.assertTrue(owner.close())
            self.assertIsNotNone(child.returncode)
            self.assertFalse(owner.children)

    def test_failed_collection_keeps_metadata_and_reaps(self):
        state = {'sha': 'a' * 40, 'status': '?? smoke', 'inputs': {}}
        selected = {'k6': 'k6 v2.3.0 (synthetic)', 'k6_path': '/missing'}
        with tempfile.TemporaryDirectory() as d:
            for reason in ['readiness timeout', 'occupied port', 'orchestration cancelled', 'missing/truncated summary']:
                output = Path(d) / reason.replace('/', '-')
                children = []
                def fail_start(owner, *args):
                    children.append(owner.launch([sys.executable, '-c', 'import time; time.sleep(30)'], 'owned'))
                    if reason == 'orchestration cancelled':
                        os.kill(os.getpid(), signal.SIGTERM)
                    raise ValueError(reason)
                with mock.patch.object(h, 'source', return_value=state), mock.patch.object(h, 'tools', return_value=selected), \
                     mock.patch.object(h, 'machine', return_value={}), mock.patch.object(h, 'build', return_value={}), \
                     mock.patch.object(h, 'start', side_effect=fail_start):
                    self.assertEqual(h.collect('smoke', '/missing', output), 1)
                m = h.load_json(output / 'manifest.json')
                self.assertIn('cancelled' if reason == 'orchestration cancelled' else reason, m['errors'][0])
                self.assertFalse(m['valid'])
                self.assertIsNotNone(children[0].returncode)
                self.assertIsNotNone(m['commands'][0]['exit'])
                with self.assertRaises(ValueError):
                    h.summarize(output)
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / 'truncated.json'
            p.write_text('{')
            with self.assertRaises(ValueError):
                h.load_json(p)
            with self.assertRaises(OSError):
                h.load_json(p.with_name('missing.json'))

    def test_readiness_occupied_port(self):
        with socket.socket() as owned:
            owned.bind(('127.0.0.1', 0))
            port = owned.getsockname()[1]
            with socket.socket() as conflicting:
                with self.assertRaises(OSError):
                    conflicting.bind(('127.0.0.1', port))
            with mock.patch.object(h, 'fetch', side_effect=OSError('not ready')):
                with self.assertRaisesRegex(ValueError, 'readiness timeout'):
                    h.ready(port, mock.Mock(poll=lambda: None), seconds=.04)
            with self.assertRaisesRegex(ValueError, 'exited'):
                h.ready(port, mock.Mock(poll=lambda: 7))


if __name__ == '__main__':
    unittest.main(verbosity=2)
