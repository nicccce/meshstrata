"""SSH worker RPC; every raw response remains in the ignored private evidence root."""
import base64,json,pathlib,select,subprocess,time
from preflight import ROOT,SSH,IPS,ssh,write
class Worker:
 def __init__(self,index,local,remote):
  self.index=index;self.local=local;self.remote=pathlib.Path(remote);self.seq=0
  for source,name in [(ROOT/'scripts/integration/t03/worker.py','worker.py'),(ROOT/'scripts/integration/i03c/local.py','helpers/i03c/local.py')]:
   p=ssh(index,'umask 077; mkdir -p '+str(pathlib.Path(remote,name).parent)+' && cat > '+remote+'/'+name,source.read_bytes());assert p.returncode==0,'worker upload'
  self.log=open(local/('worker-ssh-%d.log'%index),'ab',buffering=0)
  self.p=subprocess.Popen(SSH+['root@'+IPS[index],'PYTHONDONTWRITEBYTECODE=1 python3 '+remote+'/worker.py '+remote],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=self.log)
 def call(self,op,**kwargs):
  self.seq+=1;value=dict(op=op,**kwargs);write(self.local/('rpc-%d-%04d-input.json'%(self.index,self.seq)),value)
  self.p.stdin.write(json.dumps(value).encode()+b'\n');self.p.stdin.flush()
  assert select.select([self.p.stdout],[],[],110)[0],'worker response timeout'
  raw=self.p.stdout.readline();write(self.local/('rpc-%d-%04d-output.json'%(self.index,self.seq)),raw)
  v=json.loads(raw);assert v['ok'],'remote worker '+v.get('error','error');return v['result']
 def file(self,path,data):
  if not isinstance(data,bytes):data=json.dumps(data).encode() if isinstance(data,(dict,list)) else data.encode()
  return self.call('file',path=str(path),data=base64.b64encode(data).decode())
 def close(self):
  self.p.stdin.close();self.p.wait(50);self.log.close()
