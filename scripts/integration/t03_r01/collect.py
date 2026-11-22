#!/usr/bin/env python3
"""Aggregate stopped-process logs; scan before copy, then scan local helper logs."""
import base64,hashlib,importlib.util,json,os,pathlib,sys
from preflight import ROOT,ssh,write
spec=importlib.util.spec_from_file_location('t03_collect',ROOT/'scripts/integration/t03/collect.py')
old=importlib.util.module_from_spec(spec);spec.loader.exec_module(old)
PROGRAM=old.PROGRAM.replace("patterns.update([raw,base64.b64encode(raw),raw.hex().encode()])", "patterns.update([raw,raw.strip(),base64.b64encode(raw),base64.urlsafe_b64encode(raw).rstrip(b'='),raw.hex().encode()])")
PROGRAM=PROGRAM.replace("('password','uuid','secret','url','private-key','private_key')", "('password','uuid','secret','url','private-key','private_key','token','csrf_token','bootstrap_token','download_url')")

def inspect(value,found):
 if isinstance(value,dict):
  for k,v in value.items():
   if k in ('password','uuid','secret','url','private-key','private_key','token','csrf_token','bootstrap_token','download_url') and isinstance(v,str):found.add(v)
   inspect(v,found)
 elif isinstance(value,list):
  for item in value:inspect(item,found)

def main():
 os.umask(0o077);local=pathlib.Path(sys.argv[1]);mapping=json.loads((local/'mapping.json').read_text());generated=set()
 for p in local.glob('r*/rpc-*-input.json'):
  value=json.loads(p.read_text())
  if value.get('op')=='scan':generated.update(value['secrets'])
 for p in local.rglob('*.json'):
  if p.stat().st_size>8*1024*1024:continue
  try:inspect(json.loads(p.read_bytes()),generated)
  except (ValueError,UnicodeError):pass
 write(local/'aggregate-secrets.json',sorted(generated));summaries=[]
 for i in range(2):
  payload=dict(root=mapping['remote'],secrets=sorted(generated),action='scan')
  def program():return b'import json,base64\npayload=json.loads(base64.b64decode('+repr(base64.b64encode(json.dumps(payload).encode())).encode()+b'))\n'+PROGRAM.encode()
  p=ssh(i,'MESHSTRATA_PRODUCT_COMMIT='+mapping['commit']+' PYTHONDONTWRITEBYTECODE=1 python3 -',program(),timeout=120);assert p.returncode==0,'remote scan failed';write(local/('final-scan-'+str(i)+'.json'),p.stdout);result=json.loads(p.stdout);assert not result['findings'],'secret scan finding; preserve remote evidence'
  payload['action']='archive';p=ssh(i,'PYTHONDONTWRITEBYTECODE=1 python3 -',program(),timeout=120);assert p.returncode==0;write(local/('host'+str(i)+'-logs.tar.gz'),p.stdout)
  summaries.append(dict(host='AB'[i],log_files=result['log_files'],secret_patterns=result['patterns'],findings=0,private_archive_saved=True))
 patterns=set()
 for secret in generated:
  if not secret:continue
  raw=secret.encode()
  if len(raw)<6:continue
  patterns.update([raw,raw.strip(),base64.b64encode(raw),base64.urlsafe_b64encode(raw).rstrip(b'='),raw.hex().encode(),json.dumps(secret)[1:-1].encode()])
 for p in local.rglob('*.pem'):
  if b'PRIVATE KEY' in p.read_bytes():
   raw=p.read_bytes();patterns.update([raw,base64.b64encode(raw),raw.hex().encode(),json.dumps(raw.decode())[1:-1].encode()])
 logs=list(local.rglob('*.log'));findings=[]
 for p in logs:
  matches=sum(pattern in p.read_bytes() for pattern in patterns)
  if matches:findings.append(dict(path=str(p.relative_to(local)),matches=matches))
 assert not findings,'local helper log secret finding'
 summaries.append(dict(host='build',log_files=len(logs),secret_patterns=len(patterns),findings=0))
 write(local/'final-log-scan.json',summaries);print(json.dumps(summaries))
if __name__=='__main__':main()
