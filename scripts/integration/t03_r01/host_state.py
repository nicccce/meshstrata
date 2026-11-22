#!/usr/bin/env python3
"""Read-only stable host service, listener PID, route and resolver comparison."""
import base64,json,pathlib,sys
from preflight import ssh,write
PROGRAM=r'''
import pathlib,os,subprocess,re,json,hashlib,time
p=subprocess.run('ss -lntup 2>/dev/null || netstat -lntup',shell=True,capture_output=True)
s=p.stdout.decode();pids=set(re.findall(r'pid=(\d+)',s))|set(re.findall(r'\s(\d+)/\S+',s));pids.add('1');processes={}
for pid in sorted(pids):
 root=pathlib.Path('/proc')/pid
 processes[pid]=dict(start=(root/'stat').read_text().split()[21],exe=os.readlink(root/'exe'),command_sha256=hashlib.sha256((root/'cmdline').read_bytes()).hexdigest())
route=subprocess.run(['ip','route'],capture_output=True)
services=subprocess.run('systemctl list-units --type=service --state=running --no-pager --no-legend 2>/dev/null || rc-status -a',shell=True,capture_output=True)
print(json.dumps(dict(processes=processes,route_sha256=hashlib.sha256(route.stdout).hexdigest(),resolver_sha256=hashlib.sha256(pathlib.Path('/etc/resolv.conf').read_bytes()).hexdigest(),services_sha256=hashlib.sha256(services.stdout).hexdigest(),time_ns=time.time_ns())))
'''
local=pathlib.Path(sys.argv[1]);stage=sys.argv[2];assert stage in ('before','after');summary=[]
for i in range(2):
 p=ssh(i,'PYTHONDONTWRITEBYTECODE=1 python3 -',PROGRAM.encode());assert p.returncode==0
 state=json.loads(p.stdout);write(local/('host-state-'+stage+'-'+str(i)+'.json'),state)
 if stage=='after':
  before=json.loads((local/('host-state-before-'+str(i)+'.json')).read_text())
  flags={k:state[k]==before[k] for k in ['processes','route_sha256','resolver_sha256','services_sha256']};summary.append(dict(host='AB'[i],unchanged=flags))
if stage=='after':write(local/'host-preservation.json',summary)
print(json.dumps(summary) if summary else 'Stable host state baseline saved for A/B')
