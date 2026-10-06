#!/usr/bin/env python3
"""Bounded release-image smoke using only UUID-scoped Docker resources.

Build the fixture image separately. This helper never rebuilds the tested proxy.
Native Linux CI must pass --native; Mac checks disclose emulation explicitly.
"""
import argparse
import hashlib
import http.client
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import time
import uuid

import demo


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image',required=True)
    parser.add_argument('--upstream-image',required=True)
    parser.add_argument('--client',required=True)
    parser.add_argument('--output',required=True)
    parser.add_argument('--context')
    parser.add_argument('--native',action='store_true')
    parser.add_argument('--capture',action='store_true')
    args=parser.parse_args()
    output=Path(args.output).resolve();output.mkdir(mode=0o700,parents=True)
    require=demo.require
    prefix=['docker']+(['--context',args.context] if args.context else [])
    owned='faultproxy-check-'+uuid.uuid4().hex[:12]
    containers=[];network=False;connections=[];commands=[];result={};failed=None
    def command(*words, timeout=60):
        completed=subprocess.run(prefix+list(words),stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=timeout,text=True)
        commands.append(dict(command=list(words),exit=completed.returncode,stdout=completed.stdout,stderr=completed.stderr))
        require(completed.returncode==0,'Docker command failed: '+words[0]+' exit '+str(completed.returncode)+': '+completed.stderr)
        return completed.stdout.strip()
    def inspect(name):return json.loads(command('inspect',name))[0]
    def stats(port):
        status,_,body,incomplete=demo.fetch(port,'/stats')
        require(status==200 and not incomplete,'fixture statistics unavailable')
        return json.loads(body)
    def ready(port):
        def check():
            try:
                status,_,body,incomplete=demo.fetch(port,'/healthz')
                return status==200 and body==b'ok\n' and not incomplete
            except OSError:return False
        demo.until(check,'container readiness failed',10)
    def stop(name,expected,signum='SIGTERM'):
        start=time.monotonic();command('kill','--signal='+signum,name)
        exit_code=int(command('wait',name,timeout=9))
        elapsed=time.monotonic()-start;state=inspect(name)['State']
        require(exit_code==expected and state['ExitCode']==expected and not state['OOMKilled'],'container shutdown exit mismatch')
        require(elapsed<=8,'container stop exceeded bounded assertion')
        return dict(exit=exit_code,seconds=elapsed,signal=signum,oom=state['OOMKilled'])
    def launch_proxy(name,config,data,admin,capture_file):
        config_path=output/(name+'.yaml');config_path.write_text(config);config_path.chmod(0o444)
        run=['run','-d','--name',name,'--platform=linux/amd64','--network',owned,
             '--publish','127.0.0.1:'+str(data)+':8080','--publish','127.0.0.1:'+str(admin)+':9090',
             '--mount','type=bind,src='+str(config_path)+',dst=/config/scenario.yaml,readonly']
        argv=['--upstream=http://upstream:8081','--config=/config/scenario.yaml','--listen=0.0.0.0:8080','--admin-listen=0.0.0.0:9090']
        if args.capture:
            require(os.getuid()!=0,'capture requires a nonroot owned output directory')
            run+=['--user',str(os.getuid())+':'+str(os.getgid()),'--mount','type=bind,src='+str(output)+',dst=/capture']
            argv=['/capture/'+capture_file,'--','/faultproxy']+argv
        containers.append(name)
        command(*(run+[args.image]+argv));ready(admin)
        info=inspect(name)
        user=info['Config']['User'];require(user and user.split(':')[0] not in ('0','root'),'container is root')
        mounts=[m for m in info['Mounts'] if m['Destination']=='/config/scenario.yaml']
        require(len(mounts)==1 and mounts[0]['RW'] is False,'configuration mount is writable')
        for bindings in info['NetworkSettings']['Ports'].values():
            require(bindings and all(b['HostIp']=='127.0.0.1' for b in bindings),'host publication is not loopback only')
        result.setdefault('runtime',[]).append(dict(name=name,user=user,configuration_read_only=True,ports=info['NetworkSettings']['Ports']))
    def reconcile(admin,count):
        demo.until(lambda:demo.accounting(admin)['requests']==count,'terminal accounting failed')
        evidence=demo.accounting(admin)
        require(evidence['histogram']==count,'histogram mismatch')
        for _ in range(2):require(demo.accounting(admin)==evidence,'admin scrape changed state')
        return evidence
    def close_connections():
        for connection in connections:connection.close()
        connections.clear()
    interrupted=False
    def interrupt(_signal,_frame):
        nonlocal interrupted
        if not interrupted:
            interrupted=True
            raise RuntimeError('Docker verification interrupted')
    for sig in (signal.SIGINT,signal.SIGTERM):signal.signal(sig,interrupt)
    ports=[demo.available_port() for _ in range(3)]
    require(len(set(ports))==3,'port allocation collision')
    up,data,admin=ports
    try:
        arch=command('info','--format','{{.Architecture}}')
        result.update(platform='linux/amd64',daemon_architecture=arch,host_platform=sys.platform,native=args.native,ports=ports)
        if args.native:require(sys.platform=='linux' and arch in ('x86_64','amd64'),'native Linux amd64 required')
        info=json.loads(command('image','inspect',args.image))[0]
        require((info['Os'],info['Architecture'])==('linux','amd64'),'wrong proxy image platform')
        result['image_id']=info['Id']
        command('network','create',owned);network=True
        upstream=owned+'-upstream';containers.append(upstream)
        command('run','-d','--name',upstream,'--network',owned,'--network-alias','upstream','--platform=linux/amd64',
                '--publish','127.0.0.1:'+str(up)+':8081',args.upstream_image,'--listen=0.0.0.0:8081','--delay=3s')
        ready(up)
        for mode in ('pass-through','none','retry','controls'):
            name=owned+'-'+mode
            config='version: 1\nrules: []\n' if mode=='pass-through' else ('version: 1\nupstream_timeout_ms: 500\nrules: [{id: delayed, path_prefix: /ok, faults: {delay_ms: 250}}]\n' if mode=='controls' else 'version: 1\nrules: [{id: thirds, path_prefix: /, faults: {every_nth_request: 3, status: 503}}]\n')
            capture_file=mode+'.jsonl'
            before=stats(up)
            launch_proxy(name,config,data,admin,capture_file)
            if mode=='pass-through':
                status,headers,body,incomplete=demo.fetch(data,'/ok',connections=connections)
                require(status==200 and body==b'fixture ok\n' and not incomplete and 'X-Faultproxy-Injected' not in headers,'pass-through failed')
                count,calls,outcomes,actions,errors=1,1,{'upstream_response':1},{},{}
            elif mode in ('none','retry'):
                completed=subprocess.run([str(Path(args.client).resolve()),'--url=http://127.0.0.1:'+str(data)+'/ok','--mode='+mode,'--operations=6'],capture_output=True,text=True,timeout=35)
                (output/(mode+'-client.jsonl')).write_text(completed.stdout)
                (output/(mode+'-client.stderr')).write_text(completed.stderr)
                records=[json.loads(line) for line in completed.stdout.splitlines()]
                require(len(records)==6 and completed.returncode==(1 if mode=='none' else 0),'retry client exit/record mismatch')
                require(sum(r['outcome']=='success' for r in records)==(4 if mode=='none' else 6),'retry successes mismatch')
                count,calls=(6,4) if mode=='none' else (8,6)
                require(sum(r['attempts'] for r in records)==count,'application attempts mismatch')
                outcomes,actions,errors={'upstream_response':calls,'synthetic_status':2},{'status':2},{}
            else:
                start=time.monotonic();status,_,body,incomplete=demo.fetch(data,'/ok',connections=connections)
                elapsed=time.monotonic()-start
                require(status==200 and body==b'fixture ok\n' and not incomplete and elapsed>=.20,'delay failed')
                status,headers,body,incomplete=demo.fetch(data,'/error',connections=connections)
                require(status==503 and body==b'fixture unavailable\n' and not incomplete and 'X-Faultproxy-Injected' not in headers,'real 503 failed')
                status,_,body,incomplete=demo.fetch(data,'/slow',connections=connections)
                require(status==504 and body==b'gateway timeout\n' and not incomplete,'pre-header timeout failed')
                status,_,body,incomplete=demo.fetch(data,'/partial',connections=connections)
                require(status==200 and body==b'prefix\n' and incomplete,'post-header incomplete response failed')
                count,calls,outcomes,actions,errors=4,4,{'upstream_response':1,'upstream_http_error':1,'upstream_timeout':1,'incomplete_response':1},{'delay':1},{'http_5xx':1,'timeout':2}
            evidence=reconcile(admin,count);after=stats(up)
            demo.require_outcomes(mode,outcomes,evidence,after)
            require(evidence['actions']==actions and evidence['errors']==errors and after['calls']-before['calls']==calls and after['active']==0,'cohort accounting mismatch')
            close_connections()
            stopped=stop(name,0,'SIGINT' if mode=='pass-through' else 'SIGTERM')
            result.setdefault('cohorts',[]).append(dict(mode=mode,metrics=evidence,upstream_before=before,upstream_after=after,shutdown=stopped))
            if args.capture:
                capture=output/capture_file
                records=[json.loads(line) for line in capture.read_text().splitlines()]
                access=[r for r in records if r.get('msg')=='request completed']
                require(len(access)==count,'small capture cohort lost records; logging remains lossy')
                require(capture.stat().st_mode & 0o777==0o600,'capture file permissions mismatch')
            elif mode=='pass-through':
                command('start',name);ready(admin)
                status,_,body,incomplete=demo.fetch(data,'/ok',connections=connections)
                require(status==200 and body==b'fixture ok\n' and not incomplete,'restart failed')
                restart=reconcile(admin,1);require(restart['outcomes']=={'upstream_response':1},'restart state not reset')
                close_connections();result['restart_shutdown']=stop(name,0)
            command('rm',name);containers.remove(name)
        # Force cleanup with a real admitted request lasting beyond the 5 s drain.
        stop(upstream,0);command('rm',upstream);containers.remove(upstream)
        containers.append(upstream)
        command('run','-d','--name',upstream,'--network',owned,'--network-alias','upstream','--platform=linux/amd64',
                '--publish','127.0.0.1:'+str(up)+':8081',args.upstream_image,'--listen=0.0.0.0:8081','--delay=20s')
        ready(up)
        name=owned+'-forced'
        launch_proxy(name,'version: 1\nupstream_timeout_ms: 10000\nrules: []\n',data,admin,'forced.jsonl')
        conn=http.client.HTTPConnection('127.0.0.1',data,timeout=8);connections.append(conn);conn.request('GET','/slow')
        demo.until(lambda:stats(up)['active']==1,'slow request was not admitted')
        result['forced_shutdown']=stop(name,1)
        close_connections();demo.until(lambda:stats(up)['active']==0,'forced request leaked upstream work')
        require(stats(up)['cancelled']==1,'forced request not cancelled')
        command('rm',name);containers.remove(name)
        result['passed']=True
    except BaseException as error:
        failed=error;result['passed']=False;result['failure']=str(error)
    finally:
        cleanup=[]
        for connection in connections:
            try:connection.close()
            except BaseException as error:cleanup.append('connection close: '+str(error))
        connections.clear()
        for name in reversed(containers):
            try:
                info=inspect(name)
                (output/(name+'.inspect.json')).write_text(json.dumps(info,indent=2)+'\n')
                (output/(name+'.logs')).write_text(command('logs',name))
                if info['State']['Running']:stop(name,0)
                command('rm',name)
            except BaseException as error:
                cleanup.append(name+': '+str(error))
                # Protection only for our recorded name; never a passing exit.
                try:command('rm','-f',name,timeout=15)
                except BaseException as fallback_error:
                    cleanup.append('fallback removal '+name+': '+str(fallback_error))
        if network:
            try:command('network','rm',owned)
            except BaseException as error:cleanup.append('network removal '+owned+': '+str(error))
        for port in ports:
            try:
                with socket.socket() as listener:
                    listener.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);listener.bind(('127.0.0.1',port))
            except BaseException as error:cleanup.append('port '+str(port)+': '+str(error))
        result['cleanup_errors']=cleanup
        if cleanup:result['passed']=False
        result['commands']=commands
        try:(output/'docker-smoke.json').write_text(json.dumps(result,indent=2)+'\n')
        except BaseException as error:
            cleanup.append('report persistence: '+str(error));result['passed']=False
    require(result.get('passed') and not result['cleanup_errors'],'Docker smoke failed: '+str(failed or 'verification completed')+'; cleanup errors: '+str(cleanup))
    print('PASS: tested image',result['image_id'],'cohorts/signals/cleanup; native='+str(args.native))


if __name__=='__main__':
    main()
