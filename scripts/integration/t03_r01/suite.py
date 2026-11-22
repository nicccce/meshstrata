#!/usr/bin/env python3
"""Serial dispatch only; do not repeat a completed phase or ignore dirty cleanup."""
import argparse,json,os,pathlib,subprocess,sys
from preflight import COMMIT,write
CASES=[('observe',1,'observe'),('observe',0,'observe'),('correct',1,'correct-first'),('correct',0,'correct-first'),('sequence',1,'sequence'),('sequence',0,'sequence'),('restart',1,'restart'),('restart',0,'restart'),('history',0,'history'),('history',1,'history'),('sharing',0,'sharing'),('sharing',1,'sharing'),('sharing-fixed',0,'sharing')]
p=argparse.ArgumentParser();p.add_argument('private_root');p.add_argument('--start',type=int,default=1);p.add_argument('--end',type=int,default=12);a=p.parse_args()
root=pathlib.Path(a.private_root).resolve();assert 1<=a.start<=a.end<=len(CASES)
env=dict(os.environ,MESHSTRATA_PRODUCT_COMMIT=COMMIT,PYTHONDONTWRITEBYTECODE='1')
for number,(name,ci,scenario) in enumerate(CASES,1):
 if not a.start<=number<=a.end:continue
 phase='r%02d-%s'%(number,name);assert not (root/phase).exists(),'refuse repeated phase'
 result=subprocess.run([sys.executable,str(pathlib.Path(__file__).with_name('run.py')),str(root),'--phase',phase,'--controller',str(ci),'--scenario',scenario],env=env)
 data=json.loads((root/phase/'result.json').read_text())
 for host in 'AB':
  clean=data.get('cleanup',{}).get(host,{})
  assert not clean.get('failed') and not clean.get('live_recorded_pids') and not clean.get('sockets') and all(v==0 for v in clean.get('stop_runtime_exits',[])), 'cleanup incomplete; stop suite'
 print(phase,'branch complete:',data.get('result'),'exit',result.returncode,flush=True)
