#!/usr/bin/env python3
"""No-credential public connectivity probes against a confirmed running Controller."""
import argparse
import json
import os
from pathlib import Path
import re
import socket
import time
from remote import ROOT,Host,RemoteAPI,quote,save,stamp,wait,COMMIT

def main():
    p=argparse.ArgumentParser();p.add_argument('--run',required=True);p.add_argument('--host',choices=['ubuntu','alpine'],required=True);a=p.parse_args()
    assert re.fullmatch(r'meshstrata-t01-[a-zA-Z0-9-]+',a.run);os.umask(0o077)
    out=ROOT/'.local'/a.run
    addresses=re.findall(r'`(\d+\.\d+\.\d+\.\d+)`',(ROOT/'.local/test-servers.md').read_text())[:2]
    hosts=[Host(label,ip,a.run,out) for label,ip in zip(['ubuntu','alpine'],addresses)]
    server=next(h for h in hosts if h.label==a.host);peer=next(h for h in hosts if h.label!=a.host)
    candidates=sorted((out/'private').glob(a.host+'*/expiring.json'));assert candidates
    tag=candidates[0].parent.name;source=server.root+'/private/'+tag
    config=source+'/controller.json';ca=server.get(source+'/keys/web-ca.pem');nodeca=server.get(source+'-local/ca.pem')
    d=peer.root+'/private/network-'+a.host;peer.ssh('mkdir '+quote(d));peer.put(d+'/web-ca.pem',ca);peer.put(d+'/node-ca.pem',nodeca)
    c=server.start('meshstrata-controller',config,'network-'+a.host)
    result={'product_commit':COMMIT,'started_at':stamp(),'server':a.host,'source_state':tag,'probes':[]}
    try:
        api=RemoteAPI(server,8443,ca,source);wait(lambda:api.request('/readyz'))
        result['server_ready_before']=True
        for port,trust in [(8443,'web'),(9443,'node')]:
            t=time.monotonic()
            try:
                with socket.create_connection((server.address,port),timeout=3):pass
                observation='connected'
            except OSError as e:observation=type(e).__name__
            result['probes'].append({'source':'build-host','destination':a.host,'port':port,'transport':'TCP direct','observation':observation,'seconds':round(time.monotonic()-t,3),'at':stamp()})
            command='curl --silent --show-error --connect-timeout 3 --max-time 5 --cacert '+quote(d+'/'+trust+'-ca.pem')+' --output /dev/null --write-out '+quote('http=%{http_code} connect=%{time_connect} verify=%{ssl_verify_result}\n')+' '+quote(f'https://{server.address}:{port}/'+('readyz' if port==8443 else 'control/v1'))
            r=peer.ssh(command,check=False,timeout=10)
            save(peer.out/('network-to-'+a.host+'-'+str(port)+'.txt'),r.stdout+r.stderr)
            result['probes'].append({'source':peer.label,'destination':a.host,'port':port,'transport':'HTTPS direct; trusted CA+SAN, no client credentials','exit_code':r.returncode,'curl':r.stdout.decode().strip(),'at':stamp()})
        result['server_ready_after']=bool(api.request('/readyz'))
    finally:
        result['controller_exit']=server.stop(c);result['finished_at']=stamp();save(out/('network-'+a.host+'.json'),result)
    print(json.dumps(result))

if __name__=='__main__':main()
