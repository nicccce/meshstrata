#!/usr/bin/env python3
"""T01 bounded remote acceptance driver. Secrets and raw logs stay under .local.

Reads the two authorized hosts from .local/test-servers.md. No SSH configuration,
firewall, package, service, or product implementation changes are performed.
"""
import argparse
import base64
import concurrent.futures
import hashlib
import gzip
import http.client
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import socket
import sqlite3
import ssl
import subprocess
import time
import traceback

ROOT = Path(__file__).resolve().parents[3]
COMMIT = 'f5702078aa48f946fb3c9ee35af65e991a4fc493'
KEY = str(Path.home()/'.ssh/id_ed25519_outbound')

def quote(s): return shlex.quote(str(s))
def stamp(): return time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())
def save(p, data):
    p = Path(p); p.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    p.write_bytes(data if isinstance(data, bytes) else (data if isinstance(data, str) else json.dumps(data, indent=2)).encode()); p.chmod(0o600)

class Host:
    def __init__(self, label, address, run, out):
        self.label, self.address, self.root, self.out = label, address, '/root/'+run+('-alpine' if label=='alpine' else ''), out/label
        self.out.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.processes = []
    def ssh(self, command, data=None, timeout=60, check=True):
        command='umask 077; '+command
        extra=['-o','ControlMaster=auto','-o','ControlPersist=30','-o','ControlPath=/tmp/'+Path(self.root).name+'-'+self.label+'.sock','-o','ServerAliveInterval=10','-o','ServerAliveCountMax=2']
        if self.label=='ubuntu' and os.environ.get('T01_SSH_JUMP'):
            jump=os.environ['T01_SSH_JUMP'];assert re.fullmatch(r'[0-9.]+',jump)
            extra+=['-o','ProxyCommand=ssh -i '+quote(KEY)+' -o BatchMode=yes -W %h:%p root@'+jump]
        r = subprocess.run(['ssh',*extra,'-i',KEY,'-o','BatchMode=yes','-o','ConnectTimeout=12', 'root@'+self.address,command], input=data, capture_output=True, timeout=timeout)
        if r.returncode and r.stderr:
            with (self.out/'ssh-errors.log').open('ab') as errors:errors.write(r.stderr+b'\n')
        if check and r.returncode: raise AssertionError(f'{self.label}: remote command exit {r.returncode}')
        return r
    def put(self, path, data):
        raw = data if isinstance(data, bytes) else (data if isinstance(data, str) else json.dumps(data)).encode()
        self.ssh(f'umask 077; cat > {quote(path)}',raw)
    def get(self, path): return self.ssh('cat '+quote(path)).stdout
    def digest(self,path):return self.ssh('sha256sum '+quote(path)).stdout.decode().split()[0]
    def control_sockets(self,pid):
        raw=self.ssh(f'for f in /proc/{pid}/fd/*; do v=$(readlink "$f" 2>/dev/null); case "$v" in socket:*) echo "$v";; esac; done; cat /proc/{pid}/net/tcp /proc/{pid}/net/tcp6').stdout.decode()
        owned=set(re.findall(r'socket:\[(\d+)\]',raw));result=set()
        for line in raw.splitlines():
            fields=line.split()
            if len(fields)>9 and fields[9] in owned:
                if fields[3]=='01' and int(fields[2].rsplit(':',1)[1],16)==9443:result.add(fields[9])
        return result
    def cli(self, binary, action, cfg, data=None, ok=True):
        t=time.monotonic(); command=' '.join(map(quote,[self.root+'/bin/'+binary,action,'-config',cfg]))
        if action=='run' and not ok:command='timeout -s TERM 15 '+command
        r=self.ssh(command,data,timeout=60,check=False)
        save(self.out/(Path(cfg).stem+'-'+action+'-'+str(time.time_ns())+'.log'),r.stdout+r.stderr)
        elapsed=time.monotonic()-t
        with (self.out/'cli-events.jsonl').open('a') as events:
            events.write(json.dumps({'at':stamp(),'binary':binary,'action':action,'config_name':Path(cfg).name,'config_relative':str(cfg).removeprefix(self.root+'/'),'expected_exit':0 if ok else 1,'actual_exit':r.returncode,'seconds':round(elapsed,4)})+'\n')
        assert r.returncode==(0 if ok else 1), f'{self.label} {action}: exit {r.returncode}'
        return time.monotonic()-t
    def start(self, binary, cfg, name, args=None, env="") :
        base=self.root+'/evidence/'+name
        # The supervisor reaps the exact child and records its real exit code.
        argv=args if args is not None else ['run','-config',cfg]
        inner=f'{env} {quote(self.root+"/bin/"+binary)} '+ ' '.join(map(quote,argv))+f' & p=$!; echo "$p" > {quote(base+".pid")}; wait "$p"; echo "$?" > {quote(base+".exit")}'
        self.ssh(f'nohup sh -c {quote(inner)} > {quote(base+".log")} 2>&1 < /dev/null &',timeout=10)
        pid=int(self.get(base+'.pid').strip()); p=(pid,base,binary); self.processes.append(p)
        return p
    def stop(self,p):
        pid,base,binary=p
        if self.ssh('test -f '+quote(base+'.exit'),check=False).returncode==0: return int(self.get(base+'.exit'))
        # Verify the PID still belongs to this test binary before signalling.
        self.ssh(f'test "$(readlink /proc/{pid}/exe)" = {quote(self.root+"/bin/"+binary)} && kill -TERM {pid}')
        for _ in range(60):
            if self.ssh('test -f '+quote(base+'.exit'),check=False).returncode==0: return int(self.get(base+'.exit'))
            time.sleep(.1)
        raise AssertionError(self.label+' child did not exit')
    def snapshot(self,name):
        r=self.ssh('(ss -lntup 2>/dev/null || netstat -lntup); cat /proc/meminfo; cat /etc/os-release; uname -m; cat /proc/1/comm; for x in go node python3 curl rc-status; do command -v $x || true; done; cat /proc/vmstat | sed -n "/^oom_kill/p"; cat /sys/fs/cgroup/memory.events 2>/dev/null || true')
        save(self.out/(name+'.txt'),r.stdout)
    def deploy(self):
        self.ssh(f'umask 077; if test -d {quote(self.root)}; then test "$(stat -c %a {quote(self.root)})" = 700 && test -d {quote(self.root+"/bin")}; else mkdir {quote(self.root)} && mkdir {quote(self.root+"/bin")} {quote(self.root+"/private")} {quote(self.root+"/state")} {quote(self.root+"/evidence")}; fi')
        if not (self.out/'before.txt').exists(): self.snapshot('before')
        arc=(ROOT/'.local/t01-artifacts.tar.gz').read_bytes()
        self.ssh('tar -xzf - -C '+quote(self.root+'/bin'),arc,timeout=600)
        r=self.ssh(f'cd {quote(self.root+"/bin")} && sha256sum -c SHA256SUMS && chmod 700 meshstrata-* *.test mihomo && ./meshstrata-controller version && ./meshstrata-agent version && ./mihomo -v')
        save(self.out/'artifact-verification.txt',r.stdout)
    def module(self, test, regex, name, env=''):
        base=self.root+'/evidence/'+name
        r=self.ssh(f'umask 077; {env} {quote(self.root+"/bin/"+test)} -test.run {quote(regex)} -test.v -test.timeout 8m > {quote(base+".log")} 2>&1; rc=$?; echo "$rc" > {quote(base+".exit")}; exit "$rc"',timeout=500,check=False)
        save(self.out/(name+'.log'),self.get(base+'.log'))
        return r.returncode

class API:
    def __init__(self,address,port,ca):
        self.address,self.port=address,port
        self.origin=f'https://{address}:{port}'
        self.context=ssl.create_default_context(cadata=ca.decode() if isinstance(ca,bytes) else ca)
        self.cookie=''; self.csrf=''; self.timings=[]
    def request(self,path,body=None,status=200,headers=None,method=None,authenticated=True,raw=False):
        h={'Content-Type':'application/json'}
        if body is not None: h['Origin']=self.origin
        if self.cookie and authenticated:h['Cookie']=self.cookie
        h.update(headers or {})
        c=http.client.HTTPSConnection(self.address,self.port,context=self.context,timeout=8)
        start=time.monotonic()
        c.request(method or ('POST' if body is not None else 'GET'),path, None if body is None else json.dumps(body),h)
        r=c.getresponse(); data=r.read(); self.timings.append({'path':path,'seconds':round(time.monotonic()-start,4),'status':r.status})
        assert r.status==status,f'{path}: expected {status}, got {r.status}'
        hs=dict(r.getheaders()); c.close()
        if raw:return data,hs
        return json.loads(data) if data else None
    def login(self,credentials):
        _,headers=self.request('/api/v1/auth/login',credentials,status=204,raw=True)
        cookie=headers['Set-Cookie']; assert all(s in cookie for s in ['Secure','HttpOnly','SameSite=Strict'])
        self.cookie=cookie.split(';')[0]; self.csrf=self.request('/api/v1/auth/session')['csrf_token']
    def write(self,path,body,status=200): return self.request(path,body,status,{'X-CSRF-Token':self.csrf})

class RemoteAPI(API):
    """SSH executes curl; curl performs CA/SAN-verified remote loopback HTTPS.
    This management transport never establishes public or cross-host reachability.
    Headers/body use stdin config; node private keys never use this class.
    """
    def __init__(self,host,port,ca,directory):
        super().__init__('127.0.0.1',port,ca)
        self.host=host;self.ca=directory+'/curl-ca-'+secrets.token_hex(6)+'.pem';host.put(self.ca,ca)
    def request(self,path,body=None,status=200,headers=None,method=None,authenticated=True,raw=False):
        h={'Content-Type':'application/json'}
        if body is not None:h['Origin']=self.origin
        if self.cookie and authenticated:h['Cookie']=self.cookie
        h.update(headers or {})
        values=[('url',self.origin+path),('cacert',self.ca),('request',method or ('POST' if body is not None else 'GET'))]
        values.extend(('header',k+': '+v) for k,v in h.items())
        if body is not None:values.append(('data',json.dumps(body)))
        config='\n'.join(k+' = '+json.dumps(v,ensure_ascii=False) for k,v in values)+'\n'
        start=time.monotonic();r=self.host.ssh('curl --silent --show-error --include --max-time 10 --config -',config.encode(),timeout=15,check=False)
        if r.returncode:raise OSError('remote verified HTTPS transport failed')
        head,data=r.stdout.split(b'\r\n\r\n',1);lines=head.decode().split('\r\n');code=int(lines[0].split()[1]);hs={}
        for line in lines[1:]:
            if ':' in line:
                k,v=line.split(':',1);hs[k]=v.strip()
        self.timings.append({'path':path,'seconds':round(time.monotonic()-start,4),'status':code,'transport':'remote loopback HTTPS'})
        assert code==status,f'{path}: expected {status}, got {code}'
        if raw:return data,hs
        return json.loads(data) if data else None

def wait(fn,seconds=40):
    deadline=time.monotonic()+seconds
    while time.monotonic()<deadline:
        try:
            if fn():return
        except (OSError,ssl.SSLError,http.client.HTTPException):pass
        time.sleep(.25)
    raise AssertionError('condition timed out')

def topology(controller,other,out,tag=None,local_only=False,management="public"):
    tag=tag or controller.label; p=controller.root+'/private/'+tag
    steps=[]
    def mark(name):
        steps.append({'case':name,'actual':'passed','at':stamp()}); save(out/(tag+'-steps.json'),steps)
    controller.ssh('mkdir '+quote(p))
    def new_api(port,ca):return RemoteAPI(controller,port,ca,p) if management=='ssh' else API(controller.address,port,ca)
    cfg={'state_dir':p+'/controller','log_level':'info','listen_address':'0.0.0.0:8443','secure':{'tls_cert_file':p+'/keys/web-cert.pem','tls_key_file':p+'/keys/web-key.pem','web_ca_file':p+'/keys/web-ca.pem','allowed_origins':[f'https://{controller.address}:8443'],'current_key_id':'v1','key_files':{'v1':p+'/keys/master.key'},'bootstrap_file':p+'/keys/bootstrap.json','control_listen_address':'0.0.0.0:9443','controller_url':f'https://{controller.address}:9443','server_names':[controller.address,'127.0.0.1']}}
    if management=='ssh':cfg['secure']['allowed_origins'].insert(0,'https://127.0.0.1:8443')
    cp=p+'/controller.json';controller.put(cp,cfg)
    controller.cli('meshstrata-controller','prepare-local',cp)
    controller.cli('meshstrata-controller','prepare-local',cp,ok=False)
    kernels=[]
    for h in ([controller] if local_only else [controller,other]):
        kd=h.root+'/private/'+tag+'-independent-kernel';h.ssh('mkdir '+quote(kd));h.put(kd+'/config.json',{'mode':'rule','log-level':'silent','external-controller-unix':kd+'/api.sock','rules':['MATCH,REJECT'],'tun':{'enable':False},'dns':{'enable':False}})
        kp=h.start('mihomo','',tag+'-'+h.label+'-independent-kernel',args=['-d',kd,'-f',kd+'/config.json']);kernels.append((h,kp))
        wait(lambda:h.ssh('test -S '+quote(kd+'/api.sock'),check=False).returncode==0)
        denied=h.ssh(f'T01_SOCKET={quote(kd+"/api.sock")} {quote(h.root+"/bin/t01-kernel.test")} -test.run "^TestUnixPermission$" -test.v',check=False)
        save(h.out/(tag+'-nonuid.txt'),denied.stdout+denied.stderr);assert denied.returncode==0
    c=controller.start('meshstrata-controller',cp,tag+'-controller')
    api=new_api(8443,controller.get(p+'/keys/web-ca.pem'))
    wait(lambda:api.request('/readyz'))
    api.request('/healthz'); html,_=api.request('/',raw=True); assert b'<html' in html
    instance=api.request('/api/v1/system')['instance_id'];mark('HTTPS chain/SAN, health/ready, embedded demo')
    controller.cli('meshstrata-controller','run',cp,ok=False)
    credentials={'username':'admin','password':secrets.token_urlsafe(24)}
    api.request('/api/v1/auth/initialize',credentials,403)
    proof=controller.get(p+'/keys/bootstrap-proof').decode().strip()
    api.request('/api/v1/auth/initialize',credentials,403,{'Origin':'https://wrong.invalid','X-Meshstrata-Bootstrap':proof})
    initialize_seconds=controller.cli('meshstrata-controller','initialize-admin',cp,json.dumps(credentials).encode())
    api.request('/api/v1/auth/initialize',credentials,409,{'X-Meshstrata-Bootstrap':proof})
    # Omit consumed bootstrap on restarts; delete proof after its negative cases.
    controller.ssh('rm '+quote(p+'/keys/bootstrap-proof'));cfg['secure'].pop('bootstrap_file');controller.put(cp,cfg)
    api.login(credentials);mark('bootstrap proof/Origin/one-time initialize and cookie flags')
    issue={'expected_revision':0,'idempotency_key':'local','name':'T01-local','connection_mode':'direct','role':'node','control_address':controller.address,'proxy_address':controller.address}
    api.request('/api/v1/nodes',issue,401,authenticated=False)
    api.request('/api/v1/nodes',issue,403)
    api.request('/api/v1/nodes',issue,403,{'X-CSRF-Token':'wrong'})
    api.request('/api/v1/nodes',issue,403,{'X-CSRF-Token':api.csrf,'Origin':'https://wrong.invalid'})
    api.write('/api/v1/nodes',{**issue,'connection_mode':'reverse_tunnel','idempotency_key':'reverse','proxy_address':'','relay_node_id':'node_'+'f'*32},501)
    mark('administrator authentication/Origin/CSRF and Reverse501')
    agents=[]
    for host,kind in ([(controller,'local')] if local_only else [(controller,'local'),(other,'remote')]):
        material=api.write('/api/v1/nodes',{**issue,'idempotency_key':kind,'name':'T01-'+kind})
        assert material==api.write('/api/v1/nodes',{**issue,'idempotency_key':kind,'name':'T01-'+kind})
        ap=host.root+'/private/'+tag+'-'+kind
        host.ssh('mkdir '+quote(ap));host.put(ap+'/ca.pem',material['ca_pem']);host.put(ap+'/token',material['token'])
        ac={'state_dir':ap+'/agent','log_level':'info','control':{'mode':'control_only','controller_url':material['controller_url'],'ca_file':ap+'/ca.pem','ca_fingerprint':material['ca_fingerprint'],'node_id':material['node_id'],'token_file':ap+'/token'}}
        if management=='ssh' and kind=='local':ac['control']['controller_url']='https://127.0.0.1:9443'
        acp=ap+'/agent.json';host.put(acp,ac)
        host.put(ap+'/wrong-ca.pem',controller.get(p+'/keys/web-ca.pem'))
        bad={**ac,'state_dir':ap+'/bad-ca-agent','control':{**ac['control'],'ca_file':ap+'/wrong-ca.pem'}};bad['control'].pop('ca_fingerprint')
        host.put(ap+'/bad-ca.json',bad);host.cli('meshstrata-agent','enroll',ap+'/bad-ca.json',ok=False)
        if kind=='local':
            bad={**ac,'state_dir':ap+'/bad-san-agent','control':{**ac['control'],'controller_url':'https://0.0.0.0:9443'}}
            host.put(ap+'/bad-san.json',bad);host.cli('meshstrata-agent','enroll',ap+'/bad-san.json',ok=False)
        host.cli('meshstrata-agent','enroll',acp)
        identity=ap+'/agent/identity/management-identity.json';before=host.digest(identity)
        host.cli('meshstrata-agent','enroll',acp);assert host.digest(identity)==before
        host.ssh('rm '+quote(ap+'/token'));ac['control'].pop('token_file');host.put(acp,ac)
        a=host.start('meshstrata-agent',acp,tag+'-'+kind+'-agent')
        nodepath='/api/v1/nodes/'+material['node_id']
        wait(lambda:api.request(nodepath)['control']=='online')
        assert api.request(nodepath)['verification']=='not_checked'
        host.cli('meshstrata-agent','healthcheck',acp);host.cli('meshstrata-agent','run',acp,ok=False)
        mark(kind+' real enroll/retry/online/not_checked/lock')
        agents.append(dict(host=host,cfg=ac,cp=acp,p=a,path=nodepath,identity=identity,before=before,material=material,private=ap))
    # Drop the first committed enrollment response body, then recover using the
    # same CSR over the real public endpoint. Private key stays on the driver.
    if management=='public':
        lost=api.write('/api/v1/nodes',{**issue,'idempotency_key':'lost-response','name':'T01-lost-response'})
        ld=out/'private'/tag/'lost-response';ld.mkdir(parents=True,mode=0o700)
        subprocess.run(['openssl','req','-new','-newkey','ed25519','-nodes','-keyout',str(ld/'key.pem'),'-out',str(ld/'csr.pem'),'-subj','/CN=T01'],capture_output=True,check=True)
        lc=API(controller.address,9443,lost['ca_pem']);body={'token':lost['token'],'csr_pem':(ld/'csr.pem').read_text()}
        connection=http.client.HTTPSConnection(controller.address,9443,context=lc.context,timeout=8)
        connection.request('POST','/enroll/v1',json.dumps(body),{'Content-Type':'application/json'})
        response=connection.getresponse();assert response.status==200;connection.close()
        recovered=lc.request('/enroll/v1',body);assert recovered==lc.request('/enroll/v1',body)
        mark('discarded first enrollment response; same CSR public retry stable')
    else:
        lost=api.write('/api/v1/nodes',{**issue,'idempotency_key':'lost-response','name':'T01-lost-response'})
        ld=out/'private'/tag/'lost-response';ld.mkdir(parents=True,mode=0o700)
        subprocess.run(['openssl','req','-new','-newkey','ed25519','-nodes','-keyout',str(ld/'key.pem'),'-out',str(ld/'csr.pem'),'-subj','/CN=T01'],capture_output=True,check=True)
        enrollment=new_api(9443,lost['ca_pem']);body={'token':lost['token'],'csr_pem':(ld/'csr.pem').read_text()}
        values=[('url',enrollment.origin+'/enroll/v1'),('cacert',enrollment.ca),('request','POST'),('header','Content-Type: application/json'),('data',json.dumps(body))]
        curl_config='\n'.join(k+' = '+json.dumps(v) for k,v in values)+'\n'
        dropped=controller.ssh('curl --silent --show-error --max-time 10 --output /dev/null --write-out '+quote('%{http_code}')+' --config -',curl_config.encode())
        assert dropped.stdout==b'200'
        recovered=enrollment.request('/enroll/v1',body);assert recovered==enrollment.request('/enroll/v1',body)
        mark('first HTTPS enrollment body discarded; same CSR response stable on retry')
    # Cross-host race uses public URLs; local smoke uses two separate identities.
    race_codes=[]
    race=api.write('/api/v1/nodes',{**issue,'idempotency_key':'race','name':'T01-race'})
    racepaths=[]
    for index,host in enumerate([controller,other]):
        rp=host.root+'/private/'+tag+'-race-'+str(index);host.ssh('mkdir '+quote(rp));host.put(rp+'/token',race['token']);host.put(rp+'/ca.pem',race['ca_pem'])
        rc={'state_dir':rp+'/agent','control':{'mode':'control_only','controller_url':'https://127.0.0.1:9443' if management=='ssh' and local_only else race['controller_url'],'node_id':race['node_id'],'ca_file':rp+'/ca.pem','token_file':rp+'/token'}}
        host.put(rp+'/agent.json',rc);racepaths.append((host,rp+'/agent.json'))
    def race_enroll(pair):
        host,path=pair
        r=host.ssh(' '.join(map(quote,[host.root+'/bin/meshstrata-agent','enroll','-config',path])),check=False)
        save(host.out/(tag+'-race-'+Path(path).parent.name+'.log'),r.stdout+r.stderr);return r.returncode
    with concurrent.futures.ThreadPoolExecutor(2) as pool: race_codes=list(pool.map(race_enroll,racepaths))
    assert sorted(race_codes)==[0,1];mark('distinct CSR single token race: '+('two local identities' if local_only else 'two hosts'))
    def node_probe(a,removed=False):
        host=a['host'];env=f'T01_IDENTITY={quote(a["identity"])} T01_CA={quote(a["private"]+"/ca.pem")} T01_URL={quote(a["cfg"]["control"]["controller_url"])} T01_REMOVED={"1" if removed else "0"}'
        label=tag+'-'+host.label+('-removed' if removed else '-mtls')
        code=host.module('t01-kernel.test','^TestNodeEndpoint$',label,env);assert code==0,host.label+' mTLS endpoint probe failed'
    for a in agents:node_probe(a)
    mark('mTLS renew updated certificate, artifact501 and no-certificate denial')
    # Exact controller stop models a real interruption, without firewall edits.
    assert controller.stop(c)==0
    for a in agents:a['host'].cli('meshstrata-agent','healthcheck',a['cp'])
    for h,k in kernels:assert h.ssh('kill -0 '+str(k[0]),check=False).returncode==0
    c=controller.start('meshstrata-controller',cp,tag+'-controller-restart')
    wait(lambda:api.request('/readyz'));assert api.request('/api/v1/system')['instance_id']==instance
    for a in agents:
        wait(lambda:api.request(a['path'])['control']=='online')
        assert a['host'].stop(a['p'])==0
        wait(lambda:api.request(a['path'])['control']=='offline')
        api.request('/healthz')
        for h,k in kernels:assert h.ssh('kill -0 '+str(k[0]),check=False).returncode==0
        a['p']=a['host'].start('meshstrata-agent',a['cp'],tag+'-'+a['host'].label+'-restart')
        wait(lambda:api.request(a['path'])['control']=='online')
        assert a['host'].digest(a['identity'])==a['before']
    mark('controller and tested agents restart/reconnect/identity persistence/independence')
    resource={}
    for a in agents:
        pid=a['p'][0];resource[a['host'].label]=a['host'].ssh(f'cat /proc/{pid}/status | sed -n "/VmRSS/p;/VmHWM/p;/Threads/p"').stdout.decode()
    resource['controller']=controller.ssh(f'cat /proc/{c[0]}/status | sed -n "/VmRSS/p;/VmHWM/p;/Threads/p"').stdout.decode()
    expiring=api.write('/api/v1/nodes',{**issue,'idempotency_key':'expiring','name':'T01-expiring'})
    save(out/'private'/tag/'expiring.json',expiring)
    removals=[]
    for a in agents:
        captured=a['host'].control_sockets(a['p'][0]);assert captured,'no actual Agent control socket captured before removal'
        node=api.request(a['path']);task=api.write(a['path']+'/remove',{'expected_revision':node['revision'],'idempotency_key':'remove-'+a['host'].label},202)
        api.request(a['path'],status=404)
        wait(lambda:captured.isdisjoint(a['host'].control_sockets(a['p'][0])))
        a['host'].cli('meshstrata-agent','healthcheck',a['cp'])
        save(a['host'].out/(tag+'-removed-socket.json'),{'captured_control_sockets':len(captured),'original_established_sockets_absent':True,'agent_still_healthy':True})
        node_probe(a,removed=True)
        assert a['host'].stop(a['p'])==0;removals.append(task['task_id'])
    mark('remove202/detail404/old certificate rejected/agents SIGTERM')
    # Password change invalidates both independently issued sessions, logout too.
    second=new_api(8443,controller.get(p+'/keys/web-ca.pem'));second.login(credentials)
    new_password=secrets.token_urlsafe(24)
    api.write('/api/v1/auth/password',{'current_password':credentials['password'],'new_password':new_password},204)
    api.request('/api/v1/auth/session',status=401);second.request('/api/v1/auth/session',status=401)
    credentials['password']=new_password;api.login(credentials);old_cookie=api.cookie
    api.write('/api/v1/auth/logout',{},204);api.cookie=old_cookie;api.request('/api/v1/auth/session',status=401)
    resource['controller_after_password']=controller.ssh(f'cat /proc/{c[0]}/status | sed -n "/VmRSS/p;/VmHWM/p;/Threads/p"').stdout.decode()
    assert controller.stop(c)==0;mark('password invalidates both sessions; logout invalidates session')
    dbpath=out/'private'/tag/'controller.db';save(dbpath,controller.get(p+'/controller/controller.db'))
    with sqlite3.connect(dbpath) as db:
        for task in removals:
            assert db.execute('SELECT applied,verification,cleanup_state,result_sequence FROM task_nodes WHERE task_id=?',(task,)).fetchone()==(0,'not_checked','pending',0)
            assert db.execute('SELECT state FROM tasks WHERE id=?',(task,)).fetchone()[0]=='queued'
    for h,k in kernels:assert h.stop(k)==0
    mark('removal database queued/applied=false/not_checked/cleanup=pending')
    return {'status':'passed','transport':('remote loopback admin HTTPS; local node loopback; remote node public mTLS' if not local_only else 'remote loopback HTTPS/mTLS via SSH command') if management=='ssh' else 'public HTTPS/mTLS; no tunnel','instance_stable':True,'identity_stable':True,'csr_race_exit_codes':race_codes,'initialize_cli_seconds':round(initialize_seconds,3),'resource':resource,'api_samples':api.timings,'business_execution':'control_only; proxy not_checked','removal':'queued/applied=false/not_checked/cleanup=pending'}

def main():
    parser=argparse.ArgumentParser();parser.add_argument('--run',default='meshstrata-t01-'+time.strftime('%Y%m%d-%H%M%S'));parser.add_argument('--phase',choices=['deploy','deploy-missing','refresh','modules','kernel','smoke','smoke-local','topologies','cleanup'],required=True);parser.add_argument('--host',choices=['ubuntu','alpine','all'],default='all');parser.add_argument('--attempt',default='');parser.add_argument('--management',choices=['public','ssh'],default='public');args=parser.parse_args()
    assert not args.attempt or re.fullmatch(r'[a-zA-Z0-9-]+',args.attempt)
    assert re.fullmatch(r'meshstrata-t01-[a-zA-Z0-9-]+',args.run)
    os.umask(0o077);out=ROOT/'.local'/args.run;out.mkdir(exist_ok=True,mode=0o700)
    addresses=re.findall(r'`(\d+\.\d+\.\d+\.\d+)`', (ROOT/'.local/test-servers.md').read_text())[:2]
    assert len(addresses)==2
    hosts=[Host(label,address,args.run,out) for label,address in zip(['ubuntu','alpine'],addresses)]
    results={'product_commit':COMMIT,'started_at':stamp(),'phase':args.phase,'run':args.run,'results':{}}
    assert (ROOT/'.local/stage1-artifacts/COMMIT').read_text().strip()==COMMIT
    selected=[h for h in hosts if args.host in ['all',h.label]]
    try:
        if args.phase=='deploy':
            for h in selected:h.deploy();results['results'][h.label]='sha256 verified'
        elif args.phase=='deploy-missing':
            for h in selected:
                for line in (ROOT/'.local/stage1-artifacts/SHA256SUMS').read_text().splitlines():
                    digest,name=line.split();path=h.root+'/bin/'+name
                    current=h.ssh('sha256sum '+quote(path),check=False).stdout.decode().split()
                    if not current or current[0]!=digest:
                        h.ssh('umask 077; gzip -dc > '+quote(path),gzip.compress((ROOT/'.local/stage1-artifacts'/name).read_bytes(),compresslevel=1),timeout=600)
                h.put(h.root+'/bin/SHA256SUMS',(ROOT/'.local/stage1-artifacts/SHA256SUMS').read_bytes())
                r=h.ssh('cd '+quote(h.root+'/bin')+' && chmod 700 meshstrata-* *.test mihomo && sha256sum -c SHA256SUMS && ./meshstrata-controller version && ./meshstrata-agent version && ./mihomo -v')
                save(h.out/'artifact-verification.txt',r.stdout);results['results'][h.label]='all SHA256 verified'
        elif args.phase=='refresh':
            for h in selected:
                h.ssh('umask 077; gzip -dc > '+quote(h.root+'/bin/.t01-kernel.upload')+' && chmod 700 '+quote(h.root+'/bin/.t01-kernel.upload')+' && mv '+quote(h.root+'/bin/.t01-kernel.upload')+' '+quote(h.root+'/bin/t01-kernel.test'),gzip.compress((ROOT/'.local/stage1-artifacts/t01-kernel.test').read_bytes(),compresslevel=1),timeout=600)
                h.put(h.root+'/bin/SHA256SUMS',(ROOT/'.local/stage1-artifacts/SHA256SUMS').read_bytes())
                r=h.ssh('cd '+quote(h.root+'/bin')+' && chmod 700 t01-kernel.test && sha256sum -c SHA256SUMS');save(h.out/'supplementary-verification.txt',r.stdout)
                results['results'][h.label]='all SHA256 verified after test-only helper refresh'
        elif args.phase in ['modules','kernel']:
            for h in selected:
                cases={}
                for binary,regex,name in [('control.test','^TestControlOnly','control-only'),('agent-enrollment.test','^TestTrustBeforeToken','trust-before-token'),('enrollment.test','TestTokenAtomic|TestRealTLS|TestCARestart','enrollment-boundaries'),('auth.test','TestLifecycle|TestBootstrap|TestEmptyDatabase','auth-boundaries')]:
                    if args.phase=='modules':cases[name]=h.module(binary,regex,name)
                h.ssh('umask 077; mkdir '+quote(h.root+'/private/b03'))
                env=f'B03_MIHOMO_BINARY={quote(h.root+"/bin/mihomo")} B03_ARTIFACT_DIR={quote(h.root+"/private/b03")} GOMAXPROCS=1'
                cases['real-kernel']=h.module('b03.test','^TestRealKernelCompatibility$','real-kernel',env)
                save(h.out/'kernel-evidence.json',h.get(h.root+'/private/b03/evidence.json'));results['results'][h.label]=cases
        elif args.phase in ['topologies','smoke','smoke-local']:
            pairs=[(h,h) for h in selected] if args.phase in ['smoke','smoke-local'] else [(c,o) for c,o in [hosts,hosts[::-1]] if args.host in ['all',c.label]]
            for c,o in pairs:
                try:results['results'][c.label]=topology(c,o,out,tag=c.label+'-'+args.phase+('-'+args.attempt if args.attempt else '') if args.phase in ['smoke','smoke-local'] else c.label+'-managed'+('-'+args.attempt if args.attempt else '') if args.management=='ssh' else None,local_only=args.phase in ['smoke','smoke-local'],management='ssh' if args.phase=='smoke-local' else args.management)
                except Exception as e:
                    save(out/(c.label+'-'+args.phase+'-error.log'),traceback.format_exc())
                    results['results'][c.label]={'status':'failed','error_type':type(e).__name__,'error':str(e) if isinstance(e,AssertionError) else 'see private driver log'}
                finally:
                    for h in hosts:
                        for p in reversed(h.processes):
                            try:h.stop(p)
                            except Exception:
                                save(out/(h.label+'-cleanup-error-'+str(p[0])+'.log'),traceback.format_exc())
                save(out/(args.phase+'.json'),results)
        elif args.phase=='cleanup':
            for h in hosts:
                # Only PID files produced by this driver's supervisors; stop checks exe path.
                r=h.ssh('find '+quote(h.root+'/evidence')+' -name "*.pid"')
                codes={}
                for path in r.stdout.decode().splitlines():
                    base=path[:-4];pid=int(h.get(path));exe=h.ssh(f'readlink /proc/{pid}/exe',check=False).stdout.decode().strip()
                    if exe.startswith(h.root+'/bin/'):codes[Path(base).name]=h.stop((pid,base,Path(exe).name))
                h.snapshot('after');results['results'][h.label]={'stopped':codes,'retained_private_root':h.root}
    finally:
        results['finished_at']=stamp();save(out/(args.phase+('-'+args.host if args.host!='all' else '')+'.json'),results)
    print(json.dumps({'phase':args.phase,'evidence':str(out),'results':results['results']},ensure_ascii=False) if args.phase not in ['topologies','smoke','smoke-local'] else json.dumps({'phase':args.phase,'evidence':str(out),'statuses':{k:v['status'] for k,v in results['results'].items()}}))

if __name__=='__main__':main()
