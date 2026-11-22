#!/usr/bin/env python3
"""One private, stdio-only remote worker; owns and checks all child PIDs."""
import base64,contextlib,hashlib,http.server,importlib.util,json,os,pathlib,socket,ssl,subprocess,sys,threading,time,traceback
os.umask(0o077)
ROOT=pathlib.Path(sys.argv[1]);PROCS={};SERVERS=[];SECRETS=[];CLIENTS={};AGENTS=[];STREAMS={};ALLOWED={'127.0.0.1',os.environ.get('SSH_CONNECTION','').split(' ')[0]}
spec=importlib.util.spec_from_file_location('i03c',ROOT/'helpers/i03c/local.py');lib=importlib.util.module_from_spec(spec);spec.loader.exec_module(lib)

def save(path,data):
 p=pathlib.Path(path);assert p.is_relative_to(ROOT);p.parent.mkdir(parents=True,exist_ok=True,mode=0o700);p.write_bytes(base64.b64decode(data));p.chmod(0o600)
def spawn(argv,label,env=None):
 p=lib.Owned(argv,ROOT/(label+'.log'),env=env);PROCS[str(p.p.pid)]=p
 ledger=[dict(pid=int(k),start=v.start,argv=v.p.args) for k,v in PROCS.items()];lib.write(ROOT/'processes.json',ledger)
 return str(p.p.pid)
def stop_owned(p):
 if p.p.poll() is None:
  args=pathlib.Path('/proc/%d/cmdline'%p.p.pid).read_bytes().rstrip(b'\0').split(b'\0');assert args==[str(a).encode() for a in p.p.args],'owned command changed'
  assert os.readlink('/proc/%d/exe'%p.p.pid)==str(pathlib.Path(p.p.args[0]).resolve()),'owned executable changed'
 p.stop()
def request(key):
 c=CLIENTS[key]
 with socket.create_connection(('127.0.0.1',c['port']),timeout=4) as s:
  target=c.get('target','127.0.0.1:18080');s.sendall(('GET http://'+target+'/ HTTP/1.1\r\nHost: '+target+'\r\nConnection: close\r\n\r\n').encode());raw=b''
  while b'meshstrata-i03c-controlled-loopback' not in raw:
   b=s.recv(4096)
   if not b:break
   raw+=b
  return b'meshstrata-i03c-controlled-loopback' in raw

def call(v):
 op=v['op']
 if op=='file':save(v['path'],v['data']);return True
 if op=='read':
  p=pathlib.Path(v['path']);assert p.is_relative_to(ROOT);return base64.b64encode(p.read_bytes()).decode()
 if op=='cli':
  p=subprocess.run([str(ROOT/v['binary']),v['action'],'-config',v['config']],input=base64.b64decode(v.get('data','')),capture_output=True,timeout=40)
  lib.write(ROOT/('cli-'+v['action']+'-'+str(time.time_ns())+'.log'),p.stdout+p.stderr)
  return dict(exit=p.returncode)
 if op=='check-free':
  held=[]
  try:
   for port in v['ports']:
    for kind in (socket.SOCK_STREAM,socket.SOCK_DGRAM):
     s=socket.socket(socket.AF_INET,kind);s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);held.append(s);s.bind(('0.0.0.0',port))
   return True
  finally:
   for s in held:s.close()
 if op=='spawn':return spawn(v['argv'],v['label'])
 if op=='stop':stop_owned(PROCS[v['pid']]);return True
 if op=='agent':AGENTS.append(v['config']);return True
 if op=='probe-start':
  ALLOWED.update(v['allowed']);checks=[]
  for transport,port in v['ports']:
   if transport=='tcp':
    class Probe(lib.socketserver.BaseRequestHandler):
     def handle(self):
      if self.client_address[0] not in ALLOWED:return
      self.request.settimeout(3)
      with contextlib.suppress(OSError):self.request.sendall(self.request.recv(64))
    server=lib.TCP(('0.0.0.0',port),Probe);threading.Thread(target=server.serve_forever,daemon=True).start()
   else:
    class UDP(lib.socketserver.BaseRequestHandler):
     def handle(self):
      if self.client_address[0] in ALLOWED:self.request[1].sendto(self.request[0][:64],self.client_address)
    server=lib.socketserver.ThreadingUDPServer(('0.0.0.0',port),UDP);threading.Thread(target=server.serve_forever,daemon=True).start()
   SERVERS.append(server);checks.append([transport,port])
  return checks
 if op=='probe':
  out=[]
  for transport,port in v['ports']:
   s=socket.socket(socket.AF_INET,socket.SOCK_STREAM if transport=='tcp' else socket.SOCK_DGRAM);s.settimeout(2);payload=os.urandom(32);ok=False;error=None
   try:
    s.connect((v['host'],port));s.sendall(payload);ok=s.recv(64)==payload
   except OSError as e:error=type(e).__name__
   out.append(dict(transport=transport,port=port,pass_=ok,error=error,source=s.getsockname(),destination=[v['host'],port]));s.close()
  return out
 if op=='probe-stop':
  for server in SERVERS:server.shutdown();server.server_close()
  SERVERS.clear();return True
 if op=='aux':
  ALLOWED.update(v['allowed'])
  class AllowedEcho(lib.Echo):
   def handle(self):
    if self.client_address[0] in ALLOWED:super().handle()
  target=lib.TCP(('127.0.0.1',18080),lib.Target);echo=lib.TCP(('0.0.0.0',38443),AllowedEcho)
  for s in [target,echo]:SERVERS.append(s);threading.Thread(target=s.serve_forever,daemon=True).start()
  class TrackedSocket:
   def __init__(self,s,server):self.s=s;self.server=server
   def __getattr__(self,key):return getattr(self.s,key)
   def sendall(self,data):
    if data.startswith(b'HTTP/1.1 407') or data==b'\x01\x01':self.server.stats['rejected']+=1
    if data.startswith(b'HTTP/1.1 200') or data.startswith(b'\x05\x00\x00'):self.server.stats['accepted']+=1
    return self.s.sendall(data)
  class TrackedHTTP(lib.HTTPProxy):
   def handle(self):self.request=TrackedSocket(self.request,self.server);super().handle()
  class TrackedSOCKS(lib.SOCKSProxy):
   def handle(self):self.request=TrackedSocket(self.request,self.server);super().handle()
  for kind,handler,port in [('http',TrackedHTTP,18081),('socks5',TrackedSOCKS,18082)]:
   s=lib.TCP(('127.0.0.1',port),handler);s.kind=kind;s.stats={'accepted':0,'rejected':0};s.username=v['username'];s.password=v['password'];s.canary=v['canary'];s.target_port=18080;SERVERS.append(s);threading.Thread(target=s.serve_forever,daemon=True).start()
  ctx=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);ctx.minimum_version=ssl.TLSVersion.TLSv1_3;ctx.load_cert_chain(ROOT/'business/cert.pem',ROOT/'business/key.pem');s=lib.TCP(('127.0.0.1',18083),lib.Reality);s.context=ctx;SERVERS.append(s);threading.Thread(target=s.serve_forever,daemon=True).start();return True
 if op=='proxy-stats':return {s.kind:s.stats for s in SERVERS if hasattr(s,'kind')}
 if op=='proxy-stop':
  for s in list(SERVERS):
   if getattr(s,'kind',None)==v['kind']:s.shutdown();s.server_close();SERVERS.remove(s)
  return True
 if op=='client':
  key=v['key'];folder=ROOT/key;folder.mkdir(mode=0o700);cfg=v['config'];SECRETS.extend(p.get('password',p.get('uuid','')) for p in cfg.get('proxies',[]));lib.write(folder/'default.json',cfg)
  cfg['tun']={'enable':False};cfg['mixed-port']=lib.port();cfg['bind-address']='127.0.0.1';cfg['hosts']=v['hosts'];cfg['dns']['use-hosts']=True;cfg['dns']['listen']='127.0.0.1:'+str(lib.port());cfg['log-level']='silent'
  if v.get('provider'):
   api_port=lib.port();api_secret=lib.secrets.token_urlsafe(24);SECRETS.append(api_secret);cfg['external-controller']='127.0.0.1:'+str(api_port);cfg['secret']=api_secret
   cfg.pop('proxies',None);cfg['proxy-providers']={'subscription':{'type':'http','url':v['provider'],'path':'./provider.json','interval':3600,'proxy':'DIRECT'}};cfg['tls']={'custom-certifactes':[v['ca']]}
   for g in cfg['proxy-groups']:g.pop('proxies',None);g['use']=['subscription']
  lib.write(folder/'config.json',cfg)
  for file in ['default.json','config.json']:
   p=subprocess.run([str(ROOT/'mihomo'),'-t','-d',str(folder),'-f',str(folder/file)],capture_output=True,timeout=20);lib.write(folder/(file+'.check.log'),p.stdout+p.stderr)
   assert p.returncode==0,'client validation failed'
  env={k:val for k,val in os.environ.items() if 'proxy' not in k.lower()};env['SSL_CERT_FILE']=str(ROOT/'web-ca.pem')
  pid=spawn([str(ROOT/'mihomo'),'-d',str(folder),'-f',str(folder/'config.json')],key,env)
  CLIENTS[key]={'port':cfg['mixed-port'],'pid':pid,'target':v.get('target','127.0.0.1:18080'),'api_port':cfg.get('external-controller'),'api_secret':cfg.get('secret')};return dict(key=key,pid=pid)
 if op=='stream-open':
  c=CLIENTS[v['key']];s=socket.create_connection(('127.0.0.1',c['port']),timeout=4);s.sendall(b'CONNECT 127.0.0.1:18080 HTTP/1.1\r\nHost: 127.0.0.1:18080\r\n\r\n');raw=b''
  while b'\r\n\r\n' not in raw:
   data=s.recv(4096)
   if not data:raise OSError('CONNECT closed')
   raw+=data
  assert b' 200 ' in raw.split(b'\r\n')[0],'CONNECT rejected';STREAMS[v['stream']]=s;return True
 if op=='stream-close':STREAMS.pop(v['stream']).close();return True
 if op=='stream-request':
  try:
   s=STREAMS[v['stream']];s.sendall(b'GET / HTTP/1.1\r\nHost: controlled.local\r\nConnection: keep-alive\r\n\r\n');raw=b''
   while b'meshstrata-i03c-controlled-loopback' not in raw:
    data=s.recv(4096)
    if not data:break
    raw+=data
   return b'meshstrata-i03c-controlled-loopback' in raw
  except OSError:return False
 if op=='provider-refresh':
  c=CLIENTS[v['key']];req=lib.urllib.request.Request('http://'+c['api_port']+'/providers/proxies/subscription',headers={'Authorization':'Bearer '+c['api_secret']},method='PUT');opener=lib.urllib.request.build_opener(lib.urllib.request.ProxyHandler({}))
  with opener.open(req,timeout=15) as response:return dict(status=response.code)
 if op=='request':
  try:return request(v['key'])
  except (OSError,AssertionError):return False
 if op=='snapshot':
  interfaces=[]
  for p in pathlib.Path('/sys/class/net').iterdir():
   try:(p/'tun_flags').stat();error=None
   except OSError as e:error=e.errno
   interfaces.append(dict(name=p.name,tun_flags_errno=error))
  managed=[]
  for proc in pathlib.Path('/proc').iterdir():
   if not proc.name.isdigit():continue
   try:
    if os.readlink(proc/'exe')!=str(ROOT/'mihomo'):continue
    argv=(proc/'cmdline').read_bytes().rstrip(b'\0').split(b'\0');inodes=[]
    for fd in (proc/'fd').iterdir():
     with contextlib.suppress(OSError):
      link=os.readlink(fd)
      if link.startswith('socket:['):inodes.append(link[8:-1])
    sockets=[]
    for name in ['tcp','tcp6','udp','udp6']:
     for line in (proc/'net'/name).read_text().splitlines()[1:]:
      columns=line.split()
      if columns[9] in inodes:sockets.append(dict(transport=name,local=columns[1],remote=columns[2],state=columns[3],inode=columns[9]))
    managed.append(dict(pid=int(proc.name),start=(proc/'stat').read_text().split()[21],argv=[a.decode() for a in argv],sockets=sockets))
   except OSError:continue
  return dict(managed_processes=managed,time_ns=time.time_ns(),processes={pid:dict(exit=p.p.poll(),start=p.start) for pid,p in PROCS.items()},interfaces=interfaces,unix=pathlib.Path('/proc/net/unix').read_text())
 if op=='scan':
  secrets=v['secrets']+SECRETS;findings=[]
  for p in ROOT.rglob('*.log'):
   raw=p.read_bytes()
   count=sum(1 for secret in secrets if secret and secret.encode() in raw)
   if count:findings.append(dict(file=str(p.relative_to(ROOT)),matches=count))
  return findings
 if op=='finish':
  for stream in STREAMS.values():stream.close()
  STREAMS.clear()
  for p in reversed(list(PROCS.values())):stop_owned(p)
  stops=[]
  for c in AGENTS:
   p=subprocess.run([str(ROOT/'meshstrata-agent'),'stop-runtime','-config',c],capture_output=True,timeout=40);lib.write(ROOT/('stop-'+str(len(stops))+'.log'),p.stdout+p.stderr);stops.append(p.returncode)
  for s in SERVERS:s.shutdown();s.server_close()
  SERVERS.clear()
  live=[]
  for pid,p in PROCS.items():
   if p.p.poll() is None:live.append(pid)
  sockets=[p for p in ROOT.rglob('*.sock') if p.is_socket()];active=pathlib.Path('/proc/net/unix').read_text();removed=[]
  for p in sockets:
   if str(p) not in active:p.unlink();removed.append(str(p.relative_to(ROOT)))
  return dict(stop_runtime_exits=stops,live_recorded_pids=live,sockets=[str(p.relative_to(ROOT)) for p in sockets if p.exists()],stale_socket_files_removed=removed)
 raise ValueError('unknown worker operation')
try:
 for line in sys.stdin:
  try:result={'ok':True,'result':call(json.loads(line))}
  except Exception as e:
   lib.write(ROOT/('worker-error-'+str(time.time_ns())+'.log'),traceback.format_exc());result={'ok':False,'error':type(e).__name__}
  print(json.dumps(result),flush=True)
finally:
 with contextlib.suppress(Exception):call({'op':'finish'})
