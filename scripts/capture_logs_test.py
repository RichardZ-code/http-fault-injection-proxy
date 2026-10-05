"""Owned-resource and output-failure checks for the external capture wrapper."""

import errno
import fcntl
import json
import os
import resource
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

import capture_logs


class CaptureTest(unittest.TestCase):
    def run_capture(self, program, setup_collector=None):
        pids = []
        descriptors = set()
        pipes = set()
        pairs = []
        observed = {"drained": 0, "backpressure_drops": 0}
        popen = subprocess.Popen
        fork = os.fork
        pipe = os.pipe
        set_blocking = os.set_blocking
        read = os.read
        write = os.write
        fcntl_call = fcntl.fcntl
        inherited = {fd: fcntl.fcntl(fd, fcntl.F_GETFL) for fd in (0, 1, 2)}
        handlers = {sig: signal.getsignal(sig) for sig in (signal.SIGINT, signal.SIGTERM)}

        def own_pipe():
            pair = pipe()
            pairs.append(pair)
            pipes.update(pair)
            descriptors.update(pair)
            return pair

        def own_nonblocking(fd, blocking):
            # Observe every flag-changing call, not merely restored end flags.
            self.assertIn(fd, pipes)
            self.assertNotIn(fd, inherited)
            self.assertFalse(blocking)
            for inherited_fd, flags in inherited.items():
                self.assertEqual(fcntl.fcntl(inherited_fd, fcntl.F_GETFL), flags)
            set_blocking(fd, blocking)

        def checked_fcntl(fd, operation, *args):
            if operation == fcntl.F_SETFL:
                self.assertIn(fd, pipes)
                self.assertNotIn(fd, inherited)
            return fcntl_call(fd, operation, *args)

        def observe_drain(fd, count):
            data = read(fd, count)
            if len(pairs) >= 2 and fd == pairs[1][0]:
                observed["drained"] += len(data)
            return data

        def observe_write(fd, data):
            try:
                return write(fd, data)
            except BlockingIOError:
                if pairs and fd == pairs[0][1]:
                    observed["backpressure_drops"] += 1
                raise

        def own_fork():
            pid = fork()
            if pid:
                pids.append(pid)
                if setup_collector:
                    setup_collector(pid)
            return pid

        def own_child(*args, **kwargs):
            child = popen(*args, **kwargs)
            pids.append(child.pid)
            return child

        with tempfile.TemporaryDirectory() as directory:
            path = os.path.join(directory, "capture.jsonl")
            started = time.monotonic()
            with mock.patch.object(os, "pipe", own_pipe), \
                    mock.patch.object(os, "set_blocking", own_nonblocking), \
                    mock.patch.object(os, "fork", own_fork), \
                    mock.patch.object(os, "read", observe_drain), \
                    mock.patch.object(os, "write", observe_write), \
                    mock.patch.object(fcntl, "fcntl", checked_fcntl), \
                    mock.patch.object(subprocess, "Popen", own_child):
                code = capture_logs.capture(path, [sys.executable, "-c", program])
            elapsed = time.monotonic() - started
            with open(path, "rb") as output:
                data = output.read()
            self.assertEqual(os.stat(path).st_mode & 0o777, 0o600)
        self.assertEqual(len(pids), 2)
        for pid in pids:
            with self.assertRaises(ChildProcessError):
                os.waitpid(pid, os.WNOHANG)
        for fd in descriptors:
            with self.assertRaises(OSError) as raised:
                os.fstat(fd)
            self.assertEqual(raised.exception.errno, errno.EBADF)
        for fd, flags in inherited.items():
            self.assertEqual(fcntl.fcntl(fd, fcntl.F_GETFL), flags)
        for sig, handler in handlers.items():
            self.assertEqual(signal.getsignal(sig), handler)
        return code, data, elapsed, observed

    def test_owned_flags_status_and_reaping(self):
        program = (
            "import fcntl,json,os,sys; "
            "assert fcntl.fcntl(2, fcntl.F_GETFL) & os.O_NONBLOCK; "
            "os.write(2, json.dumps({'fixture': 'captured'}).encode()+b'\\n'); "
            "sys.exit(7)"
        )
        code, data, _elapsed, _observed = self.run_capture(program)
        self.assertEqual(code, 7)
        self.assertEqual(json.loads(data), {"fixture": "captured"})

    def test_output_failure_stops_child_and_preserves_status(self):
        fork = os.fork

        def limited_fork():
            pid = fork()
            if pid == 0:
                # A real file write fails; no simulated successful output.
                resource.setrlimit(resource.RLIMIT_FSIZE, (1, 1))
            return pid

        program = (
            "import os,signal,sys; "
            "signal.signal(signal.SIGTERM, lambda *_: sys.exit(7)); "
            "os.write(2,b'x'*1024); signal.pause()"
        )
        with mock.patch.object(os, "fork", limited_fork):
            code, data, elapsed, _observed = self.run_capture(program)
        self.assertEqual(code, 7)
        self.assertLessEqual(len(data), 1)
        self.assertLess(elapsed, 3)

    def test_stalled_output_does_not_stall_drain_or_child_exit(self):
        program = """
import os, sys, time
until = time.monotonic() + 0.3
while time.monotonic() < until:
    try:
        os.write(2, b'x' * 4096)
    except BlockingIOError:
        pass
sys.exit(7)
"""
        code, data, elapsed, observed = self.run_capture(
            program, lambda pid: os.kill(pid, signal.SIGSTOP))
        self.assertEqual(code, 7)
        self.assertEqual(data, b"")  # The file writer stayed stopped throughout.
        self.assertLess(elapsed, 3)  # Wrapper kills/reaps only its stalled writer.
        self.assertGreater(observed["drained"], 65536)
        self.assertGreater(observed["backpressure_drops"], 0)
        print("stalled owned writer: drained bytes={}, backpressure drops={}; "
              "child exit 7 preserved, both children reaped".format(
                  observed["drained"], observed["backpressure_drops"]))

    def test_existing_file_is_preserved_without_launch(self):
        with tempfile.TemporaryDirectory() as directory:
            path = os.path.join(directory, "existing")
            with open(path, "wb") as output:
                output.write(b"preserve")
            with mock.patch.object(subprocess, "Popen") as popen:
                with self.assertRaises(FileExistsError):
                    capture_logs.capture(path, ["unused"])
                popen.assert_not_called()
            with open(path, "rb") as output:
                self.assertEqual(output.read(), b"preserve")


if __name__ == "__main__":
    unittest.main(verbosity=2)
