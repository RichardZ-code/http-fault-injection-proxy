#!/usr/bin/env python3
"""Small native P07 correctness cohorts, production executables, Python 3.9+.

Run after building bin/faultproxy, bin/upstream and bin/retry-client. Default
temporary output is removed after inspection; --output retains a new directory.
No Docker claims.
"""

import argparse
import collections
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import signal
import socket
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parent.parent


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def require_outcomes(name, expected, evidence, stats):
    require(evidence["outcomes"] == expected,
            "terminal outcomes mismatch: " + json.dumps({
                "cohort": name, "expected_outcomes": expected,
                "actual_outcomes": evidence["outcomes"],
                "requests": evidence["requests"], "histogram": evidence["histogram"],
                "actions": evidence["actions"], "errors": evidence["errors"],
                "upstream": stats}, sort_keys=True))


def available_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def child_pids(parent):
    output = subprocess.check_output(["ps", "-axo", "pid=,ppid="], text=True, timeout=3)
    return [int(pid) for pid, ppid in (line.split() for line in output.splitlines())
            if int(ppid) == parent]


def fetch(port, path, headers=None, body=None, connections=None):
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
    if connections is not None:
        connections.append(connection)
    try:
        connection.request("GET", path, body=body, headers=headers or {})
        response = connection.getresponse()
        incomplete = False
        try:
            body = response.read((1 << 20) + 1)
            require(len(body) <= 1 << 20, "fixture body exceeds demo bound")
            incomplete = response.length is not None and response.length != 0
        except http.client.IncompleteRead as error:
            body, incomplete = error.partial, True
        return response.status, dict(response.getheaders()), body, incomplete
    finally:
        if connections is None:
            connection.close()


def until(predicate, message, seconds=5):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(0.02)
    raise RuntimeError(message)


def metrics(port):
    status, _, body, incomplete = fetch(port, "/metrics")
    require(status == 200 and not incomplete, "metrics unavailable")
    samples = []
    for line in body.decode().splitlines():
        if line.startswith("#") or not line:
            continue
        match = re.fullmatch(r'([a-z_]+)\{(.*)\} ([0-9.eE+\-]+)', line)
        require(match is not None, "unexpected metrics sample")
        labels = dict(re.findall(r'([a-z_]+)="([^"]*)"', match[2]))
        samples.append((match[1], labels, float(match[3])))
    return samples


def accounting(port):
    samples = metrics(port)
    def total(name, key=None):
        if key:
            result = collections.Counter()
            for metric, labels, value in samples:
                if metric == name:
                    result[labels[key]] += value
            return dict(result)
        return sum(value for metric, _, value in samples if metric == name)
    return {"requests": total("faultproxy_requests_total"),
            "histogram": total("faultproxy_request_duration_seconds_count"),
            "outcomes": total("faultproxy_requests_total", "outcome"),
            "actions": total("faultproxy_injected_faults_total", "kind"),
            "errors": total("faultproxy_upstream_errors_total", "kind")}


class Cohort:
    def __init__(self, directory, name, binaries, config):
        self.directory = directory
        self.name = name
        self.processes = []
        self.children = {}
        self.connections = []
        self.up, self.data, self.admin = [available_port() for _ in range(3)]
        require(len({self.up, self.data, self.admin}) == 3, "port allocation collision")
        self.capture = directory / (name + ".jsonl")
        self.config = directory / (name + ".yaml")
        self.config.write_text(config)
        self.config_hash = hashlib.sha256(self.config.read_bytes()).hexdigest()
        try:
            self.launch([str(binaries["upstream"]), "--listen=127.0.0.1:" + str(self.up), "--delay=3s"])
            self.ready(self.up, "/healthz")
            self.launch([sys.executable, str(ROOT / "scripts/capture_logs.py"), str(self.capture), "--",
                         str(binaries["proxy"]), "--upstream=http://127.0.0.1:" + str(self.up),
                         "--config=" + str(self.config), "--listen=127.0.0.1:" + str(self.data),
                         "--admin-listen=127.0.0.1:" + str(self.admin)])
            self.ready(self.admin, "/healthz")
            self.children[self.processes[-1].pid] = child_pids(self.processes[-1].pid)
        except BaseException:
            self.close()
            raise

    def launch(self, command):
        # Blocking inherited stderr remains unsupported by the proxy itself;
        # the capture helper, not this redirection, owns its nonblocking pipe.
        output = open(self.directory / (self.name + "-process-" + str(len(self.processes)) + ".log"), "xb")
        try:
            process = subprocess.Popen(command, cwd=self.directory, stdout=output, stderr=output,
                                       start_new_session=True)
        finally:
            output.close()
        self.processes.append(process)

    def ready(self, port, path):
        def check():
            require(all(p.poll() is None for p in self.processes), "child exited before readiness")
            try:
                status, _, body, _ = fetch(port, path)
                return status == 200 and body == b"ok\n"
            except OSError:
                return False
        until(check, "bounded readiness failed")

    def stats(self):
        status, _, body, _ = fetch(self.up, "/stats")
        require(status == 200, "stats unavailable")
        return json.loads(body)

    def fetch(self, path, headers=None, body=None):
        # Complete client reads can precede the checked flush and terminal
        # cleanup. Keep these owned cohort connections open until reconciliation.
        return fetch(self.data, path, headers, body, self.connections)

    def reconcile(self, count):
        until(lambda: accounting(self.admin)["requests"] == count,
              "terminal publication did not reconcile")
        evidence = accounting(self.admin)
        require(evidence["histogram"] == count, "histogram mismatch")
        for _ in range(3):
            require(accounting(self.admin) == evidence, "admin scrape consumed data state")
        return evidence

    def close(self):
        for connection in self.connections:
            connection.close()
        self.connections.clear()
        # Keep upstream alive while the production proxy/helper drains.
        failure = None
        for process in reversed(self.processes):
            owned = self.children.get(process.pid, [])
            try:
                if process.poll() is None:
                    owned = list(set(owned + child_pids(process.pid)))
                    process.send_signal(signal.SIGTERM)
                code = process.wait(timeout=8)
                require(code == 0, "owned child did not stop cleanly: exit " + str(code))
                try:
                    os.killpg(process.pid, 0)
                except ProcessLookupError:
                    pass
                else:
                    raise RuntimeError("owned descendants survived launcher exit")
                for pid in owned:
                    try:
                        os.kill(pid, 0)
                    except ProcessLookupError:
                        pass
                    else:
                        raise RuntimeError("owned capture child survived launcher exit")
            except BaseException as error:
                if failure is None:
                    failure = error
                # Each launcher owns a fresh session. Failed-demo protection
                # includes its proxy/writer descendants, never unrelated PIDs.
                # This kill cannot satisfy any successful shutdown assertion.
                for pid in owned:
                    try:
                        os.kill(pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.wait(timeout=3)
        self.processes.clear()
        if self.config.exists():
            require(hashlib.sha256(self.config.read_bytes()).hexdigest() == self.config_hash,
                    "configuration changed")
        for port in (self.up, self.data, self.admin):
            with socket.socket() as listener:
                listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
                listener.bind(("127.0.0.1", port))
        if failure:
            raise failure

    def logs(self, expected):
        records, malformed = [], 0
        data = self.capture.read_bytes()
        for line in data.splitlines():
            try:
                record = json.loads(line)
            except (ValueError, UnicodeError):
                malformed += 1
                continue
            if record.get("msg") == "request completed":
                records.append(record)
        require(len(records) == expected and malformed == 0, "demo capture incomplete or malformed; logs are lossy")
        fields = {"request_id", "method", "rule", "decision", "started_actions", "outcome", "cause", "upstream_status", "sent_status", "duration_seconds"}
        allowed = fields | {"time", "level", "msg", "sequence"}
        require(all(fields <= set(r) <= allowed for r in records), "unexpected access fields")
        for canary in (b"query-secret", b"body-secret", b"authorization-secret", b"cookie-secret"):
            require(canary not in data, "privacy canary leaked")
        print(json.dumps({"cohort": self.name, "capture": str(self.capture), "parsed_access": len(records), "malformed": malformed,
                          "outcomes": dict(collections.Counter(r["outcome"] for r in records)), "record": records[0]}))
        return records


def demonstration(binaries, directory):
    print(json.dumps({"artifacts": {name: {"path": str(path), "sha256": hashlib.sha256(path.read_bytes()).hexdigest()}
                                   for name, path in binaries.items()}, "python": sys.version.split()[0], "source": "local working tree"}))
    empty = "version: 1\nrules: []\n"
    nth = (ROOT / "examples/demo.yaml").read_text()
    for name, mode in (("pass-through", None), ("six-request", "none"), ("retry", "retry"), ("restart", "none")):
        cohort = Cohort(directory, name, binaries, empty if mode is None else nth)
        try:
            if mode is None:
                status, headers, body, incomplete = cohort.fetch("/ok?query-secret", {"Authorization": "authorization-secret", "Cookie": "cookie-secret"}, b"body-secret")
                require(status == 200 and body == b"fixture ok\n" and not incomplete and "X-Faultproxy-Injected" not in headers, "pass-through failed")
                attempts, successes, expected_calls = 1, 1, 1
            else:
                command = [str(binaries["client"]), "--url=http://127.0.0.1:" + str(cohort.data) + "/ok", "--mode=" + mode, "--operations=6"]
                run = subprocess.run(command, cwd=directory, capture_output=True, timeout=35)
                require(run.returncode == (0 if mode == "retry" else 1), "retry client exit mismatch")
                results = [json.loads(line) for line in run.stdout.splitlines()]
                require(len(results) == 6 and [r["operation_id"] for r in results] == list(range(1, 7)), "logical operation mismatch")
                attempts = sum(r["attempts"] for r in results)
                successes = sum(r["outcome"] == "success" for r in results)
                expected_calls = 6 if mode == "retry" else 4
                require((attempts, successes) == ((8, 6) if mode == "retry" else (6, 4)), "retry counts mismatch")
                if mode == "none":
                    require([r["status"] for r in results] == [200, 200, 503, 200, 200, 503], "N=3 sequence mismatch")
                require(sum(h["injected"] for r in results for h in r["history"]) == 2, "synthetic evidence missing")
                print(json.dumps({"cohort": name, "operations": results}))
            evidence = cohort.reconcile(attempts)
            stats = cohort.stats()
            require(stats == {"calls": expected_calls, "active": 0, "cancelled": 0}, "wire replay or unexpected fixture contact")
            require(evidence["errors"] == {}, "synthetic response invented upstream error")
            require_outcomes(name, {"upstream_response": 1} if mode is None else {"upstream_response": expected_calls, "synthetic_status": 2}, evidence, stats)
            require(evidence["actions"] == ({} if mode is None else {"status": 2}), "action counts mismatch")
            print(json.dumps({"cohort": name, "metrics": evidence, "upstream": stats}))
        finally:
            cohort.close()
        cohort.logs(attempts)

    delay = "version: 1\nupstream_timeout_ms: 500\nrules: [{id: delayed, path_prefix: /ok, faults: {delay_ms: 250}}]\n"
    cohort = Cohort(directory, "delay-timeout", binaries, delay)
    try:
        started = time.monotonic()
        status, _, body, incomplete = cohort.fetch("/ok")
        elapsed = time.monotonic() - started
        require(status == 200 and body == b"fixture ok\n" and not incomplete and elapsed >= 0.20, "configured delay missing")
        status, headers, body, incomplete = cohort.fetch("/error")
        require(status == 503 and body == b"fixture unavailable\n" and not incomplete and "X-Faultproxy-Injected" not in headers, "real upstream 503 lost")
        status, headers, body, incomplete = cohort.fetch("/slow")
        require(status == 504 and body == b"gateway timeout\n" and not incomplete and "X-Faultproxy-Injected" not in headers, "pre-header timeout failed")
        status, _, body, incomplete = cohort.fetch("/partial")
        require(status == 200 and incomplete and body == b"prefix\n", "post-header failure became replacement status")
        evidence = cohort.reconcile(4)
        until(lambda: cohort.stats()["active"] == 0, "fixture work did not cancel")
        stats = cohort.stats()
        require(stats == {"calls": 4, "active": 0, "cancelled": 2}, "upstream cleanup mismatch")
        require_outcomes("delay-timeout", {"upstream_response": 1, "upstream_http_error": 1, "upstream_timeout": 1, "incomplete_response": 1}, evidence, stats)
        require(evidence["actions"] == {"delay": 1} and evidence["errors"] == {"http_5xx": 1, "timeout": 2}, "event mismatch")
        print(json.dumps({"cohort": "delay-timeout", "observed_delay_seconds": elapsed, "metrics": evidence, "upstream": stats}))
    finally:
        cohort.close()
    cohort.logs(4)
    print("PASS: production native cohorts; processes reaped and ports rebound; no harness kill")


def main():
    interrupted = False
    def stop(_signum, _frame):
        nonlocal interrupted
        if not interrupted:
            interrupted = True
            # Unwind the cohort's finally block. subprocess.run also kills and
            # reaps its owned client on this failed-demo interruption.
            # InterruptedError is retried by Python's selector machinery.
            raise RuntimeError("demonstration interrupted")
    for signum in (signal.SIGINT, signal.SIGTERM):
        signal.signal(signum, stop)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--proxy", default=str(ROOT / "bin/faultproxy"))
    parser.add_argument("--upstream", default=str(ROOT / "bin/upstream"))
    parser.add_argument("--client", default=str(ROOT / "bin/retry-client"))
    parser.add_argument("--output", help="retain captures in this new private directory")
    args = parser.parse_args()
    binaries = {name: Path(getattr(args, name)).resolve() for name in ("proxy", "upstream", "client")}
    require(all(p.is_file() for p in binaries.values()), "build the three documented binaries first")
    if args.output:
        directory = Path(args.output).resolve()
        directory.mkdir(mode=0o700)  # Existing captures are never overwritten.
        demonstration(binaries, directory)
    else:
        with tempfile.TemporaryDirectory(prefix="faultproxy-p07-") as path:
            demonstration(binaries, Path(path))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
