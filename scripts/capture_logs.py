#!/usr/bin/env python3
"""POSIX launch/capture wrapper; Python 3.9+, standard library only.

Usage: python3 scripts/capture_logs.py NEW_LOG_FILE -- FAULTPROXY [ARGS...]
The file is created exclusively with mode 0600. The proxy's exit status wins,
including when a capture failure asks it to stop. Capture is best-effort.
"""

import argparse
import os
import selectors
import signal
import subprocess
import time


def copy_output(reader, output):
    # Only this owned process can block on the capture file. The launcher keeps
    # draining proxy stderr even when the bounded pipe to this process is full.
    signal.signal(signal.SIGINT, signal.SIG_IGN)
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    try:
        while True:
            data = os.read(reader, 65536)
            if not data:
                return 0
            while data:
                written = os.write(output, data)
                if written == 0:
                    return 1
                data = data[written:]
    except OSError:
        return 1
    finally:
        os.close(reader)
        os.close(output)


def capture(path, command):
    child = None
    collector = None
    collector_status = None
    reader = writer = queue_reader = queue_writer = output = None
    pending_signals = []
    old_handlers = {}
    shutdown_requested = False

    def forward(signum, _frame):
        nonlocal shutdown_requested
        shutdown_requested = True
        if child is None:
            pending_signals.append(signum)
        elif child.poll() is None:
            try:
                child.send_signal(signum)
            except ProcessLookupError:
                pass

    def stop_for_capture_failure():
        if not shutdown_requested:
            forward(signal.SIGTERM, None)

    try:
        for signum in (signal.SIGINT, signal.SIGTERM):
            old_handlers[signum] = signal.signal(signum, forward)
        output = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        queue_reader, queue_writer = os.pipe()
        os.set_blocking(queue_writer, False)  # A newly created, owned pipe only.
        chunk_size = os.fpathconf(queue_writer, "PC_PIPE_BUF")
        collector = os.fork()
        if collector == 0:
            os.close(queue_writer)
            try:
                status = copy_output(queue_reader, output)
            except OSError:
                status = 1
            os._exit(status)
        os.close(queue_reader)
        queue_reader = None
        os.close(output)
        output = None

        reader, writer = os.pipe()
        os.set_blocking(reader, False)
        os.set_blocking(writer, False)  # Never touch inherited stdout/stderr.
        child = subprocess.Popen(command, stderr=writer, start_new_session=True)
        os.close(writer)
        writer = None
        for signum in pending_signals:
            forward(signum, None)
        pending_signals.clear()

        with selectors.DefaultSelector() as selector:
            selector.register(reader, selectors.EVENT_READ)
            while selector.get_map() or child.poll() is None:
                if collector_status is None:
                    waited, status = os.waitpid(collector, os.WNOHANG)
                    if waited:
                        collector_status = status
                        # The writer must stay alive until EOF. An early exit
                        # (including an output error) stops the proxy normally.
                        stop_for_capture_failure()
                for key, _events in selector.select(0.05):
                    try:
                        data = os.read(key.fd, 65536)
                    except BlockingIOError:
                        continue
                    except OSError:
                        data = b""
                        stop_for_capture_failure()
                    if not data:
                        selector.unregister(key.fd)
                        continue
                    if collector_status is not None:
                        continue  # Drain/discard after capture failure.
                    for offset in range(0, len(data), chunk_size):
                        try:
                            # One nonblocking attempt per chunk, no backlog.
                            # A full pipe drops the chunk; a short write drops
                            # its remainder. Either can fragment JSON records.
                            os.write(queue_writer, data[offset:offset + chunk_size])
                        except BlockingIOError:
                            pass
                        except OSError:
                            stop_for_capture_failure()
                            break
        returncode = child.wait()  # Reap even after an output failure.
        return returncode if returncode >= 0 else 128 - returncode
    except OSError:
        if child is None:
            raise
        # A launcher-side capture error must also preserve the child's result.
        # Its nonblocking stderr cannot hold shutdown behind an absent reader.
        stop_for_capture_failure()
        returncode = child.wait()
        return returncode if returncode >= 0 else 128 - returncode
    finally:
        if child is not None and child.poll() is None:
            stop_for_capture_failure()
            child.wait()  # Uses the proxy's shutdown policy, no proxy kill/retry.
        for fd in (reader, writer, queue_reader, queue_writer, output):
            if fd is not None:
                os.close(fd)
        if collector is not None and collector_status is None:
            # This allowance belongs only to the external file writer, after
            # the proxy has been reaped. It never extends the proxy's budget.
            deadline = time.monotonic() + 0.5
            while True:
                waited, _status = os.waitpid(collector, os.WNOHANG)
                if waited:
                    break
                if time.monotonic() >= deadline:
                    try:
                        os.kill(collector, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    os.waitpid(collector, 0)
                    break
                time.sleep(0.01)
        for signum, handler in old_handlers.items():
            signal.signal(signum, handler)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("log_file", help="new capture file (must not already exist)")
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    command = args.command
    if command[:1] == ["--"]:
        command = command[1:]
    if not command:
        parser.error("a faultproxy executable and its arguments are required")
    try:
        return capture(args.log_file, command)
    except OSError:
        # No fallback to possibly blocked inherited diagnostics. Failure before
        # launching the proxy has no child status to preserve.
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
