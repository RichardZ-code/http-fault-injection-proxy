"""Preparation guards; Docker and registry operations are local test doubles."""
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import textwrap
import unittest
from unittest import mock

SCRIPTS = Path(__file__).resolve().parent
sys.path.insert(0, str(SCRIPTS))
import release


def load_smoke():
    path = Path(os.environ.get('SMOKE_TEST_SOURCE', SCRIPTS / 'docker_smoke.py'))
    spec = importlib.util.spec_from_file_location('docker_smoke', path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def promotion_job():
    path = Path(os.environ.get('PROMOTION_TEST_SOURCE', SCRIPTS.parent / '.github/workflows/publish-container.yml'))
    text = path.read_text()
    return re.search(r'^  promote:\n((?:^    .*\n|^\n)+)', text, re.M).group(1)


class PromotionGuards(unittest.TestCase):
    def test_repository_version_serialization(self):
        job = promotion_job()
        self.assertIn('    concurrency:\n      group: container-promotion-${{ github.repository }}-${{ inputs.version }}\n      cancel-in-progress: false\n', job)
        # Accepted inputs have exactly one spelling per release version.
        self.assertEqual(release.identity('a' * 40, 'v1.2.3'), '1.2.3')
        for alias in ('V1.2.3', '1.2.3', 'v01.2.3', 'v1.02.3', 'v1.2.03', ' v1.2.3', 'v1.2.3\n', 'v1.2.3+build'):
            with self.subTest(alias=alias), self.assertRaises(ValueError):
                release.identity('a' * 40, alias)
        self.assertLess(job.index('scripts/release.py run-check'), job.index('docker manifest inspect'))
        self.assertLess(job.index('docker manifest inspect'), job.index('docker push'))

    def test_existing_tag_and_uncertain_absence_cannot_push(self):
        # Execute the protected job's actual absence guard and promotion tail.
        job = promotion_job()
        script = job[job.index('          if docker manifest inspect'):]
        script = textwrap.dedent(script.split('      - name: Preserve promotion diagnostics')[0])
        stub = '''
docker() {
  case "$1 $2" in
    "manifest inspect")
      case "$STATE" in
        existing) return 0 ;;
        absent) echo 'manifest unknown' >&2; return 1 ;;
        denied) echo 'unauthorized' >&2; return 1 ;;
      esac ;;
    "push "*) printf 'push\n' >>"$PUSHES"; STATE=existing; return 0 ;;
    "image inspect") echo '[]'; return 0 ;;
    "tag "*) return 0 ;;
    *) return 2 ;;
  esac
}
'''
        for state in ('existing', 'denied', 'absent'):
            with self.subTest(state=state), tempfile.TemporaryDirectory() as directory:
                pushes = Path(directory) / 'pushes'
                env = dict(os.environ, STATE=state, PUSHES=str(pushes), RUNNER_TEMP=directory,
                           GITHUB_STEP_SUMMARY=str(Path(directory) / 'summary'))
                completed = subprocess.run(['bash', '-c', 'set -euo pipefail\ntarget=fixture\nimage_id=fixture\n' + stub + script], env=env, capture_output=True, text=True, timeout=5)
                self.assertEqual(completed.returncode, 0 if state == 'absent' else 1, completed.stderr)
                self.assertEqual(pushes.read_text().splitlines() if pushes.exists() else [], ['push'] if state == 'absent' else [])
                if state == 'absent':
                    # A later serialized invocation sees the first promotion's tag.
                    env['STATE'] = 'existing'
                    second = subprocess.run(['bash', '-c', 'set -euo pipefail\ntarget=fixture\nimage_id=fixture\n' + stub + script], env=env, capture_output=True, text=True, timeout=5)
                    self.assertEqual(second.returncode, 1)
                    self.assertEqual(pushes.read_text().splitlines(), ['push'])


class DockerCleanup(unittest.TestCase):
    def exercise(self, fallback='timeout', network_error=False, report_error=False):
        smoke = load_smoke()
        calls = []
        owned = 'faultproxy-check-' + 'a' * 12
        proxy, upstream = owned + '-pass-through', owned + '-upstream'

        def docker(argv, **_kwargs):
            words = argv[1:]
            calls.append(words)
            status, stdout, stderr = 0, '', ''
            if words[0] == 'info':
                stdout = 'x86_64'
            elif words[:2] == ['image', 'inspect']:
                stdout = json.dumps([dict(Os='linux', Architecture='amd64', Id='fixture')])
            elif words[0] == 'run' and proxy in words:
                status, stderr = 1, 'original cohort failure'
            elif words == ['inspect', proxy]:
                status, stderr = 1, 'proxy inspection failure'
            elif words == ['inspect', upstream]:
                stdout = json.dumps([dict(State=dict(Running=False))])
            elif words == ['rm', '-f', proxy]:
                if fallback == 'timeout':
                    raise subprocess.TimeoutExpired(argv, 15)
                status, stderr = 1, 'fallback removal denied'
            elif words == ['network', 'rm', owned] and network_error:
                raise subprocess.TimeoutExpired(argv, 60)
            return subprocess.CompletedProcess(argv, status, stdout, stderr)

        def write(path, text, *args, **kwargs):
            if report_error and path.name == 'docker-smoke.json':
                raise OSError('report persistence denied')
            return original_write(path, text, *args, **kwargs)

        original_write = Path.write_text
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / 'evidence'
            argv = ['docker_smoke.py', '--image=fixture', '--upstream-image=fixture', '--client=fixture', '--output=' + str(output)]
            with mock.patch.object(sys, 'argv', argv), mock.patch.object(smoke.subprocess, 'run', side_effect=docker), \
                 mock.patch.object(smoke.signal, 'signal'), mock.patch.object(smoke.uuid, 'uuid4', return_value=mock.Mock(hex='a' * 32)), \
                 mock.patch.object(smoke.demo, 'available_port', side_effect=[50101, 50102, 50103]), \
                 mock.patch.object(smoke.demo, 'until', side_effect=lambda check, *_args: self.assertTrue(check())), \
                 mock.patch.object(smoke.demo, 'fetch', side_effect=lambda _port, path: (200, {}, b'ok\n' if path == '/healthz' else b'{}', False)), \
                 mock.patch.object(smoke.socket, 'socket') as socket_call, mock.patch.object(Path, 'write_text', autospec=True, side_effect=write) as writes:
                with self.assertRaisesRegex(RuntimeError, 'original cohort failure') as raised:
                    smoke.main()
                self.assertIn('cleanup', str(raised.exception))
                self.assertIn(['rm', '-f', proxy], calls)
                self.assertIn(['rm', upstream], calls)
                self.assertIn(['network', 'rm', owned], calls)
                self.assertEqual(socket_call.return_value.__enter__.return_value.bind.call_args_list,
                                 [mock.call(('127.0.0.1', p)) for p in (50101, 50102, 50103)])
                self.assertTrue(any(call.args[0].name == 'docker-smoke.json' for call in writes.call_args_list))
                # Every destructive command targets only these recorded resources.
                for words in calls:
                    if words[0] == 'rm':
                        self.assertIn(words[-1], (proxy, upstream))
                    if words[:2] == ['network', 'rm']:
                        self.assertEqual(words[-1], owned)
                if report_error:
                    self.assertIn('report persistence denied', str(raised.exception))
                else:
                    report = json.loads((output / 'docker-smoke.json').read_text())
                    self.assertFalse(report['passed'])
                    self.assertIn('original cohort failure', report['failure'])
                    self.assertTrue(any('timed out' in e if fallback == 'timeout' else 'fallback removal denied' in e for e in report['cleanup_errors']))
                    if network_error:
                        self.assertTrue(any('network' in e for e in report['cleanup_errors']))

    def test_two_container_fallback_timeout(self):
        self.exercise()

    def test_fallback_nonzero_exit(self):
        self.exercise(fallback='nonzero')

    def test_network_timeout_still_checks_ports_and_reports(self):
        self.exercise(network_error=True)

    def test_report_write_attempt_preserves_original_and_cleanup_errors(self):
        self.exercise(report_error=True)


if __name__ == '__main__':
    unittest.main()
