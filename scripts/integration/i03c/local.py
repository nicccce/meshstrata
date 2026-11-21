#!/usr/bin/env python3
"""I03-C isolated loopback acceptance through ordinary HTTPS and production CLIs.

No SQL, internal Go fixtures, SSH, host routes/DNS/firewall/TUN, or T02 files.
All secrets stay in a private temporary directory, never in argv or evidence.
"""
import argparse
import base64
import contextlib
import datetime as dt
import hashlib
import http.cookiejar
import http.server
import json
import os
from pathlib import Path
import secrets
import select
import shutil
import socket
import socketserver
import ssl
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request

LOCKED_SHA = '19cbe7ba1523a92ed2d0a30716ea093de57db05b7844db23de2b9c21fd7a1afa'
ROOT = Path(__file__).resolve().parents[3]

def stamp(seconds=0):
    return (dt.datetime.now(dt.timezone.utc) + dt.timedelta(seconds=seconds)).isoformat().replace('+00:00', 'Z')

def ident(kind):
    return kind + '_' + secrets.token_hex(16)

def pin(kind, spec):
    return dict(id=ident(kind), revision=1, digest=hashlib.sha256(json.dumps(spec, separators=(',', ':'), ensure_ascii=False).encode()).hexdigest())

def write(path, value):
    raw = json.dumps(value).encode() if isinstance(value, (dict, list)) else value
    path.write_bytes(raw if isinstance(raw, bytes) else raw.encode())
    path.chmod(0o600)

def port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]

def wait_for(fn, timeout=50):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            value = fn()
            if value:
                return value
        except (OSError, AssertionError, urllib.error.URLError) as exc:
            last = str(exc)[:600]
        time.sleep(.25)
    raise AssertionError('bounded wait expired: ' + str(last))

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args):
        return None

class HTTP:
    def __init__(self, origin, ca):
        self.origin = origin
        self.csrf = ''
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=str(ca))), urllib.request.HTTPCookieProcessor(self.jar), NoRedirect())

    def raw(self, method, path, data=None, status=(200,), authenticated=True, extra=None):
        headers = {'Content-Type': 'application/json'}
        if authenticated:
            headers.update(Origin=self.origin, **{'X-CSRF-Token': self.csrf})
        if extra:
            headers.update(extra)
        body = json.dumps(data).encode() if data is not None else None
        req = urllib.request.Request(self.origin + path, data=body, headers=headers, method=method)
        try:
            response = self.opener.open(req, timeout=12)
        except urllib.error.HTTPError as exc:
            response = exc
        raw = response.read()
        assert response.code in status, 'HTTP status %d (expected %s): %s' % (response.code, status, json.loads(raw).get('error',{}) if raw.startswith(b'{') else 'non-JSON')
        return raw, response.headers, response.code

    def api(self, method, path, data=None, status=(200, 202), **kwargs):
        raw, _, _ = self.raw(method, path, data, status, **kwargs)
        return json.loads(raw) if raw else None

class Owned:
    def __init__(self, argv, log, **kwargs):
        self.file = open(log, 'ab', buffering=0)
        self.p = subprocess.Popen([str(x) for x in argv], stdout=self.file, stderr=self.file, **kwargs)
        self.start = Path('/proc/%d/stat' % self.p.pid).read_text().split()[21]
    def stop(self):
        if self.p.poll() is None:
            assert Path('/proc/%d/stat' % self.p.pid).read_text().split()[21] == self.start
            self.p.terminate()
            try:
                self.p.wait(14)
            except subprocess.TimeoutExpired:
                self.p.kill()
                self.p.wait(5)
        self.file.close()

class Target(http.server.BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'
    def do_GET(self):
        body = b'meshstrata-i03c-controlled-loopback'
        self.send_response(200)
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *args):
        pass

class Echo(socketserver.BaseRequestHandler):
    def handle(self):
        self.request.settimeout(8)
        with contextlib.suppress(OSError):
            while data := self.request.recv(512):
                self.request.sendall(data)

class TCP(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True

def relay(a,b):
    end=time.monotonic()+10
    while time.monotonic()<end:
        readable,_,_=select.select([a,b],[],[],.25)
        for src in readable:
            data=src.recv(8192)
            if not data:return
            (b if src is a else a).sendall(data)

class HTTPProxy(socketserver.BaseRequestHandler):
    def handle(self):
        self.request.settimeout(10)
        try:
            raw=b''
            while b'\r\n\r\n' not in raw and len(raw)<4096:raw+=self.request.recv(1)
            auth=base64.b64encode((self.server.username+':'+self.server.password).encode())
            target=raw.split(b' ',2)[1].decode()
            allowed={self.server.canary,'127.0.0.1:%d'%self.server.target_port}
            if target not in allowed or not raw.startswith(('CONNECT '+target+' HTTP/1.1').encode()) or not any(line.lower().startswith(b'proxy-authorization:') and line.split(b':',1)[1].strip()==b'Basic '+auth for line in raw.split(b'\r\n')):
                self.request.sendall(b'HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n');return
            with socket.create_connection(tuple(target.rsplit(':',1)[:1])+ (int(target.rsplit(':',1)[1]),),timeout=3) as upstream:
                self.request.sendall(b'HTTP/1.1 200 Connection Established\r\n\r\n');relay(self.request,upstream)
        except OSError:pass

class SOCKSProxy(socketserver.BaseRequestHandler):
    def handle(self):
        self.request.settimeout(10)
        def read(n):
            data=b''
            while len(data)<n:
                chunk=self.request.recv(n-len(data))
                if not chunk:raise OSError('closed')
                data+=chunk
            return data
        try:
            version,n=read(2);methods=read(n)
            if version!=5 or 2 not in methods:return
            self.request.sendall(b'\x05\x02')
            version,n=read(2);username=read(n);n=read(1)[0];password=read(n)
            if version!=1 or username.decode()!=self.server.username or password.decode()!=self.server.password:
                self.request.sendall(b'\x01\x01');return
            self.request.sendall(b'\x01\x00');header=read(4)
            if header!=b'\x05\x01\x00\x01':return
            address=socket.inet_ntoa(read(4));targetport=int.from_bytes(read(2),'big')
            allowed={self.server.canary, '127.0.0.1:%d'%self.server.target_port}
            if address+':'+str(targetport) not in allowed:return
            with socket.create_connection((address,targetport),timeout=3) as upstream:
                self.request.sendall(b'\x05\x00\x00\x01\x7f\x00\x00\x01\x00\x00');relay(self.request,upstream)
        except (OSError,UnicodeError):pass

class Reality(socketserver.BaseRequestHandler):
    def handle(self):
        self.request.settimeout(3)
        with contextlib.suppress(OSError, ssl.SSLError):
            with self.server.context.wrap_socket(self.request, server_side=True): pass

class Run:
    def __init__(self, root, controller, agent, mihomo):
        self.root, self.controller, self.agent, self.mihomo = root, controller, agent, mihomo
        self.processes, self.agents, self.urls, self.passwords = [], [], [], []
        self.evidence = []
        self.pool_ports = set()
        self.ephemeral = tuple(map(int,Path("/proc/sys/net/ipv4/ip_local_port_range").read_text().split()))
    def record(self,label):
        self.evidence.append(label)
        print(label,flush=True)
    def cli(self, binary, action, cfg, data=None):
        run = subprocess.run([str(binary), action, '-config', str(cfg)], input=json.dumps(data).encode() if data else b'', stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
        assert run.returncode == 0, 'CLI action rejected: ' + action + ' ' + run.stderr.decode()[:240]
    def launch(self, binary, cfg, label):
        p = Owned([binary, 'run', '-config', cfg], self.root / (label + '.log'))
        self.processes.append(p)
        return p
    def start_controller(self):
        web, control = port(), port()
        self.origin = 'https://127.0.0.1:%d' % web
        self.node_origin = 'https://127.0.0.1:%d' % control
        private = self.root / 'controller-keys'
        cfg = dict(state_dir=str(self.root/'controller'), log_level='warn', listen_address='127.0.0.1:%d' % web, secure=dict(enable_v2=True, public_origin=self.origin, egress_probes=[self.echo], tls_cert_file=str(private/'web-cert.pem'), tls_key_file=str(private/'web-key.pem'), web_ca_file=str(private/'web-ca.pem'), allowed_origins=[self.origin], current_key_id='local-test', key_files={'local-test': str(private/'master.key')}, bootstrap_file=str(private/'bootstrap.json'), control_listen_address='127.0.0.1:%d' % control, controller_url=self.node_origin, server_names=['127.0.0.1']))
        self.ccfg = self.root/'controller.json'
        write(self.ccfg, cfg)
        self.cli(self.controller, 'prepare-local', self.ccfg)
        self.cp = self.launch(self.controller, self.ccfg, 'controller')
        self.http = HTTP(self.origin, private/'web-ca.pem')
        wait_for(lambda: self.http.api('GET', '/readyz'))
        self.password = secrets.token_urlsafe(24)
        self.passwords.append(self.password)
        self.cli(self.controller, 'initialize-admin', self.ccfg, dict(username='admin', password=self.password))
        self.login()
        self.record('ordinary_controller_https_login_csrf')
    def login(self):
        self.http.api('POST', '/api/v1/auth/login', dict(username='admin', password=self.password), status=(204,))
        self.http.csrf = self.http.api('GET', '/api/v1/auth/session')['csrf_token']
    def business_materials(self):
        folder=self.root/'business-materials';folder.mkdir(mode=0o700)
        def openssl(*args):
            p=subprocess.run(['openssl',*map(str,args)],capture_output=True,timeout=15)
            assert p.returncode==0,'local business material generation failed'
            return p.stdout
        openssl('req','-x509','-newkey','ec','-pkeyopt','ec_paramgen_curve:P-256','-nodes','-keyout',folder/'key.pem','-out',folder/'cert.pem','-days','1','-subj','/CN=test.local','-addext','basicConstraints=critical,CA:FALSE','-addext','subjectAltName=DNS:test.local')
        cert=(folder/'cert.pem').read_bytes();key=(folder/'key.pem').read_bytes()
        leaf=hashlib.sha256(openssl('x509','-in',folder/'cert.pem','-outform','DER')).hexdigest()
        openssl('genpkey','-algorithm','X25519','-out',folder/'x.pem')
        raw=openssl('pkey','-in',folder/'x.pem','-outform','DER')[-32:]
        public=openssl('pkey','-in',folder/'x.pem','-pubout','-outform','DER')[-32:]
        encode=lambda v:base64.urlsafe_b64encode(v).decode().rstrip('=')
        context=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);context.minimum_version=ssl.TLSVersion.TLSv1_3
        context.load_cert_chain(folder/'cert.pem',folder/'key.pem')
        self.reality=TCP(('127.0.0.1',0),Reality);self.reality.context=context
        threading.Thread(target=self.reality.serve_forever,daemon=True).start()
        self.business=dict(cert=cert,key=key,leaf=leaf,reality=encode(raw).encode(),public=encode(public),dest='127.0.0.1:%d'%self.reality.server_address[1])
    def pool(self):
        # Every Agent shares this host's loopback namespace. Keep listener pools
        # disjoint and outside kernel-assigned ephemeral TCP/QUIC source ports.
        for _ in range(128):
            first=10000+secrets.randbelow(19980)
            block=set(range(first,first+21))
            if block & self.pool_ports or not (first+20<self.ephemeral[0] or first>self.ephemeral[1]):continue
            held=[]
            try:
                for number in block:
                    for kind in (socket.SOCK_STREAM,socket.SOCK_DGRAM):
                        sock=socket.socket(socket.AF_INET,kind);held.append(sock);sock.bind(('127.0.0.1',number))
                self.pool_ports.update(block)
                return first
            except OSError:pass
            finally:
                for sock in held:sock.close()
        raise RuntimeError('no isolated loopback listener pool available')
    def node(self, protocols=('ss2022', 'trojan', 'vless', 'hysteria2')):
        created = self.http.api('POST', '/api/v1/nodes', dict(expected_revision=0, idempotency_key=ident('issue'), name='本地节点', connection_mode='direct', role='node', control_address='127.0.0.1', proxy_address='127.0.0.1'))
        self.passwords.append(created['token'])
        key = created['node_id']
        folder = self.root / ('a%d' % len(self.agents))
        folder.mkdir(mode=0o700)
        write(folder/'ca.pem', created['ca_pem'])
        write(folder/'enroll.token', created['token'])
        dns_spec = dict(bootstrap=['127.0.0.1'], resolvers=['127.0.0.1'], ipv6=False)
        dns = dict(pin=pin('dns', dns_spec), **dns_spec)
        sockets = []
        endpoints = []
        materials = []
        imports = []
        for purpose, label in [('tls_certificate','cert'),('tls_private_key','key'),('reality_private_key','reality')]:
            ref=dict(id=ident('material'),purpose=purpose)
            materials.append(ref)
            imports.append(dict(ref=ref,data=base64.b64encode(self.business[label]).decode()))
        for protocol in protocols:
            spec = dict(address='127.0.0.1', requested_port=0, transport='tcp', certificate=None, private_key=None, reality_key=None, reality_destination='', server_names=[], short_ids=[])
            endpoint=dict(host='test.local', port=0, protocol=protocol, cipher='', server_name='', reality_public_key='', short_id='')
            if protocol=='ss2022': endpoint['cipher']='2022-blake3-aes-128-gcm'
            elif protocol in ('trojan','hysteria2'):
                spec.update(certificate=materials[0],private_key=materials[1],tls_leaf_sha256=self.business['leaf'])
                endpoint.update(server_name='test.local',tls_leaf_sha256=self.business['leaf'])
                if protocol=='hysteria2': spec['transport']='udp'
            else:
                spec.update(reality_key=materials[2],reality_destination=self.business['dest'],server_names=['test.local'],short_ids=['0123456789abcdef'])
                endpoint.update(server_name='test.local',reality_public_key=self.business['public'],short_id='0123456789abcdef')
            # TLSLeafSHA256 is the first Go SocketSpec field when present.
            if 'tls_leaf_sha256' in spec: spec={'tls_leaf_sha256':spec['tls_leaf_sha256'],**{k:v for k,v in spec.items() if k!='tls_leaf_sha256'}}
            first = self.pool()
            sockets.append(dict(protocol=protocol, pin=pin('socket', spec), spec=spec, pool_first=first, pool_last=min(first+20, 65535)))
            endpoints.append(endpoint)
        approved=[dict(pin=pin('target',dict(node_id=key,type=kind,interface='',host='127.0.0.1',dial_ip='127.0.0.1',port=proxy.server_address[1],credential_ref='')),spec=dict(node_id=key,type=kind,interface='',host='127.0.0.1',dial_ip='127.0.0.1',port=proxy.server_address[1],credential_ref='')) for kind,proxy in self.proxies.items()]
        cfg = dict(state_dir=str(folder/'state'), log_level='warn', control=dict(mode='runtime', controller_url=self.node_origin, ca_file=str(folder/'ca.pem'), ca_fingerprint=created['ca_fingerprint'], node_id=key, token_file=str(folder/'enroll.token')), runtime=dict(enable_v2=True, materials=materials, mihomo_binary=str(self.mihomo), provisioning=dict(targets=approved, dns=dns, sockets=sockets, topology=dict(local_addresses=[], protected_endpoints=[self.node_origin.removeprefix('https://')], protected_networks=[], protected_interfaces=[]), egress_probes=[self.echo])))
        path = folder/'agent.json'
        write(path, cfg)
        self.cli(self.agent, 'enroll', path)
        self.cli(self.agent, 'prepare-runtime', path)
        self.cli(self.agent, 'import-materials', path,dict(materials=imports))
        current = self.http.api('GET', '/api/v1/nodes/'+key)
        prepared = self.http.api('PUT', '/api/v1/nodes/'+key+'/preparation', dict(expected_revision=current['revision'], idempotency_key=ident('prepare'), dns=dns, sockets=sockets))
        process = self.launch(self.agent, path, 'agent-%d' % len(self.agents))
        info = dict(id=key, path=path, cfg=cfg, process=process, dns=dns, sockets=sockets, endpoints=endpoints)
        self.agents.append(info)
        wait_for(lambda: self.current_observation(key), 45)
        self.record('normal_agent_mtls_v2_observation')
        return info
    def current_observation(self, key):
        out = self.http.api('GET', '/api/v1/nodes/'+key+'/preparation')
        return out if out['observation'] and out['observation']['current'] else None
    def default_policy(self, node):
        t = self.http.api('POST', '/api/v1/egress-targets', dict(expected_revision=0, idempotency_key=ident('target'), name='Node Default', target=dict(node_id=node['id'], type='node_default', interface='', host='', dial_ip='', port=0)))['pin']
        return self.http.api('POST', '/api/v1/egress-policies', dict(expected_revision=0, idempotency_key=ident('policy'), name='默认出口', node_id=node['id'], dns=node['dns']['pin'], targets=[t], rules=[dict(destination='MATCH', target=t, no_resolve=False)]))['pin']
    def route(self, nodes, protocol_index=0, name='中文 空格 / 标点 !',policy=None):
        for n in nodes:wait_for(lambda:self.current_observation(n['id']),45)
        default=policy is None
        policy = policy or self.default_policy(nodes[-1])
        indices = protocol_index if isinstance(protocol_index,list) else [protocol_index]*len(nodes)
        hops = [dict(node_id=n['id'], node_config=n['sockets'][idx]['pin'], dns=n['dns']['pin'], endpoint=n['endpoints'][idx], dial_ip='127.0.0.1') for n,idx in zip(nodes,indices)]
        v = self.http.api('POST', '/api/v1/routes', dict(expected_revision=0, idempotency_key=ident('draft'), name=name, hops=hops, egress=policy, egress_is_default=default, business_udp=False))
        p = v['pin']
        def reviewed():
            review = self.http.api('POST', '/api/v1/routes/'+p['id']+'/validate', dict(expected_revision=p['revision'], idempotency_key=ident('validate'), pin=p))
            if not review['blocking']:return review
            # Existing terminal policies also need evidence at the current node
            # revision. Wait for the real asynchronous refresh, never inject it.
            if all(i['path']=='/egress/evidence' and i['code'] in ('NOT_FOUND','TASK_EXPIRED') for i in review['blocking']):return None
            raise RuntimeError('route blocked: '+json.dumps(review['blocking']))
        wait_for(reviewed,45)
        task = self.http.api('POST', '/api/v1/routes/'+p['id']+'/publish', dict(expected_revision=p['revision'], idempotency_key=ident('publish'), pin=p, deadline=stamp(180)))
        self.wait_task(task['task_id'])
        published = wait_for(lambda: self.http.api('GET', '/api/v1/routes/'+p['id'])['published'])
        for n in nodes:
            wait_for(lambda: self.current_observation(n['id']))
        self.record('persistent_route_worker_publication')
        return published
    def wait_task(self, key):
        def check():
            v = self.http.api('GET', '/api/v1/tasks/'+key)
            if v['state'] in ('failed','cancelled','timed_out'): raise RuntimeError('task terminal: '+json.dumps(v))
            return v if v['state']=='succeeded' else None
        return wait_for(check, 90)
    def profile(self, name='命名规则'):
        return self.http.api('POST', '/api/v1/rule-profiles', dict(expected_revision=0, idempotency_key=ident('rules'), name=name, source='MATCH,All-Proxies', sets=[]))['pin']
    def subscribe(self, route, profile, expiry=1200):
        prefs=dict(mode='tun', ipv6=False, business_udp=False, all_routes_group=True, group_by_exit=True, selections=[], dns=dict(bootstrap=['192.0.2.53'], proxy_servers=['192.0.2.53'], business=['https://192.0.2.53/dns-query']))
        v=self.http.api('POST', '/api/v1/subscriptions', dict(expected_revision=0, idempotency_key=ident('create'), name='独立订阅 中文 !', expires_at=stamp(expiry), profile=profile, routes=[route], preferences=prefs, initial_selections=[dict(name='All-Proxies', route_id=route['id'])]))
        key=v['id']
        v=wait_for(lambda:self.confirmed(key),90)
        url=self.http.api('POST','/api/v1/subscriptions/'+key+'/download-link')['download_url']
        self.urls.append(url)
        raw,headers,_=self.http.raw('GET',url.removeprefix(self.origin))
        assert headers['Cache-Control']=='no-store' and headers['Referrer-Policy']=='no-referrer'
        config=json.loads(raw)
        assert config['tun']['enable'] and len(config['proxies'])==1
        self.passwords.extend(p.get('password',p.get('uuid','')) for p in config['proxies'])
        self.record('standard_https_full_config_download')
        return v,url,config
    def confirmed(self,key):
        v=self.http.api('GET','/api/v1/subscriptions/'+key)
        assert v['sync']!='failed','subscription failed: '+json.dumps(v)
        if v['sync']!='confirmed':
            states=[]
            for op in v['operations']:
                task=op.get('task_id')
                state=self.http.api('GET','/api/v1/tasks/'+task)['state'] if task else None
                if state in ('failed','cancelled','timed_out'):raise RuntimeError('subscription task terminal: '+json.dumps(self.http.api('GET','/api/v1/tasks/'+task)))
                cleanup_task=op.get('cleanup_task_id')
                cleanup_result=self.http.api('GET','/api/v1/tasks/'+cleanup_task) if cleanup_task else None
                cleanup=(cleanup_result['state'],[n['cleanup'] for n in cleanup_result['nodes']]) if cleanup_result else None
                states.append((op['state'],op['cleanup'],state,cleanup))
            marker=(key,str(states))
            if marker!=getattr(self,'last_pending',None):print('pending_operation_states: '+str(states),flush=True);self.last_pending=marker
        return v if v['sync']=='confirmed' else None
    def client(self,config,provider_url=None):
        self.passwords.extend(p.get('password',p.get('uuid','')) for p in config.get('proxies',[]))
        p=port();cfg=json.loads(json.dumps(config));cfg['hosts']={'test.local':'127.0.0.1'};cfg['dns']['use-hosts']=True;cfg['tun']={'enable':False};cfg['mixed-port']=p;cfg['bind-address']='127.0.0.1';cfg['dns']['listen']='127.0.0.1:%d'%port()
        if provider_url:
            cfg.pop('proxies',None)
            cfg['proxy-providers']={'subscription':{'type':'http','url':provider_url,'path':'./provider.json','interval':3600,'proxy':'DIRECT'}}
            cfg['tls']={'custom-certifactes':[(self.root/'controller-keys/web-ca.pem').read_text()]}
            # Third-party provider errors may print the capability URL. Keep this
            # isolated native client silent; real request assertions remain active.
            cfg['log-level']='silent'
            for group in cfg['proxy-groups']:group.pop('proxies',None);group['use']=['subscription']
        folder=self.root/ident('client');folder.mkdir(mode=0o700);write(folder/'config.json',cfg)
        write(folder/'full-default-tun.json',config)
        original=subprocess.run([str(self.mihomo),'-t','-d',str(folder),'-f',str(folder/'full-default-tun.json')],capture_output=True,timeout=20)
        assert original.returncode==0,'original full default TUN config rejected'
        check=subprocess.run([str(self.mihomo),'-t','-d',str(folder),'-f',str(folder/'config.json')],capture_output=True,timeout=20)
        assert check.returncode==0,'downloaded client config rejected'
        environment={k:v for k,v in os.environ.items() if 'proxy' not in k.lower()}
        environment['SSL_CERT_FILE']=str(self.root/'controller-keys/web-ca.pem')
        process=Owned([self.mihomo,'-d',folder,'-f',folder/'config.json'],folder/'kernel.log',env=environment);self.processes.append(process)
        target=self.target.server_address[1]
        def request():
            # HTTP proxy request uses only this isolated client mixed-port.
            with socket.create_connection(('127.0.0.1',p),timeout=3) as s:
                s.sendall(('GET http://127.0.0.1:%d/ HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nConnection: close\r\n\r\n'%(target,target)).encode())
                result=b''
                while data:=s.recv(4096):
                    result+=data
                    if b'meshstrata-i03c-controlled-loopback' in result: break
                assert b'meshstrata-i03c-controlled-loopback' in result, 'isolated HTTP response: '+repr(result[:160])
                return True
        wait_for(request,20)
        self.record('pinned_mihomo_isolated_tcp_actual_request')
        return process,request
    def fetch(self,url,status=(200,)):
        return self.http.raw('GET',url.removeprefix(self.origin),status=status)
    def denied(self,request):
        try: request()
        except (OSError,AssertionError): return True
        return False
    def copy(self,key):
        url=self.http.api('POST','/api/v1/subscriptions/'+key+'/download-link')['download_url']
        self.urls.append(url);return url
    def custom_egress(self,node,profile):
        for kind,proxy in self.proxies.items():
            target=dict(node_id=node['id'],type=kind,interface='',host='127.0.0.1',dial_ip='127.0.0.1',port=proxy.server_address[1])
            t=self.http.api('POST','/api/v1/egress-targets',dict(expected_revision=0,idempotency_key=ident('target'),name='认证出口 '+kind,target=target))['pin']
            body=dict(expected_revision=t['revision'],idempotency_key=ident('auth'),pin=t,username=proxy.username,password=proxy.password)
            t=self.http.api('POST','/api/v1/egress-targets/'+t['id']+'/credentials',body)['pin']
            assert self.http.api('POST','/api/v1/egress-targets/'+t['id']+'/credentials',body)['pin']==t
            def trigger():
                try:self.http.api('POST','/api/v1/egress-targets/'+t['id']+'/validate',dict(expected_revision=t['revision'],idempotency_key=ident('check'),pin=t));return True
                except AssertionError:return False
            wait_for(trigger,20)
            wait_for(lambda:self.http.api('GET','/api/v1/egress-targets/'+t['id']).get('observation',{}).get('current'),20)
            policy=self.http.api('POST','/api/v1/egress-policies',dict(expected_revision=0,idempotency_key=ident('policy'),name='固定出口',node_id=node['id'],dns=node['dns']['pin'],targets=[t],rules=[dict(destination='MATCH',target=t,no_resolve=False)]))['pin']
            route=self.route([node],policy=policy);v,url,config=self.subscribe(route,profile);self.client(config)
            detail=json.dumps(self.http.api('GET','/api/v1/egress-targets/'+t['id']))
            assert proxy.password not in detail and 'credential_ref' not in detail
            self.record('normal_mtls_'+kind+'_auth_observation_and_actual_terminal_tcp')
    def lifecycle(self,route,alternate,profile):
        a,ua,ca=self.subscribe(route,profile);pa,ra=self.client(ca)
        b,ub,cb=self.subscribe(route,profile);pb,rb=self.client(cb)
        provider=ua.replace('/config/','/provider/');self.urls.append(provider)
        raw,_,_=self.fetch(provider);assert set(json.loads(raw))=={'proxies'}
        self.client(ca,provider_url=provider)
        self.record('independent_provider_native_mihomo_https_fetch_actual_tcp')
        assert a['entries'][0]['identity_id']!=b['entries'][0]['identity_id'] and ca['proxies'][0]['password']!=cb['proxies'][0]['password'] and ca['proxies'][0]['port']!=cb['proxies'][0]['port']
        self.record('two_subscriptions_independent_credentials_ports_actual_tcp')
        assert self.copy(a['id'])==ua
        self.http.api('POST','/api/v1/subscriptions/'+a['id']+'/reset-token',dict(expected_revision=a['revision'],idempotency_key=ident('reset')))
        self.fetch(ua,(404,));ua=self.copy(a['id']);self.fetch(ua);self.fetch(ub);ra();rb()
        self.record('reset_link_isolated_copy_after_refresh_proxy_identity_unchanged')
        a=self.http.api('GET','/api/v1/subscriptions/'+a['id'])
        self.http.api('POST','/api/v1/subscriptions/'+a['id']+'/renew',dict(expected_revision=a['revision'],idempotency_key=ident('renew'),expires_at=stamp(1300)))
        a=wait_for(lambda:self.confirmed(a['id']),90);raw,_,_=self.fetch(ua);renewed=json.loads(raw)
        self.client(renewed);assert self.denied(ra),'old generation still authorized';rb()
        self.record('renew_new_generation_old_client_rejected_other_subscription_live')
        # Remove and re-add the same route through reviewed config updates.
        old=a['entries'][0]['identity_id']
        for selected in [alternate,route]:
            a=self.http.api('GET','/api/v1/subscriptions/'+a['id'])
            prefs=a['preferences'];prefs['selections']=[]
            a=self.http.api('PATCH','/api/v1/subscriptions/'+a['id'],dict(expected_revision=a['revision'],idempotency_key=ident('edit'),name=a['name'],expires_at=a['expires_at'],profile=profile,routes=[selected],preferences=prefs,initial_selections=[dict(name='All-Proxies',route_id=selected['id'])]))
            a=wait_for(lambda:self.confirmed(a['id']),90)
        assert old!=a['entries'][0]['identity_id'],'re-add reused revoked identity'
        raw,_,_=self.fetch(ua);pa,ra=self.client(json.loads(raw));self.record('route_remove_readd_new_identity_history_retained')
        # Adopt a new immutable profile explicitly, preserving entry identity.
        profile=self.http.api('PATCH','/api/v1/rule-profiles/'+profile['id'],dict(expected_revision=profile['revision'],idempotency_key=ident('rule-edit'),name='修订规则',source='DOMAIN,example.com,REJECT\nMATCH,All-Proxies',sets=[]))['pin']
        identity=b['entries'][0]['identity_id'];b=self.http.api('GET','/api/v1/subscriptions/'+b['id'])
        self.http.api('POST','/api/v1/subscriptions/'+b['id']+'/rule-profile',dict(expected_revision=b['revision'],idempotency_key=ident('adopt'),profile=profile))
        b=wait_for(lambda:self.confirmed(b['id']),90);raw,_,_=self.fetch(ub)
        assert b['entries'][0]['identity_id']==identity,'profile adoption changed entry identity'
        assert json.loads(raw)['rules'][:3]==['NETWORK,udp,REJECT','DOMAIN,example.com,REJECT','MATCH,All-Proxies'],'reviewed rule order or UDP guard lost'
        self.record('reviewed_profile_adoption_next_download_same_entry_identity')
        # A committed intent remains recoverable across normal Controller restart.
        pending=self.http.api('POST','/api/v1/subscriptions/'+b['id']+'/renew',dict(expected_revision=b['revision'],idempotency_key=ident('restart-renew'),expires_at=stamp(1400)))
        self.cp.stop();self.cp=self.launch(self.controller,self.ccfg,'controller');wait_for(lambda:self.http.api('GET','/readyz'));self.login()
        b=wait_for(lambda:self.confirmed(b['id']),90);raw,_,_=self.fetch(ub);pb,rb=self.client(json.loads(raw))
        self.record('controller_restart_persistent_subscription_intent_converges')
        # Expiry must be enforced by the Agent with Controller disconnected.
        c,uc,cc=self.subscribe(route,profile,expiry=45);pc,rc=self.client(cc)
        self.cp.stop();remaining=max(0,(dt.datetime.fromisoformat(c['expires_at'].replace('Z','+00:00'))-dt.datetime.now(dt.timezone.utc)).total_seconds())
        end=time.monotonic()+remaining+1
        while time.monotonic()<end: time.sleep(.25)
        # The local 250 ms expiry clock schedules a real kernel reconciliation.
        # Assert bounded enforcement, allowing the same 12 s as the runtime gate.
        wait_for(lambda:self.denied(rc),12);ra()
        self.cp=self.launch(self.controller,self.ccfg,'controller');wait_for(lambda:self.http.api('GET','/readyz'));self.login();self.fetch(uc,(404,))
        self.record('agent_expiry_with_controller_offline_old_client_rejected')
        b=self.http.api('GET','/api/v1/subscriptions/'+b['id'])
        self.http.api('DELETE','/api/v1/subscriptions/'+b['id'],dict(expected_revision=b['revision'],idempotency_key=ident('delete')))
        self.fetch(ub,(404,))
        def cleaned():
            value=self.http.api('GET','/api/v1/subscriptions/'+b['id'])
            return value if all(o['cleanup']=='confirmed' for o in value['operations']) else None
        wait_for(cleaned,90);assert self.denied(rb);self.fetch(ua);ra()
        self.record('delete_download_revoked_real_cleanup_confirmed_other_subscription_live')
        self.security(ua)
    def security(self,url):
        path=url.removeprefix(self.origin)
        for method,p in [('HEAD',path),('POST',path),('GET',path+'?x=1'),('GET',path+'/'),('GET',path.replace('/v1/','//v1/')),('GET',path.replace('/config/','/config/%2e%2e/config/')),('GET',path.replace('/subscriptions/','/%73ubscriptions/')),('GET','/subscriptions/v1/config'),('GET','/subscriptions/v1/provider/no-token')]:
            raw,headers,_=self.http.raw(method,p,status=(404,))
            assert not headers.get('Location') and headers['Cache-Control']=='no-store' and headers['Referrer-Policy']=='no-referrer'
            assert url.encode() not in raw and path.split('/')[-1].encode() not in raw
        anonymous=HTTP(self.origin,self.root/'controller-keys/web-ca.pem')
        anonymous.api('GET','/api/v1/subscriptions',status=(401,))
        self.http.api('POST','/api/v1/subscriptions/'+ident('sub')+'/download-link',status=(403,),authenticated=False)
        self.record('outer_https_sensitive_malformed_methods_headers_no_redirect_no_token_echo')
    def close(self):
        for p in reversed(self.processes):
            with contextlib.suppress(Exception): p.stop()
        for n in self.agents:
            with contextlib.suppress(Exception): self.cli(self.agent,'stop-runtime',n['path'])
        for path in self.root.rglob('*.log'):
            raw=path.read_bytes()
            for secret in self.urls+self.passwords+[u.rsplit('/',1)[-1] for u in self.urls]:
                if secret: assert secret.encode() not in raw,'secret leaked into local process log'

def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('--controller',type=Path,required=True)
    parser.add_argument('--agent',type=Path,required=True)
    parser.add_argument('--mihomo',type=Path,required=True)
    parser.add_argument('--evidence',type=Path,required=True)
    parser.add_argument('--scenario',choices=['all','matrix','egress','lifecycle','restart'],default='all')
    args=parser.parse_args()
    assert hashlib.sha256(args.mihomo.read_bytes()).hexdigest()==LOCKED_SHA
    with tempfile.TemporaryDirectory(prefix='mc-') as scratch:
        root=Path(scratch);root.chmod(0o700)
        run=Run(root,args.controller.resolve(),args.agent.resolve(),args.mihomo.resolve())
        server=http.server.ThreadingHTTPServer(('127.0.0.1',0),Target);run.target=server
        threading.Thread(target=server.serve_forever,daemon=True).start()
        echo=TCP(('127.0.0.1',0),Echo)
        run.echo='127.0.0.1:%d'%echo.server_address[1]
        run.proxies={}
        for kind,handler in [('http',HTTPProxy),('socks5',SOCKSProxy)]:
            proxy=TCP(('127.0.0.1',0),handler);proxy.username='local';proxy.password=secrets.token_urlsafe(24);proxy.canary=run.echo;proxy.target_port=server.server_address[1];run.proxies[kind]=proxy;run.passwords.append(proxy.password)
            threading.Thread(target=proxy.serve_forever,daemon=True).start()
        threading.Thread(target=echo.serve_forever,daemon=True).start()
        try:
            run.start_controller()
            run.business_materials()
            nodes=[run.node() for _ in range(1 if args.scenario in ('egress','restart') else 3)]
            profile=run.profile()
            if args.scenario=='restart':
                route=run.route(nodes)
                b,ub,cb=run.subscribe(route,profile);run.client(cb)
                profile=run.http.api('PATCH','/api/v1/rule-profiles/'+profile['id'],dict(expected_revision=profile['revision'],idempotency_key=ident('edit'),name='new',source='DOMAIN,example.com,REJECT\nMATCH,All-Proxies',sets=[]))['pin']
                run.http.api('POST','/api/v1/subscriptions/'+b['id']+'/rule-profile',dict(expected_revision=b['revision'],idempotency_key=ident('adopt'),profile=profile))
                b=run.http.api('GET','/api/v1/subscriptions/'+b['id'])
                run.http.api('POST','/api/v1/subscriptions/'+b['id']+'/renew',dict(expected_revision=b['revision'],idempotency_key=ident('restart'),expires_at=stamp(1400)))
                run.cp.stop();run.cp=run.launch(run.controller,run.ccfg,'controller');wait_for(lambda:run.http.api('GET','/readyz'));run.login()
                wait_for(lambda:run.confirmed(b['id']),90)
                run.record('controller_restart_persistent_subscription_intent_converges')
            if args.scenario in ('all','matrix'):
                for index,protocol in enumerate(('ss2022','trojan','vless','hysteria2')):
                    route=run.route([nodes[0]],index)
                    v,url,config=run.subscribe(route,profile)
                    process,_=run.client(config)
                    run.record('client_ingress_'+protocol+'_actual_tcp')
                    process.stop()
                    current=run.http.api('GET','/api/v1/subscriptions/'+v['id'])
                    run.http.api('DELETE','/api/v1/subscriptions/'+v['id'],dict(expected_revision=current['revision'],idempotency_key=ident('matrix-cleanup')))
                    wait_for(lambda:all(o['cleanup']=='confirmed' for o in run.http.api('GET','/api/v1/subscriptions/'+v['id'])['operations']),90)
                alternate=route
                for indices in ([0,1],[1,2,3]):
                    route=run.route(nodes[:len(indices)],indices)
                    v,url,config=run.subscribe(route,profile)
                    run.client(config)
                    run.record('mixed_%d_hop_actual_tcp'%len(indices))
            if args.scenario in ('all','egress'):run.custom_egress(nodes[-1],profile)
            if args.scenario=='lifecycle':
                alternate=run.route([nodes[0]])
                route=run.route(nodes,[0,1,2])
            if args.scenario in ('all','lifecycle'):run.lifecycle(route,alternate,profile)
            result=dict(date=dt.datetime.now(dt.timezone(dt.timedelta(hours=8))).date().isoformat(),scenario=args.scenario,network_mode='loopback TCP; TUN disabled and explicit test.local hosts mapping for runtime; original default TUN offline validation only',mihomo_sha256=LOCKED_SHA,checks=run.evidence)
            args.evidence.parent.mkdir(parents=True,exist_ok=True)
            write(args.evidence,result)
            print(json.dumps(result,ensure_ascii=False))
        finally:
            run.close()
            logs=ROOT/'.local/i03c-last-logs'
            logs.mkdir(exist_ok=True)
            for path in root.rglob('*.log'):
                shutil.copyfile(path,logs/(path.parent.name+'-'+path.name))
            server.shutdown();server.server_close();echo.shutdown();echo.server_close()
            for proxy in run.proxies.values():proxy.shutdown();proxy.server_close()
            if hasattr(run,'reality'): run.reality.shutdown();run.reality.server_close()

if __name__=='__main__':
    main()
