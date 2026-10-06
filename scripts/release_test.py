import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest import mock
import subprocess
import os

spec = importlib.util.spec_from_file_location('release', Path(__file__).with_name('release.py'))
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)
SHA = 'a' * 40


class ReleaseGates(unittest.TestCase):
    def test_inputs(self):
        self.assertEqual(release.identity(SHA, 'v0.1.0'), '0.1.0')
        for sha, version in [('main', 'v0.1.0'), (SHA, 'v01.1.0'), (SHA, '../v0.1.0'), (SHA, 'v1.0.0;echo secret')]:
            with self.assertRaises(ValueError):
                release.identity(sha, version)

    def test_hidden_source_change_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            subprocess.run(['git','init','-q',str(root)],check=True)
            (root/'input').write_text('reviewed')
            subprocess.run(['git','-C',str(root),'add','input'],check=True)
            subprocess.run(['git','-C',str(root),'-c','user.name=Fixture','-c','user.email=fixture@example.invalid','commit','-qm','fixture'],check=True)
            source=subprocess.check_output(['git','-C',str(root),'rev-parse','HEAD']).decode().strip()
            subprocess.run(['git','-C',str(root),'update-index','--assume-unchanged','input'],check=True)
            previous=Path.cwd()
            try:
                os.chdir(root)
                release.source_check(source,'v0.1.0')
                (root/'input').write_text('unreviewed')
                with self.assertRaisesRegex(ValueError,'source blob mismatch'):
                    release.source_check(source,'v0.1.0')
            finally:
                os.chdir(previous)

    def test_run_identity_rejects_foreign_failed_or_pr(self):
        valid=dict(repository={'full_name':'owner/repo'},head_repository={'full_name':'owner/repo'},head_sha=SHA,event='workflow_dispatch',path='.github/workflows/prepare-release.yml',status='completed',conclusion='success',run_attempt=1)
        variants=[valid,dict(valid,event='pull_request'),dict(valid,head_sha='b'*40),dict(valid,conclusion='failure'),dict(valid,run_attempt=2),dict(valid,head_repository={'full_name':'foreign/repo'})]
        for index, payload in enumerate(variants):
            with mock.patch.dict(os.environ,{'GH_TOKEN':'test-token'}), mock.patch.object(release.urllib.request,'urlopen') as call:
                call.return_value.__enter__.return_value=io.BytesIO(json.dumps(payload).encode())
                if index==0:release.run_check('owner/repo','123','1',SHA)
                else:
                    with self.assertRaises(ValueError):release.run_check('owner/repo','123','1',SHA)

    def test_archive_guards(self):
        for name, kind in [('../outside', tarfile.REGTYPE), ('/absolute', tarfile.REGTYPE), ('link', tarfile.SYMTYPE), ('device', tarfile.CHRTYPE)]:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as path:
                archive = Path(path) / 'bad.tar.gz'
                with tarfile.open(archive, 'w:gz') as stream:
                    info=tarfile.TarInfo(name); info.type=kind; info.linkname='/outside'
                    stream.addfile(info)
                with self.assertRaises(ValueError):
                    release.unpack(archive, Path(path)/'destination')
                self.assertFalse((Path(path)/'destination').exists())

    def test_archive_duplicate_and_mode(self):
        for mode, duplicate in [(0o4755, False), (0o644, True)]:
            stream=io.BytesIO()
            with tarfile.open(fileobj=stream, mode='w') as archive:
                for _ in range(2 if duplicate else 1):
                    info=tarfile.TarInfo('file');info.mode=mode;archive.addfile(info)
            stream.seek(0)
            with tarfile.open(fileobj=stream) as archive, self.assertRaises(ValueError):
                release.members(archive)

    def test_saved_config_is_distinct_from_store_id(self):
        with tempfile.TemporaryDirectory() as directory:
            path=Path(directory)/'image.tar'
            for architecture in ('amd64','arm64'):
                data=json.dumps(dict(os='linux',architecture=architecture)).encode()
                with tarfile.open(path,'w') as archive:
                    for name,content in [('config.json',data),('manifest.json',b'[{"Config":"config.json"}]')]:
                        info=tarfile.TarInfo(name);info.size=len(content);archive.addfile(info,io.BytesIO(content))
                if architecture=='amd64':
                    self.assertEqual(release.image_config(path),'sha256:'+release.hashlib.sha256(data).hexdigest())
                else:
                    with self.assertRaisesRegex(ValueError,'platform'):release.image_config(path)

    def test_public_checksum_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            names=[release.archive_name('v0.1.0',p) for p in ('darwin_arm64','linux_amd64','source')]
            for name in names:(root/name).write_bytes(b'approved')
            (root/'SHA256SUMS').write_text(''.join(release.sha(root/n)+'  '+n+'\n' for n in names))
            expected=release.sha(root/'SHA256SUMS')
            release.public_checks(root,SHA,'v0.1.0',expected)
            (root/names[0]).write_bytes(b'replaced')
            with self.assertRaisesRegex(ValueError,'asset checksum'):
                release.public_checks(root,SHA,'v0.1.0',expected)
            with self.assertRaisesRegex(ValueError,'checksum file'):
                release.public_checks(root,SHA,'v0.1.0','b'*64)

    def test_candidate_run_and_tampered_image(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            for p in ('darwin_arm64','linux_amd64','source'):
                (root/release.archive_name('v0.1.0',p)).write_bytes(b'archive')
            for p in ('darwin_arm64','linux_amd64'):
                (root/(p+'.json')).write_text(json.dumps(dict(source_sha=SHA,proposed_version='v0.1.0',platform=p)))
            config=json.dumps(dict(os='linux',architecture='amd64')).encode()
            with tarfile.open(root/'image.tar','w') as archive:
                for name,data in [('config.json',config),('manifest.json',b'[{"Config":"config.json"}]')]:
                    info=tarfile.TarInfo(name);info.size=len(data);archive.addfile(info,io.BytesIO(data))
            (root/'docker-smoke.json').write_text('{}')
            image=dict(source_sha=SHA,platform='linux/amd64',tested=True,image_id='sha256:'+'c'*64,config_digest=release.image_config(root/'image.tar'),archive_sha256=release.sha(root/'image.tar'))
            (root/'image.json').write_text(json.dumps(image))
            release.finalize(SHA,'v0.1.0',root,'owner/repo','123','1')
            h=release.sha(root/'PROVENANCE.json')
            release.candidate_check(root,SHA,'v0.1.0','owner/repo','123','1',h)
            with self.assertRaisesRegex(ValueError,'identity'):
                release.candidate_check(root,SHA,'v0.1.0','owner/repo','124','1',h)
            (root/'image.tar').write_bytes(b'rebuilt-image')
            with self.assertRaisesRegex(ValueError,'checksum'):
                release.candidate_check(root,SHA,'v0.1.0','owner/repo','123','1',h)


if __name__ == '__main__':
    unittest.main()
