#!/usr/bin/env python3
"""Synchronized demo startup ownership checks; no process retries."""

import errno
from pathlib import Path
import socket
import tempfile
import unittest
from unittest import mock

import demo


class DemoLifecycle(unittest.TestCase):
    def bare_cohort(self, directory):
        cohort = object.__new__(demo.Cohort)
        cohort.directory, cohort.name = directory, "fixture"
        cohort.capture = directory / "fixture.jsonl"
        cohort.up, cohort.data, cohort.admin = 1, 2, 3
        cohort.processes = [mock.Mock(pid=123)]
        cohort.processes[0].poll.return_value = None
        return cohort

    def test_ports_held_until_corresponding_launch(self):
        observed = []
        with tempfile.TemporaryDirectory() as temporary:
            def launch(cohort, command):
                released = [cohort.up] if not observed else [cohort.up, cohort.data, cohort.admin]
                for port in (cohort.up, cohort.data, cohort.admin):
                    with socket.socket() as probe:
                        if port in released:
                            probe.bind(("127.0.0.1", port))
                        else:
                            with self.assertRaises(OSError) as caught:
                                probe.bind(("127.0.0.1", port))
                            self.assertEqual(caught.exception.errno, errno.EADDRINUSE)
                observed.append(command)
                cohort.processes.append(mock.Mock(pid=123))
            with mock.patch.object(demo.Cohort, "launch", launch), \
                    mock.patch.object(demo.Cohort, "ready"), \
                    mock.patch.object(demo, "child_pids", return_value=[]):
                cohort = demo.Cohort(Path(temporary), "fixture", {"proxy": "proxy", "upstream": "up"}, "version: 1\nrules: []\n")
            self.assertEqual(len(observed), 2)
            self.assertEqual(len({cohort.up, cohort.data, cohort.admin}), 3)
            for port in (cohort.up, cohort.data, cohort.admin):
                with socket.socket() as probe:
                    probe.bind(("127.0.0.1", port))

    def test_startup_failure_preserved_and_reservations_released(self):
        failure = RuntimeError("original startup failure")
        ports = []
        def launch(cohort, command):
            ports.extend((cohort.up, cohort.data, cohort.admin))
            raise failure
        with tempfile.TemporaryDirectory() as temporary, \
                mock.patch.object(demo.Cohort, "launch", launch), \
                mock.patch.object(demo.Cohort, "close", side_effect=RuntimeError("cleanup failure")):
            with self.assertRaises(RuntimeError) as caught:
                demo.Cohort(Path(temporary), "fixture", {"upstream": "up"}, "version: 1\nrules: []\n")
        self.assertIs(caught.exception, failure)
        self.assertEqual(str(caught.exception.__cause__), "cleanup failure")
        for port in ports:
            with socket.socket() as probe:
                probe.bind(("127.0.0.1", port))

    def test_readiness_rechecks_child_after_fetch(self):
        with tempfile.TemporaryDirectory() as temporary:
            cohort = self.bare_cohort(Path(temporary))
            def fetch(*args):
                cohort.processes[0].poll.return_value = 1
                return 200, {}, b"ok\n", False
            with mock.patch.object(demo, "fetch", fetch), self.assertRaisesRegex(RuntimeError, '"exit": 1'):
                cohort.ready(2, "/healthz")

    def test_incomplete_health_never_ready(self):
        with tempfile.TemporaryDirectory() as temporary:
            cohort = self.bare_cohort(Path(temporary))
            def until(predicate, message):
                self.assertFalse(predicate())
            with mock.patch.object(demo, "fetch", return_value=(200, {}, b"ok\n", True)), \
                    mock.patch.object(demo, "until", until):
                cohort.ready(2, "/healthz")

    def test_startup_output_sanitized_and_bounded(self):
        with tempfile.TemporaryDirectory() as temporary:
            cohort = self.bare_cohort(Path(temporary))
            cohort.capture.write_bytes(b'{"msg":"startup failed","private":"secret"}\n' + b'secret\n' * 20000)
            state = cohort.startup_state()
            self.assertIn("startup failed", state)
            self.assertIn('"retained": 65536', state)
            self.assertNotIn("secret", state)
            self.assertNotIn(temporary, state)


if __name__ == "__main__":
    unittest.main(verbosity=2)
