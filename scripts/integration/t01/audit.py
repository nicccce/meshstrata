#!/usr/bin/env python3
"""Final exact-path process audit, listener comparison and SSH-master cleanup."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import time
from remote import ROOT,Host,KEY,quote,save,stamp,COMMIT

def listening(text):
    result=set()
    for line in text.splitlines():
        f=line.split()
        if len(f)<5 or f[0] not in ['tcp','tcp6','udp','udp6']:continue
        # ss uses state as second field; netstat uses numeric receive queue.
        result.add((f[0],f[4] if not f[1].isdigit() else f[3]))
    return sorted(result)

def main():
    p=argparse.ArgumentParser();p.add_argument('--run',required=True);a=p.parse_args();assert re.fullmatch(r'meshstrata-t01-[a-zA-Z0-9-]+',a.run)
    os.umask(0o077);out=ROOT/'.local'/a.run
    ips=re.findall(r'`(\d+\.\d+\.\d+\.\d+)`',(ROOT/'.local/test-servers.md').read_text())[:2]
    result={'product_commit':COMMIT,'started_at':stamp(),'hosts':{}}
    for label,ip in zip(['ubuntu','alpine'],ips):
        h=Host(label,ip,a.run,out)
        scan='for p in /proc/[0-9]*/exe; do v=$(readlink "$p" 2>/dev/null); case "$v" in '+h.root+'/bin/*) echo "$p $v";; esac; done'
        before=h.ssh(scan).stdout.decode().splitlines();terminated=[]
        for line in before:
            path,exe=line.split(' ',1);pid=int(path.split('/')[2])
            r=h.ssh(f'test "$(readlink /proc/{pid}/exe)" = {quote(exe)} && kill -TERM {pid}',check=False)
            terminated.append({'pid':pid,'signal_exit':r.returncode})
        if before:time.sleep(3)
        remaining=h.ssh(scan).stdout.decode().splitlines();assert not remaining,label+' test process remains'
        unix=h.ssh('cat /proc/net/unix').stdout.decode();active_unix=[l for l in unix.splitlines() if h.root+'/' in l]
        assert not active_unix,label+' test Unix socket remains active'
        check=h.ssh('cd '+quote(h.root+'/bin')+' && sha256sum -c SHA256SUMS');save(h.out/'final-sha256-check.txt',check.stdout)
        modes=h.ssh('stat -c "%a %u %g" '+quote(h.root)+' '+quote(h.root+'/private')).stdout.decode().splitlines()
        assert modes==['700 0 0','700 0 0']
        h.snapshot('after')
        old=listening((h.out/'before.txt').read_text());new=listening((h.out/'after.txt').read_text())
        assert old==new,label+' listening endpoints differ'
        socket='/tmp/'+Path(h.root).name+'-'+label+'.sock'
        if Path(socket).exists():
            master=subprocess.run(['ssh','-i',KEY,'-S',socket,'-O','exit','root@'+ip],capture_output=True,timeout=10)
            save(h.out/'ssh-master-exit.txt',master.stdout+master.stderr);assert master.returncode==0
        assert not Path(socket).exists()
        result['hosts'][label]={'root':h.root,'root_and_private_modes':modes,'terminated_in_final_audit':terminated,'remaining_test_processes':[],'active_test_unix_sockets':0,'host_listener_endpoints_unchanged':True,'listener_endpoints':new,'sha256_exit':check.returncode,'local_ssh_master_closed':True}
    result['finished_at']=stamp();save(out/'final-audit.json',result);print(json.dumps(result))

if __name__=='__main__':main()
