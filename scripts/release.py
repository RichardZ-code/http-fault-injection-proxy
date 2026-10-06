#!/usr/bin/env python3
"""Nonpublishing candidate packaging and identity gates. Python 3.9+, stdlib."""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import tarfile
import urllib.request

PROJECT = 'http-fault-injection-proxy'


def require(ok, message):
    if not ok:
        raise ValueError(message)


def identity(source, version):
    require(re.fullmatch(r'[0-9a-f]{40}', source), 'full lowercase source SHA required')
    require(re.fullmatch(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)', version), 'version must be vMAJOR.MINOR.PATCH without leading zeros')
    return version[1:]


def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def run(command, **kwargs):
    return subprocess.check_output(command, timeout=120, **kwargs).decode().strip()


def source_check(source, version):
    identity(source, version)
    require(run(['git', 'rev-parse', 'HEAD']) == source, 'checkout does not match requested SHA')
    require(not run(['git', 'status', '--porcelain', '--untracked-files=all']), 'candidate checkout must be clean')
    # Status flags can hide tracked changes. Compare every blob and executable
    # bit directly with the requested commit before packaging or building claims.
    tree = subprocess.check_output(['git', 'ls-tree', '-rz', '--full-tree', source], timeout=30)
    for record in tree.split(b'\0'):
        if not record:
            continue
        metadata, name = record.split(b'\t', 1)
        mode, kind, object_id = metadata.decode().split()
        path = Path(os.fsdecode(name))
        require(kind == 'blob' and mode in ('100644', '100755') and path.is_file() and not path.is_symlink(), 'unsupported or missing source input')
        content = path.read_bytes()
        actual = hashlib.sha1(b'blob ' + str(len(content)).encode() + b'\0' + content).hexdigest()
        require(actual == object_id, 'source blob mismatch: ' + str(path))
        require(bool(path.stat().st_mode & 0o111) == (mode == '100755'), 'source executable mode mismatch: ' + str(path))



def archive_name(version, platform):
    return PROJECT + '_' + version[1:] + '_' + platform + '.tar.gz'


def members(archive):
    result = archive.getmembers()
    require(len(result) <= 10000, 'archive member count exceeds bound')
    names, total = set(), 0
    for member in result:
        path = PurePosixPath(member.name)
        require(member.name and not path.is_absolute() and '..' not in path.parts and '\\' not in member.name,
                'unsafe archive path')
        require(member.name not in names, 'duplicate archive member')
        require(member.isfile() or member.isdir(), 'archive links/devices unsupported')
        require(not member.mode & 0o6000, 'set-id archive member unsupported')
        require(0 <= member.size <= 100 << 20, 'archive member exceeds bound')
        names.add(member.name)
        total += member.size
    require(total <= 256 << 20, 'archive total exceeds bound')
    return result


def unpack(archive_path, destination):
    destination = Path(destination)
    require(not destination.exists(), 'extraction destination must be new')
    with tarfile.open(archive_path, 'r:gz') as archive:
        entries = members(archive)
        destination.mkdir(mode=0o700, parents=True)
        # All entries validated; links/devices and traversal excluded on Python 3.9 too.
        archive.extractall(destination, members=entries)
    return destination


def pack_native(source, version, output, binary, platform):
    source_check(source, version)
    require(platform in ('darwin_arm64', 'linux_amd64'), 'unsupported native platform')
    goos, goarch = platform.split('_')
    require(run(['go', 'env', 'GOHOSTOS']) == goos and run(['go', 'env', 'GOHOSTARCH']) == goarch, 'native runner required')
    require(run([str(Path(binary).resolve()), '--version']) == 'faultproxy dev commit=' + source + ' go=go1.27.1', 'native binary source/version identity mismatch')
    metadata = dict(schema=1, source_sha=source, proposed_version=version, platform=platform,
                    binary_sha256=sha(binary), runtime_version='dev', go='go1.27.1',
                    build_info=run(['go', 'version', '-m', binary]))
    output = Path(output); output.mkdir(parents=True, exist_ok=True)
    name = archive_name(version, platform)
    require(not (output / name).exists(), 'refuse archive overwrite')
    with tarfile.open(output / name, 'w:gz') as archive:
        for filename, path in [('faultproxy', Path(binary)), ('LICENSE', Path('LICENSE')), ('example.yaml', Path('examples/demo.yaml'))]:
            info = tarfile.TarInfo(filename); info.mode = 0o755 if filename == 'faultproxy' else 0o644
            data = path.read_bytes(); info.size = len(data); archive.addfile(info, io.BytesIO(data))
        for filename, data in [('RELEASE.json', (json.dumps(metadata, indent=2)+'\n').encode()),
                               ('USAGE.md', ('Candidate '+version+' from '+source+'.\nRuntime identifies itself as dev; proposed version is packaging metadata.\nRun ./faultproxy --help and --check-config --upstream=http://127.0.0.1:8081 --config=example.yaml.\nSee the frozen source README for the fixture/demo and bounded lossy capture.\n').encode())]:
            info = tarfile.TarInfo(filename); info.size = len(data); info.mode = 0o644
            archive.addfile(info, io.BytesIO(data))
    (output / (platform+'.json')).write_text(json.dumps(metadata, indent=2)+'\n')


def pack_source(source, version, output):
    source_check(source, version)
    output = Path(output); output.mkdir(parents=True, exist_ok=True)
    raw = subprocess.check_output(['git', 'archive', '--format=tar', '--prefix='+PROJECT+'-'+version[1:]+'/', source], timeout=60)
    name = output / archive_name(version, 'source')
    require(not name.exists(), 'refuse source overwrite')
    with tarfile.open(fileobj=io.BytesIO(raw)) as original, tarfile.open(name, 'w:gz') as archive:
        for member in members(original):
            archive.addfile(member, original.extractfile(member) if member.isfile() else None)


def verify_native(archive_path, source, version, platform, destination):
    identity(source, version)
    root = unpack(archive_path, destination)
    require({p.name for p in root.iterdir()} == {'faultproxy', 'LICENSE', 'example.yaml', 'USAGE.md', 'RELEASE.json'}, 'native archive inventory mismatch')
    meta = json.loads((root/'RELEASE.json').read_text())
    require((meta['source_sha'], meta['proposed_version'], meta['platform']) == (source, version, platform), 'native metadata identity mismatch')
    require(sha(root/'faultproxy') == meta['binary_sha256'] and (root/'faultproxy').stat().st_mode & 0o111, 'native binary hash/mode mismatch')
    require(run([str((root/'faultproxy').resolve()), '--version']) == 'faultproxy dev commit='+source+' go=go1.27.1', 'downloaded runtime identity mismatch')
    run([str((root/'faultproxy').resolve()), '--check-config', '--config='+str((root/'example.yaml').resolve()), '--upstream=http://127.0.0.1:8081'])
    return root


def image_config(archive_path):
    # Docker's containerd store can report an index digest as .Id. Read the
    # actual configuration bytes rather than equating those identities.
    with tarfile.open(archive_path) as archive:
        member = archive.getmember('manifest.json')
        require(member.isfile() and member.size <= 1 << 20, 'invalid Docker archive manifest')
        manifest = json.load(archive.extractfile(member))
        require(isinstance(manifest, list) and len(manifest) == 1, 'one saved image required')
        name = manifest[0]['Config']
        path = PurePosixPath(name)
        require(not path.is_absolute() and '..' not in path.parts, 'unsafe Docker config path')
        member = archive.getmember(name)
        require(member.isfile() and member.size <= 1 << 20, 'invalid Docker configuration')
        content = archive.extractfile(member).read()
        config = json.loads(content)
        require((config['os'], config['architecture']) == ('linux', 'amd64'), 'wrong saved image platform')
        return 'sha256:' + hashlib.sha256(content).hexdigest()


def finalize(source, version, directory, repository, run_id, attempt):
    identity(source, version)
    root = Path(directory)
    names = [archive_name(version, p) for p in ('darwin_arm64', 'linux_amd64', 'source')]
    for name in names:
        require((root/name).is_file(), 'required archive missing: '+name)
    for p in ('darwin_arm64', 'linux_amd64'):
        m=json.loads((root/(p+'.json')).read_text())
        require((m['source_sha'],m['proposed_version'],m['platform']) == (source,version,p), 'native preparation mismatch')
    image = json.loads((root/'image.json').read_text())
    require((image['source_sha'], image['platform'], image['tested']) == (source,'linux/amd64',True), 'image preparation mismatch')
    require(sha(root/'image.tar') == image['archive_sha256'], 'image archive mismatch')
    require(image_config(root/'image.tar') == image['config_digest'], 'image configuration mismatch')
    require(re.fullmatch(r'sha256:[a-f0-9]{64}', image['image_id']), 'invalid image ID')
    (root/'SHA256SUMS').write_text(''.join(sha(root/n)+'  '+n+'\n' for n in names))
    metadata = dict(schema=1, source_sha=source, version=version, repository=repository,
                    preparation_run=int(run_id), preparation_attempt=int(attempt), image=image,
                    files={n:sha(root/n) for n in names+['SHA256SUMS','image.tar','image.json','docker-smoke.json','darwin_arm64.json','linux_amd64.json']})
    (root/'PROVENANCE.json').write_text(json.dumps(metadata,indent=2,sort_keys=True)+'\n')
    print('Review PROVENANCE.json SHA-256:', sha(root/'PROVENANCE.json'))


def candidate_check(root, source, version, repository, run_id, attempt, provenance_hash):
    identity(source, version)
    require(re.fullmatch(r'[a-f0-9]{64}', provenance_hash), 'reviewed provenance SHA-256 required')
    root=Path(root)
    require(sha(root/'PROVENANCE.json') == provenance_hash, 'reviewed provenance hash mismatch')
    m=json.loads((root/'PROVENANCE.json').read_text())
    require((m['source_sha'],m['version'],m['repository'],m['preparation_run'],m['preparation_attempt']) ==
            (source,version,repository,int(run_id),int(attempt)), 'selected candidate identity mismatch')
    expected={archive_name(version,p) for p in ('darwin_arm64','linux_amd64','source')} | {'SHA256SUMS','image.tar','image.json','docker-smoke.json','darwin_arm64.json','linux_amd64.json'}
    require(set(m['files']) == expected, 'candidate provenance inventory mismatch')
    require({p.name for p in root.iterdir()} == expected | {'PROVENANCE.json'}, 'unexpected candidate file')
    for n,h in m['files'].items(): require(sha(root/n)==h,'candidate checksum mismatch: '+n)
    image=m['image']
    require(image['archive_sha256']==sha(root/'image.tar') and image['tested'] and image['source_sha']==source and image['platform']=='linux/amd64','candidate image identity mismatch')
    require(json.loads((root/'image.json').read_text())==image,'image metadata differs from provenance')
    require(image_config(root/'image.tar')==image['config_digest'],'saved image configuration mismatch')
    return m


def run_check(repository, run_id, attempt, source):
    require(re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+',repository), 'invalid repository')
    require(str(run_id).isdigit() and str(attempt).isdigit(), 'numeric selected run/attempt required')
    url='https://api.github.com/repos/'+repository+'/actions/runs/'+str(run_id)
    request=urllib.request.Request(url,headers={'Authorization':'Bearer '+os.environ['GH_TOKEN'], 'Accept':'application/vnd.github+json', 'X-GitHub-Api-Version':'2022-11-28'})
    with urllib.request.urlopen(request,timeout=30) as response:m=json.load(response)
    require(m['repository']['full_name']==repository and m['head_repository']['full_name']==repository,'foreign candidate run')
    require(m['head_sha']==source and m['event']=='workflow_dispatch' and m['path']=='.github/workflows/prepare-release.yml','wrong source/event/workflow')
    require(m['status']=='completed' and m['conclusion']=='success' and m['run_attempt']==int(attempt),'preparation run not successful selected attempt')


def public_checks(root, source, version, checksum_hash):
    identity(source,version)
    require(re.fullmatch(r'[a-f0-9]{64}',checksum_hash),'reviewed checksum-file hash required')
    root=Path(root)
    require(sha(root/'SHA256SUMS')==checksum_hash,'reviewed checksum file mismatch')
    expected={archive_name(version,p) for p in ('darwin_arm64','linux_amd64','source')}
    seen=set()
    for line in (root/'SHA256SUMS').read_text().splitlines():
        match=re.fullmatch(r'([a-f0-9]{64})  ([a-zA-Z0-9_.-]+)',line)
        require(match is not None,'invalid public checksum line')
        h,n=match.groups();require(n in expected and n not in seen,'unexpected public archive')
        require(sha(root/n)==h,'public asset checksum mismatch');seen.add(n)
    require(seen==expected,'public inventory incomplete')


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('mode',choices=['source-check','native','source','finalize','candidate-check','run-check','public-check','extract','verify-native','image-config'])
    p.add_argument('--source',required=True);p.add_argument('--version',required=True)
    p.add_argument('--directory');p.add_argument('--binary');p.add_argument('--platform');p.add_argument('--archive');p.add_argument('--destination')
    p.add_argument('--repository');p.add_argument('--run-id');p.add_argument('--attempt');p.add_argument('--hash')
    a=p.parse_args();identity(a.source,a.version)
    if a.mode=='source-check':source_check(a.source,a.version)
    elif a.mode=='native':pack_native(a.source,a.version,a.directory,a.binary,a.platform)
    elif a.mode=='source':pack_source(a.source,a.version,a.directory)
    elif a.mode=='finalize':finalize(a.source,a.version,a.directory,a.repository,a.run_id,a.attempt)
    elif a.mode=='candidate-check':candidate_check(a.directory,a.source,a.version,a.repository,a.run_id,a.attempt,a.hash)
    elif a.mode=='run-check':run_check(a.repository,a.run_id,a.attempt,a.source)
    elif a.mode=='public-check':public_checks(a.directory,a.source,a.version,a.hash)
    elif a.mode=='extract':unpack(a.archive,a.destination)
    elif a.mode=='image-config':print(image_config(a.archive))
    elif a.mode=='verify-native':verify_native(a.archive,a.source,a.version,a.platform,a.destination)


if __name__=='__main__':
    main()
