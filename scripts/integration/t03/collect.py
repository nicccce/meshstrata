#!/usr/bin/env python3
"""Final private log collection after aggregate secret scanning; no raw logs on stdout."""
import base64,json,os,pathlib,sys
from preflight import *
PROGRAM=r'''
import base64,hashlib,json,pathlib,sys,tarfile
root=pathlib.Path(payload['root']);patterns=set()
def add(value):
 if not value:return
 raw=value.encode() if isinstance(value,str) else value
 if len(raw)<6:return
 patterns.update([raw,base64.b64encode(raw),raw.hex().encode()])
 if isinstance(value,str):patterns.add(json.dumps(value)[1:-1].encode())
def inspect(value):
 if isinstance(value,dict):
  for k,v in value.items():
   if k in ('password','uuid','secret','url','private-key','private_key') and isinstance(v,str):add(v)
   inspect(v)
 elif isinstance(value,list):
  for item in value:inspect(item)
for value in payload['secrets']:add(value)
for p in root.rglob('*'):
 if not p.is_file() or p.is_symlink():continue
 if p.name.endswith('.key') or p.name.endswith('key.pem') or p.name in ('token','enroll.token','bootstrap-proof'):
  add(p.read_bytes())
 if p.suffix=='.json' and p.stat().st_size<2*1024*1024:
  try:inspect(json.loads(p.read_bytes()))
  except (ValueError,UnicodeError):pass
logs=sorted(root.rglob('*.log'));findings=[];hashes={}
for p in logs:
 raw=p.read_bytes();matches=sum(1 for pattern in patterns if pattern in raw)
 if matches:findings.append({'file':str(p.relative_to(root)),'matches':matches})
 hashes[str(p.relative_to(root))]=hashlib.sha256(raw).hexdigest()
if payload['action']=='scan':
 print(json.dumps(dict(log_files=len(logs),patterns=len(patterns),findings=findings,sha256=hashes)))
else:
 if findings:sys.exit(3)
 with tarfile.open(fileobj=sys.stdout.buffer,mode='w|gz') as tar:
  for p in logs:tar.add(p,arcname=str(p.relative_to(root)),recursive=False)
'''
def main():
 os.umask(0o077);local=pathlib.Path(sys.argv[1]);mapping=json.loads((local/'mapping.json').read_text());generated=[]
 for p in local.glob('r*/rpc-*-input.json'):
  v=json.loads(p.read_text())
  if v.get('op')=='scan':generated.extend(v['secrets'])
 generated=list(set(generated));summaries=[]
 for i in range(2):
  payload=dict(root=mapping['remote'],secrets=generated,action='scan')
  def program():return b'import json,base64\npayload=json.loads(base64.b64decode('+repr(base64.b64encode(json.dumps(payload).encode())).encode()+b'))\n'+PROGRAM.encode()
  p=ssh(i,'PYTHONDONTWRITEBYTECODE=1 python3 -',program(),timeout=120);write(local/('final-scan-'+str(i)+'.json'),p.stdout);assert p.returncode==0,'scan failed';result=json.loads(p.stdout);assert not result['findings'],'secret found; no logs copied'
  payload['action']='archive';p=ssh(i,'PYTHONDONTWRITEBYTECODE=1 python3 -',program(),timeout=120);assert p.returncode==0,'archive scan failed';write(local/('host'+str(i)+'-logs.tar.gz'),p.stdout);summaries.append(dict(host='AB'[i],log_files=result['log_files'],secret_patterns=result['patterns'],findings=0,private_archive_saved=True))
 write(local/'final-log-scan.json',summaries);print(json.dumps(summaries))
if __name__=='__main__':
 try:main()
 except Exception:
  import traceback
  write(pathlib.Path(sys.argv[1])/'collect-error.txt',traceback.format_exc());print('final collection failed; private evidence saved');sys.exit(1)
