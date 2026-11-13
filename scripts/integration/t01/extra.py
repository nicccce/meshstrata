#!/usr/bin/env python3
"""Additional T01 failure injection and real public remote-kernel orchestration."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import sqlite3
import subprocess
import time
import traceback
from remote import ROOT, Host, quote, save, wait, stamp, COMMIT, API, RemoteAPI

def rewrite(value,old,new):
    if isinstance(value,str):return value.replace(old,new)
    if isinstance(value,list):return [rewrite(x,old,new) for x in value]
    if isinstance(value,dict):return {k:rewrite(v,old,new) for k,v in value.items()}
    return value

def faults(h,out,args_attempt=''):
    candidates=sorted((out/'private').glob(h.label+'*/expiring.json'));assert candidates,'no completed local source with expired-token fixture'
    tag=candidates[0].parent.name
    source=h.root+'/private/'+tag
    original=json.loads(h.get(source+'/controller.json'))
    results={}
    def clone(name):
        dest=h.root+'/private/fault-'+name+('-'+args_attempt if args_attempt else '')
        h.ssh('cp -a '+quote(source)+' '+quote(dest))
        cfg=rewrite(original,source,dest);path=dest+'/controller.json';h.put(path,cfg)
        return dest,cfg,path
    dest,cfg,path=clone('missing-key');key=cfg['secure']['key_files']['v1']
    h.ssh('rm '+quote(key));h.cli('meshstrata-controller','run',path,ok=False)
    assert h.ssh('test ! -e '+quote(key),check=False).returncode==0
    results['missing_key']={'expected':'nonzero/no replacement','actual':'passed','exit_code':1}
    save(h.out/('fault-cases'+('-'+args_attempt if args_attempt else '')+'.json'),results)
    dest,cfg,path=clone('corrupt-ca');dbfile=out/'private'/h.label/'fault-corrupt.db'
    save(dbfile,h.get(dest+'/controller/controller.db'))
    with sqlite3.connect(dbfile) as db:db.execute("UPDATE b02_management_ca SET certificate=x'01'")
    db.close()
    h.put(dest+'/controller/controller.db',dbfile.read_bytes())
    before=hashlib.sha256(h.get(dest+'/controller/controller.db')).hexdigest()
    h.cli('meshstrata-controller','run',path,ok=False)
    check=out/'private'/h.label/'fault-corrupt-after.db';save(check,h.get(dest+'/controller/controller.db'))
    with sqlite3.connect(check) as db:assert db.execute('SELECT certificate FROM b02_management_ca').fetchone()[0]==b'\x01'
    results['corrupt_ca']={'expected':'nonzero/preserve corrupt CA','actual':'passed','exit_code':1,'snapshot_sha256':before}
    save(h.out/('fault-cases'+('-'+args_attempt if args_attempt else '')+'.json'),results)
    dest,cfg,path=clone('listener-conflict')
    hold=dest+'/hold';h.ssh('mkdir '+quote(hold))
    env=f'T01_DIR={quote(hold)} T01_LISTEN=127.0.0.1:9443 T01_HOLD=1'
    p=h.start('t01-kernel.test','',h.label+'-port-holder',args=['-test.run','^TestListenerProbe$','-test.v'],env=env)
    try:
        wait(lambda:h.ssh('test -f '+quote(hold+'/ready'),check=False).returncode==0)
        h.cli('meshstrata-controller','run',path,ok=False)
        probe=dest+'/probe';h.ssh('mkdir '+quote(probe))
        r=h.ssh(f'T01_DIR={quote(probe)} T01_LISTEN=127.0.0.1:8443 {quote(h.root+"/bin/t01-kernel.test")} -test.run "^TestListenerProbe$" -test.v',check=False)
        save(h.out/'listener-release.log',r.stdout+r.stderr);assert r.returncode==0
    finally:h.put(hold+'/stop','stop')
    wait(lambda:h.ssh('test -f '+quote(p[1]+'.exit'),check=False).returncode==0)
    assert h.stop(p)==0
    c=h.start('meshstrata-controller',path,h.label+'-after-conflict')
    time.sleep(1);assert h.ssh('kill -0 '+str(c[0]),check=False).returncode==0;assert h.stop(c)==0
    results['listener_conflict']={'expected':'later listener fails/earlier released/lock reusable','actual':'passed','port_probe_exit':r.returncode}
    save(h.out/('fault-cases'+('-'+args_attempt if args_attempt else '')+'.json'),results)
    dest,cfg,path=clone('expired-token')
    expired=json.loads((out/'private'/tag/'expiring.json').read_text())
    dbfile=out/'private'/h.label/'fault-expired.db';save(dbfile,h.get(dest+'/controller/controller.db'))
    with sqlite3.connect(dbfile) as db:
        assert db.execute('UPDATE b02_tokens SET expires_at=? WHERE node_id=? AND csr_hash IS NULL',('2000-01-01T00:00:00Z',expired['node_id'])).rowcount==1
    db.close()
    h.put(dest+'/controller/controller.db',dbfile.read_bytes())
    c=h.start('meshstrata-controller',path,h.label+'-expired-token')
    try:
        api=RemoteAPI(h,8443,h.get(dest+'/keys/web-ca.pem'),dest);wait(lambda:api.request('/readyz'))
        enrollment=RemoteAPI(h,9443,expired['ca_pem'],dest)
        csrdir=out/'private'/tag/'expired-csr';csrdir.mkdir(mode=0o700)
        subprocess.run(['openssl','req','-new','-newkey','ed25519','-nodes','-keyout',str(csrdir/'key.pem'),'-out',str(csrdir/'csr.pem'),'-subj','/CN=T01-expired'],capture_output=True,check=True)
        response=enrollment.request('/enroll/v1',{'token':expired['token'],'csr_pem':(csrdir/'csr.pem').read_text()},409)
        assert response['error']['code']=='TASK_EXPIRED'
    finally:assert h.stop(c)==0
    results['expired_token']={'expected':'expired unconsumed token returns409 TASK_EXPIRED','actual':'passed','scope':'verified remote loopback TLS on dedicated copied state; expiry timestamp injected without host clock change'}
    save(h.out/('fault-cases'+('-'+args_attempt if args_attempt else '')+'.json'),results)
    # Dedicated Agent copy keeps the real accepted identity and corrupts only the
    # stage1 marker. No source state is reset or shared with a running instance.
    ap=h.root+'/private/'+tag+'-local';clonepath=h.root+'/private/fault-agent'
    h.ssh('cp -a '+quote(ap)+' '+quote(clonepath))
    ac=rewrite(json.loads(h.get(ap+'/agent.json')),ap,clonepath);h.put(clonepath+'/agent.json',ac)
    marker=clonepath+'/agent/stage1-control.json';value=json.loads(h.get(marker));value['applied_revision']=1;h.put(marker,value)
    before=h.get(marker);h.cli('meshstrata-agent','run',clonepath+'/agent.json',ok=False);assert before==h.get(marker)
    results['unknown_business_state']={'expected':'refuse without clearing marker','actual':'passed','exit_code':1}
    save(h.out/('fault-cases'+('-'+args_attempt if args_attempt else '')+'.json'),results)
    return results

def cross(server,client,out,args_attempt=''):
    # Verify the four dedicated ports are not in the original host listener set.
    before=server.ssh('(ss -lntu 2>/dev/null || netstat -lntu)').stdout.decode()
    assert not re.search(r':2844[3-6]\b',before),'dedicated public test port already occupied'
    sd=server.root+'/private/cross-server'+('-'+args_attempt if args_attempt else '');cd=client.root+'/private/cross-from-'+server.label+('-'+args_attempt if args_attempt else '')
    server.ssh('mkdir '+quote(sd));client.ssh('mkdir '+quote(cd))
    p=server.start('t01-kernel.test','',server.label+'-cross-server'+('-'+args_attempt if args_attempt else ''),args=['-test.run','^TestRemoteKernel$','-test.v','-test.timeout','4m'],env=f'T01_ROLE=server T01_DIR={quote(sd)} T01_KERNEL={quote(server.root+"/bin/mihomo")} T01_PUBLIC={quote(server.address)}')
    try:
        wait(lambda:server.ssh('test -f '+quote(sd+'/ready'),check=False).returncode==0,seconds=25)
        client.put(cd+'/client-material.json',server.get(sd+'/client-material.json'))
        r=client.ssh(f'T01_ROLE=client T01_DIR={quote(cd)} T01_KERNEL={quote(client.root+"/bin/mihomo")} {quote(client.root+"/bin/t01-kernel.test")} -test.run "^TestRemoteKernel$" -test.v -test.timeout 3m',timeout=200,check=False)
        save(client.out/('cross-from-'+server.label+'.log'),r.stdout+r.stderr)
        evidence=json.loads(client.get(cd+'/evidence.json'));save(client.out/('cross-from-'+server.label+'.json'),evidence)
        return {'exit_code':r.returncode,'public_ports':{'ss2022':'28443 TCP+UDP','trojan':'28444 TCP','vless':'28445 TCP','hysteria2':'28446 UDP'},'protocols':evidence,'source':client.label,'destination':server.label,'target':'server-local controlled TCP/UDP echo; VLESS controlled TLS1.3 target','tunnel':False}
    finally:
        server.put(sd+'/stop','stop')
        wait(lambda:server.ssh('test -f '+quote(p[1]+'.exit'),check=False).returncode==0)
        code=server.stop(p)
        save(server.out/('cross-server'+('-'+args_attempt if args_attempt else '')+'-exit.json'),{'exit_code':code})

def main():
    parser=argparse.ArgumentParser();parser.add_argument('--run',required=True);parser.add_argument('--phase',choices=['faults','cross'],required=True);parser.add_argument('--host',choices=['ubuntu','alpine','all'],default='all');parser.add_argument('--attempt',default='');args=parser.parse_args()
    assert not args.attempt or re.fullmatch(r'[a-zA-Z0-9-]+',args.attempt)
    assert re.fullmatch(r'meshstrata-t01-[a-zA-Z0-9-]+',args.run);os.umask(0o077)
    out=ROOT/'.local'/args.run
    addresses=re.findall(r'`(\d+\.\d+\.\d+\.\d+)`',(ROOT/'.local/test-servers.md').read_text())[:2]
    hosts=[Host(label,ip,args.run,out) for label,ip in zip(['ubuntu','alpine'],addresses)]
    results={'phase':args.phase,'product_commit':COMMIT,'started_at':stamp(),'results':{}}
    try:
        for h,other in [hosts,hosts[::-1]]:
            if args.host not in ['all',h.label]:continue
            try:results['results'][h.label]=faults(h,out,args.attempt) if args.phase=='faults' else cross(h,other,out,args.attempt)
            except Exception as e:
                save(out/(h.label+'-'+args.phase+'-error.log'),traceback.format_exc())
                results['results'][h.label]={'status':'failed','error_type':type(e).__name__,'error':str(e) if isinstance(e,AssertionError) else 'private driver log'}
            finally:
                for host in hosts:
                    for p in reversed(host.processes):
                        try:host.stop(p)
                        except Exception:pass
            save(out/(args.phase+('-'+args.host if args.host!='all' else '')+'.json'),results)
    finally:
        results['finished_at']=stamp();save(out/(args.phase+('-'+args.host if args.host!='all' else '')+'.json'),results)
    print(json.dumps({'phase':args.phase,'evidence':str(out),'results':results['results']},ensure_ascii=False))

if __name__=='__main__':main()
