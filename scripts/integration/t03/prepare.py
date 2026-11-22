#!/usr/bin/env python3
import hashlib,json,pathlib,subprocess,sys,tarfile,io,traceback
from preflight import *
os.umask(0o077)
local=pathlib.Path(sys.argv[1]);mapping=json.loads((local/'mapping.json').read_text());remote=mapping['remote']
def prepare():
 for i in range(2):
     if i==1:
         p=ssh(i,'command -v python3 || apk add --no-cache python3',timeout=180);write(local/'alpine-dependency.txt',p.stdout+p.stderr);assert p.returncode==0,'dependency install failed'
     p=ssh(i,'umask 077; mkdir -p '+remote);write(local/('mkdir-%d.txt'%i),p.stdout+p.stderr);assert p.returncode==0
     memory=io.BytesIO()
     with tarfile.open(fileobj=memory,mode='w:gz') as tar:
         for p in ART.iterdir():
             if p.is_file():tar.add(p,arcname=p.name)
     p=ssh(i,'umask 077; tar -xzf - -C '+remote,memory.getvalue(),timeout=600);assert p.returncode==0,'upload failed'
     cmd='cd '+remote+' && sha256sum -c SHA256SUMS && ./meshstrata-controller version && ./meshstrata-agent version && ./mihomo -v'
     p=ssh(i,cmd);write(local/('artifact-%d.txt'%i),p.stdout+p.stderr);assert p.returncode==0,'remote artifacts failed'
     print('AB'[i]+' artifact hashes and CLI execution verified',flush=True)
try:prepare()
except Exception:
 write(local/"prepare-error.txt",traceback.format_exc());print("prepare failed; private diagnostics saved");sys.exit(1)
