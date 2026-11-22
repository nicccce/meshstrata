#!/usr/bin/env python3
"""T03 fixed-product cross-host normal CLI/API acceptance. Raw data is private."""
import argparse,base64,contextlib,datetime as dt,hashlib,importlib.util,json,os,pathlib,secrets,ssl,subprocess,sys,time,traceback
from preflight import ROOT,COMMIT,ART,IPS,ssh,write
from transport import Worker
spec=importlib.util.spec_from_file_location('i03c',ROOT/'scripts/integration/i03c/local.py');lib=importlib.util.module_from_spec(spec);spec.loader.exec_module(lib)
class RemoteProcess:
 def __init__(self,w,pid):self.w=w;self.pid=pid
 def stop(self):self.w.call('stop',pid=self.pid)
class Run(lib.Run):
 def __init__(self,local,workers,ci,phase):
  super().__init__(local, pathlib.Path('meshstrata-controller'),pathlib.Path('meshstrata-agent'),pathlib.Path('mihomo'))
  self.workers=workers;self.ci=ci;self.phase=phase;self.locations={};self.echo=IPS[0]+':38443';self.case_prefix=phase;self.events=[];self.client_host=ci;self.paths={};self.aux_auth=secrets.token_urlsafe(24);self.passwords.append(self.aux_auth)
  self.base=workers[0].remote/phase;self.downloads={};self.route_policies={}
 def record(self,label):
  event=dict(label=label,time=lib.stamp(),controller='AB'[self.ci]);self.events.append(event);write(self.root/'events.json',self.events);print(self.phase,label,flush=True)
 def remote_file(self,i,path,data):
  self.workers[i].file(path,data);self.locations[str(path)]=i
 def cli(self,binary,action,cfg,data=None):
  i=self.locations[str(cfg)];r=self.workers[i].call('cli',binary=str(binary),action=action,config=str(cfg),data=base64.b64encode(json.dumps(data).encode() if data else b'').decode());assert r['exit']==0,'CLI '+action+' rejected'
 def launch(self,binary,cfg,label):
  w=self.workers[self.locations[str(cfg)]];pid=w.call('spawn',argv=[str(w.remote/binary),'run','-config',str(cfg)],label=self.phase+'-'+label);p=RemoteProcess(w,pid);self.processes.append(p);return p
 def start_controller(self):
  self.origin='https://'+IPS[self.ci]+':8443';self.node_origin='https://'+IPS[self.ci]+':9443';private=self.base/'keys';self.ccfg=self.base/'controller.json'
  cfg=dict(state_dir=str(self.base/'c'),log_level='warn',listen_address='0.0.0.0:8443',secure=dict(enable_v2=True,public_origin=self.origin,egress_probes=[self.echo],tls_cert_file=str(private/'web-cert.pem'),tls_key_file=str(private/'web-key.pem'),web_ca_file=str(private/'web-ca.pem'),allowed_origins=[self.origin],current_key_id='t03',key_files={'t03':str(private/'master.key')},bootstrap_file=str(private/'bootstrap.json'),control_listen_address='0.0.0.0:9443',controller_url=self.node_origin,server_names=['127.0.0.1',IPS[self.ci]]))
  self.remote_file(self.ci,self.ccfg,cfg);self.cli(self.controller,'prepare-local',self.ccfg)
  ca=base64.b64decode(self.workers[self.ci].call('read',path=str(private/'web-ca.pem')));p=self.root/'controller-keys';p.mkdir(mode=0o700);lib.write(p/'web-ca.pem',ca)
  for w in self.workers:w.file(w.remote/'web-ca.pem',ca)
  self.cp=self.launch(self.controller,self.ccfg,'controller');self.http=lib.HTTP(self.origin,p/'web-ca.pem');lib.wait_for(lambda:self.http.api('GET','/readyz'))
  self.password=secrets.token_urlsafe(24);self.passwords.append(self.password);self.cli(self.controller,'initialize-admin',self.ccfg,dict(username='admin',password=self.password));self.login();self.record('ordinary_public_https_controller_admin_login_csrf')
 def materials(self):
  folder=self.root/'business';folder.mkdir(mode=0o700)
  def openssl(*args):
   p=subprocess.run(['openssl',*map(str,args)],capture_output=True,timeout=15);assert p.returncode==0;return p.stdout
  openssl('req','-x509','-newkey','ec','-pkeyopt','ec_paramgen_curve:P-256','-nodes','-keyout',folder/'key.pem','-out',folder/'cert.pem','-days','1','-subj','/CN=test.local','-addext','basicConstraints=critical,CA:FALSE','-addext','subjectAltName=DNS:test.local')
  leaf=hashlib.sha256(openssl('x509','-in',folder/'cert.pem','-outform','DER')).hexdigest();openssl('genpkey','-algorithm','X25519','-out',folder/'x.pem');raw=openssl('pkey','-in',folder/'x.pem','-outform','DER')[-32:];public=openssl('pkey','-in',folder/'x.pem','-pubout','-outform','DER')[-32:];enc=lambda b:base64.urlsafe_b64encode(b).decode().rstrip('=')
  self.business=dict(cert=(folder/'cert.pem').read_bytes(),key=(folder/'key.pem').read_bytes(),leaf=leaf,reality=enc(raw).encode(),public=enc(public),dest='127.0.0.1:18083')
  self.passwords.extend([self.business['key'].decode(),self.business['reality'].decode()])
  for w in self.workers:
   for name in ['cert.pem','key.pem']:w.file(w.remote/'business'/name,(folder/name).read_bytes())
   w.call('aux',username='t03',password=self.aux_auth,canary=self.echo,allowed=IPS)
 def node(self,hi,protocols=('ss2022','trojan','vless','hysteria2'),extra=False):
  self.workers[hi].call('check-free',ports=[29443,29444,29445] if extra else [28443,28444,28445,28446])
  created=self.http.api('POST','/api/v1/nodes',dict(expected_revision=0,idempotency_key=lib.ident('issue'),name='T03 '+str(hi),connection_mode='direct',role='node',control_address=IPS[hi],proxy_address=IPS[hi]));self.passwords.append(created['token']);key=created['node_id'];folder=self.base/('n%d'%len(self.agents));self.remote_file(hi,folder/'ca.pem',created['ca_pem']);self.remote_file(hi,folder/'token',created['token'])
  dns_spec=dict(bootstrap=['127.0.0.1'],resolvers=['127.0.0.1'],ipv6=False);dns=dict(pin=lib.pin('dns',dns_spec),**dns_spec);materials=[];imports=[]
  for purpose,label in [('tls_certificate','cert'),('tls_private_key','key'),('reality_private_key','reality')]:
   ref=dict(id=lib.ident('material'),purpose=purpose);materials.append(ref);imports.append(dict(ref=ref,data=base64.b64encode(self.business[label]).decode()))
  sockets=[];endpoints=[]
  for protocol in protocols:
   spec=dict(address='0.0.0.0',requested_port=0,transport='tcp',certificate=None,private_key=None,reality_key=None,reality_destination='',server_names=[],short_ids=[]);ep=dict(host='test.local',port=0,protocol=protocol,cipher='',server_name='',reality_public_key='',short_id='')
   if protocol=='ss2022':ep['cipher']='2022-blake3-aes-128-gcm'
   elif protocol in ('trojan','hysteria2'):
    spec.update(certificate=materials[0],private_key=materials[1],tls_leaf_sha256=self.business['leaf']);ep.update(server_name='test.local',tls_leaf_sha256=self.business['leaf'])
    if protocol=='hysteria2':spec['transport']='udp'
   else:spec.update(reality_key=materials[2],reality_destination=self.business['dest'],server_names=['test.local'],short_ids=['0123456789abcdef']);ep.update(server_name='test.local',reality_public_key=self.business['public'],short_id='0123456789abcdef')
   if 'tls_leaf_sha256' in spec:spec={'tls_leaf_sha256':spec['tls_leaf_sha256'],**{k:v for k,v in spec.items() if k!='tls_leaf_sha256'}}
   first=28446 if protocol=='hysteria2' else 28443;last=first if protocol=='hysteria2' else 28445
   if extra:first=29443;last=29445
   sockets.append(dict(protocol=protocol,pin=lib.pin('socket',spec),spec=spec,pool_first=first,pool_last=last));endpoints.append(ep)
  approved=[]
  for kind,port in [('http',18081),('socks5',18082)]:
   spec=dict(node_id=key,type=kind,interface='',host='127.0.0.1',dial_ip='127.0.0.1',port=port,credential_ref='');approved.append(dict(pin=lib.pin('target',spec),spec=spec))
  cfg=dict(state_dir=str(folder/'s'),log_level='warn',control=dict(mode='runtime',controller_url=self.node_origin,ca_file=str(folder/'ca.pem'),ca_fingerprint=created['ca_fingerprint'],node_id=key,token_file=str(folder/'token')),runtime=dict(enable_v2=True,materials=materials,mihomo_binary=str(self.workers[hi].remote/'mihomo'),provisioning=dict(targets=approved,dns=dns,sockets=sockets,topology=dict(local_addresses=[],protected_endpoints=[ip+':'+str(p) for ip in IPS for p in (8443,9443)],protected_networks=[],protected_interfaces=[]),egress_probes=[self.echo])))
  path=folder/'agent.json';self.remote_file(hi,path,cfg);self.workers[hi].call('agent',config=str(path));self.cli(self.agent,'enroll',path);self.cli(self.agent,'prepare-runtime',path);self.cli(self.agent,'import-materials',path,dict(materials=imports));current=self.http.api('GET','/api/v1/nodes/'+key);body=dict(expected_revision=current['revision'],idempotency_key=lib.ident('prepare'),dns=dns,sockets=sockets);a=self.http.api('PUT','/api/v1/nodes/'+key+'/preparation',body);assert self.http.api('PUT','/api/v1/nodes/'+key+'/preparation',body)==a
  self.http.api('PUT','/api/v1/nodes/'+key+'/preparation',dict(body,idempotency_key=lib.ident('stale')),status=(409,))
  process=self.launch(self.agent,path,'agent'+str(len(self.agents)));info=dict(id=key,path=path,cfg=cfg,process=process,dns=dns,sockets=sockets,endpoints=endpoints,host=hi);self.agents.append(info);self.observe_node(key,hi);self.record('cross_machine_enroll_mtls_import_prepare_observation');return info
 def observe_node(self,key,hi):
  try:lib.wait_for(lambda:self.current_observation(key),55)
  finally:
   write(self.root/'node-final-dto.json',self.http.api('GET','/api/v1/nodes/'+key));write(self.root/'preparation-final-dto.json',self.http.api('GET','/api/v1/nodes/'+key+'/preparation'))
   for w in self.workers:write(self.root/('snapshot-'+str(w.index)+'.json'),w.call('snapshot'))
 def route(self,nodes,protocol_index=0,name='T03 fixed path',policy=None):
  for n in nodes:lib.wait_for(lambda:self.current_observation(n['id']),45)
  default=policy is None;policy=policy or self.default_policy(nodes[-1]);indices=protocol_index if isinstance(protocol_index,list) else [protocol_index]*len(nodes)
  hops=[dict(node_id=n['id'],node_config=n['sockets'][idx]['pin'],dns=n['dns']['pin'],endpoint=n['endpoints'][idx],dial_ip=IPS[n['host']]) for n,idx in zip(nodes,indices)]
  v=self.http.api('POST','/api/v1/routes',dict(expected_revision=0,idempotency_key=lib.ident('draft'),name=name,hops=hops,egress=policy,egress_is_default=default,business_udp=False));p=v['pin']
  def review():
   r=self.http.api('POST','/api/v1/routes/'+p['id']+'/validate',dict(expected_revision=p['revision'],idempotency_key=lib.ident('validate'),pin=p));write(self.root/'last-review.json',r)
   if not r['blocking']:return r
   if all(i['path']=='/egress/evidence' and i['code'] in ('NOT_FOUND','TASK_EXPIRED') for i in r['blocking']):return None
   raise RuntimeError('route blocked')
  lib.wait_for(review,50);task=self.http.api('POST','/api/v1/routes/'+p['id']+'/publish',dict(expected_revision=p['revision'],idempotency_key=lib.ident('publish'),pin=p,deadline=lib.stamp(180)));t=self.wait_task(task['task_id']);write(self.root/('task-'+task['task_id']+'.json'),t);published=lib.wait_for(lambda:self.http.api('GET','/api/v1/routes/'+p['id'])['published']);self.paths[p['id']]=nodes;self.route_policies[p['id']]=policy
  self.record('real_worker_publication_'+str(len(nodes))+'_nodes');return published
 def subscribe(self,route,profile,expiry=1200):
  self.current_ingress=self.paths[route['id']][0]
  v,url,cfg=super().subscribe(route,profile,expiry);self.downloads[v['id']]=url
  for op in v['operations']:
   if op.get('task_id'):write(self.root/('subscription-task-'+op['task_id']+'.json'),self.http.api('GET','/api/v1/tasks/'+op['task_id']))
  return v,url,cfg
 def client(self,config,provider_url=None):
  host=self.client_host;w=self.workers[host];key=lib.ident('client');self.passwords.extend(p.get('password',p.get('uuid','')) for p in config['proxies']);ingress=config['proxies'][0]['server'];node=getattr(self,'current_ingress',self.agents[0]);address=IPS[node['host']]
  # Use actual downloaded numeric server when supplied; hosts map is explicit evidence.
  hosts={'test.local':address};r=w.call('client',key=key,config=config,hosts=hosts,provider=provider_url,ca=(self.root/'controller-keys/web-ca.pem').read_text());p=RemoteProcess(w,r['pid']);p.key=key;self.processes.append(p)
  def request():
   assert w.call('request',key=key),'controlled TCP target not reached';return True
  lib.wait_for(request,24);self.record('fixed_mihomo_remote_client_actual_tcp');return p,request
 def lifecycle(self,route,alternate,profile):
  a,ua,ca=self.subscribe(route,profile);pa,ra=self.client(ca);b,ub,cb=self.subscribe(route,profile);pb,rb=self.client(cb)
  assert a['entries'][0]['identity_id']!=b['entries'][0]['identity_id'] and ca['proxies'][0]['password']!=cb['proxies'][0]['password'] and ca['proxies'][0]['port']!=cb['proxies'][0]['port'];self.record('two_subscriptions_independent_identity_credentials_ports')
  provider=ua.replace('/config/','/provider/');self.urls.append(provider);raw,_,_=self.fetch(provider);assert set(json.loads(raw))=={'proxies'};pc,rc=self.client(ca,provider_url=provider);assert self.workers[self.client_host].call('provider-refresh',key=pc.key)['status'] in (200,204);rc();self.record('native_mihomo_https_provider_fetch_and_refresh_actual_tcp');pc.stop()
  self.http=lib.HTTP(self.origin,self.root/'controller-keys/web-ca.pem');self.login();assert self.copy(a['id'])==ua
  self.http.api('POST','/api/v1/subscriptions/'+a['id']+'/reset-token',dict(expected_revision=a['revision'],idempotency_key=lib.ident('reset')));self.fetch(ua,(404,));ua=self.copy(a['id']);raw,_,_=self.fetch(ua);assert json.loads(raw)['proxies']==ca['proxies'];ra();rb();self.downloads[a['id']]=ua;self.record('fresh_login_copy_stable_reset_old_url_dead_proxy_credentials_unchanged')
  a=self.http.api('GET','/api/v1/subscriptions/'+a['id']);self.http.api('POST','/api/v1/subscriptions/'+a['id']+'/renew',dict(expected_revision=a['revision'],idempotency_key=lib.ident('renew'),expires_at=lib.stamp(1300)));a=lib.wait_for(lambda:self.confirmed(a['id']),90);raw,_,_=self.fetch(ua);renewed=json.loads(raw);self.client(renewed);assert self.denied(ra);rb();self.record('renew_new_generation_old_client_rejected_other_subscription_live')
  old=a['entries'][0]['identity_id']
  for selected in [alternate,route]:
   a=self.http.api('GET','/api/v1/subscriptions/'+a['id']);prefs=a['preferences'];prefs['selections']=[];self.http.api('PATCH','/api/v1/subscriptions/'+a['id'],dict(expected_revision=a['revision'],idempotency_key=lib.ident('edit'),name=a['name'],expires_at=a['expires_at'],profile=profile,routes=[selected],preferences=prefs,initial_selections=[dict(name='All-Proxies',route_id=selected['id'])]));a=lib.wait_for(lambda:self.confirmed(a['id']),90)
  assert old!=a['entries'][0]['identity_id'];raw,_,_=self.fetch(ua);pa,ra=self.client(json.loads(raw));self.record('route_remove_readd_new_identity')
  profile=self.http.api('PATCH','/api/v1/rule-profiles/'+profile['id'],dict(expected_revision=profile['revision'],idempotency_key=lib.ident('rules'),name='T03 adopted',source='DOMAIN,example.com,REJECT\nMATCH,All-Proxies',sets=[]))['pin'];identity=b['entries'][0]['identity_id'];b=self.http.api('GET','/api/v1/subscriptions/'+b['id']);self.http.api('POST','/api/v1/subscriptions/'+b['id']+'/rule-profile',dict(expected_revision=b['revision'],idempotency_key=lib.ident('adopt'),profile=profile));b=lib.wait_for(lambda:self.confirmed(b['id']),90);raw,_,_=self.fetch(ub);assert b['entries'][0]['identity_id']==identity;assert json.loads(raw)['rules'][:3]==['NETWORK,udp,REJECT','DOMAIN,example.com,REJECT','MATCH,All-Proxies'];self.record('explicit_rule_adoption_next_download_same_entry_identity')
  self.http.api('POST','/api/v1/subscriptions/'+b['id']+'/renew',dict(expected_revision=b['revision'],idempotency_key=lib.ident('restart'),expires_at=lib.stamp(1400)));self.cp.stop();self.cp=self.launch(self.controller,self.ccfg,'controller');lib.wait_for(lambda:self.http.api('GET','/readyz'));self.login();b=lib.wait_for(lambda:self.confirmed(b['id']),90);raw,_,_=self.fetch(ub);pb,rb=self.client(json.loads(raw));self.record('normal_controller_restart_committed_subscription_converges')
  node=self.agents[0];node['process'].stop();node['process']=self.launch(self.agent,node['path'],'agent-restart');lib.wait_for(lambda:self.http.api('GET','/api/v1/nodes/'+node['id'])['control']=='online',40);rb();self.record('normal_agent_restart_same_state_authorized_traffic')
  c,uc,cc=self.subscribe(route,profile,expiry=45);pc,rc=self.client(cc);w=self.workers[self.client_host];w.call('stream-open',key=pc.key,stream='expiring');w.call('stream-open',key=pb.key,stream='retained');assert w.call('stream-request',stream='expiring') and w.call('stream-request',stream='retained');self.cp.stop();expiry=dt.datetime.fromisoformat(c['expires_at'].replace('Z','+00:00'));remaining=max(0,(expiry-dt.datetime.now(dt.timezone.utc)).total_seconds());deadline=time.monotonic()+remaining
  while time.monotonic()<deadline:time.sleep(.25)
  lib.wait_for(lambda:self.denied(rc) and not w.call('stream-request',stream='expiring'),12);assert w.call('stream-request',stream='retained');latency=(dt.datetime.now(dt.timezone.utc)-expiry).total_seconds();write(self.root/'expiry.json',dict(expires_at=c['expires_at'],rejection_observed_at=lib.stamp(),latency_seconds=latency,bound_seconds=12));assert latency<=12;ra();rb();self.record('controller_offline_agent_expiry_rejects_new_and_existing_connections_other_identity_stream_live');self.cp=self.launch(self.controller,self.ccfg,'controller');lib.wait_for(lambda:self.http.api('GET','/readyz'));self.login();self.fetch(uc,(404,));self.cleanup_subscription(b);assert self.denied(rb);ra();self.security(ua);self.cleanup_subscription(a);self.cleanup_subscription(c)
 def egress_cases(self,node,profile,selected=None):
  w=self.workers[node['host']]
  for kind,port in [('http',18081),('socks5',18082)]:
   if selected and kind!=selected:continue
   target=dict(node_id=node['id'],type=kind,interface='',host='127.0.0.1',dial_ip='127.0.0.1',port=port)
   t=self.http.api('POST','/api/v1/egress-targets',dict(expected_revision=0,idempotency_key=lib.ident('target'),name='T03 '+kind,target=target))['pin']
   before=w.call('proxy-stats')[kind]['rejected']
   t=self.http.api('POST','/api/v1/egress-targets/'+t['id']+'/credentials',dict(expected_revision=t['revision'],idempotency_key=lib.ident('wrong'),pin=t,username='t03',password='intentional-invalid-'+self.aux_auth))['pin']
   self.passwords.append('intentional-invalid-'+self.aux_auth)
   self.http.api('POST','/api/v1/egress-targets/'+t['id']+'/validate',dict(expected_revision=t['revision'],idempotency_key=lib.ident('validate'),pin=t))
   lib.wait_for(lambda:w.call('proxy-stats')[kind]['rejected']>before,20)
   negative=self.http.api('GET','/api/v1/egress-targets/'+t['id']);assert negative['verification']!='verified';write(self.root/(kind+'-wrong-auth.json'),negative);self.record(kind+'_wrong_auth_explicit_remote_rejection_no_observation')
   body=dict(expected_revision=t['revision'],idempotency_key=lib.ident('auth'),pin=t,username='t03',password=self.aux_auth);t=self.http.api('POST','/api/v1/egress-targets/'+t['id']+'/credentials',body)['pin'];assert self.http.api('POST','/api/v1/egress-targets/'+t['id']+'/credentials',body)['pin']==t
   self.http.api('POST','/api/v1/egress-targets/'+t['id']+'/validate',dict(expected_revision=t['revision'],idempotency_key=lib.ident('validate'),pin=t))
   lib.wait_for(lambda:self.http.api('GET','/api/v1/egress-targets/'+t['id']).get('observation',{}).get('current'),30)
   policy=self.http.api('POST','/api/v1/egress-policies',dict(expected_revision=0,idempotency_key=lib.ident('policy'),name='T03 fixed '+kind,node_id=node['id'],dns=node['dns']['pin'],targets=[t],rules=[dict(destination='MATCH',target=t,no_resolve=False)]))['pin']
   route=self.route([node],policy=policy);v,url,cfg=self.subscribe(route,profile);client,request=self.client(cfg);self.record(kind+'_normal_mtls_observation_actual_end_to_end_tcp');w.call('proxy-stop',kind=kind);lib.wait_for(lambda:self.denied(request),12);self.record(kind+'_stopped_selected_exit_no_direct_fallback');client.stop();self.cleanup_subscription(v);self.archive(route)
 def cleanup_subscription(self,v):
  c=self.http.api('GET','/api/v1/subscriptions/'+v['id']);body=dict(expected_revision=c['revision'],idempotency_key=lib.ident('cleanup'));self.http.api('DELETE','/api/v1/subscriptions/'+v['id'],body);self.fetch(self.downloads[v['id']],(404,))
  def cleaned():
   value=self.http.api('GET','/api/v1/subscriptions/'+v['id']);return value if all(o['cleanup']=='confirmed' for o in value['operations']) else None
  confirmed=lib.wait_for(cleaned,90);self.http.api('DELETE','/api/v1/subscriptions/'+v['id'],body);replay=self.http.api('GET','/api/v1/subscriptions/'+v['id']);assert confirmed['operations']==replay['operations'],'duplicate delete changed cleanup operations';write(self.root/('cleanup-'+v['id']+'.json'),replay);self.record('subscription_real_cleanup_confirmed_duplicate_delete_stable')
 def archive(self,route):
  r=self.http.api('GET','/api/v1/routes/'+route['id']);self.http.api('DELETE','/api/v1/routes/'+route['id'],dict(expected_revision=r['pin']['revision'],idempotency_key=lib.ident('archive')));self.record('route_archived')

def main():
 parser=argparse.ArgumentParser();parser.add_argument('private_root');parser.add_argument('--controller',type=int,choices=[0,1],default=0);parser.add_argument('--phase',default='r0');parser.add_argument('--egress-kind',choices=['http','socks5']);parser.add_argument('--scenario',choices=['matrix','extended','egress','observe','lifecycle'],default='matrix');args=parser.parse_args();os.umask(0o077);local=pathlib.Path(args.private_root).resolve();mapping=json.loads((local/'mapping.json').read_text());folder=local/args.phase;folder.mkdir(mode=0o700);workers=[];run=None;result={'commit':COMMIT,'phase':args.phase,'started':lib.stamp(),'cases':[]}
 try:
  snapshot=folder/'source';snapshot.mkdir(mode=0o700)
  for source in pathlib.Path(__file__).parent.glob('*.py'):write(snapshot/source.name,source.read_bytes())
  result['test_source_sha256']={source.name:hashlib.sha256(source.read_bytes()).hexdigest() for source in pathlib.Path(__file__).parent.glob('*.py')}
  for i in range(2):workers.append(Worker(i,folder,mapping['remote']))
  run=Run(folder,workers,args.controller,args.phase);run.start_controller();run.materials();n=run.node(1-args.controller);profile=run.profile()
  for idx,protocol in enumerate(('ss2022','trojan','vless','hysteria2') if args.scenario=='matrix' else ()):
   start=lib.stamp();result['current_case']='entry-'+protocol;write(folder/'result.json',result);route=run.route([n],idx);v,url,cfg=run.subscribe(route,profile);client,request=run.client(cfg);client.stop();run.security(url);run.cleanup_subscription(v);run.archive(route);result['cases'].append(dict(id='entry-'+protocol,result='pass',controller='AB'[args.controller],ingress='AB'[n['host']],client='AB'[run.client_host],port=cfg['proxies'][0]['port'],started=start,ended=lib.stamp()));write(folder/'result.json',result)
  if args.scenario=='lifecycle':
   result['current_case']='subscription-lifecycle';route=run.route([n]);alternate=run.route([n],1);run.lifecycle(route,alternate,profile);result['cases'].append(dict(id='single-hop-lifecycle',result='pass'))
  if args.scenario=='egress':
   result['current_case']='egress';run.egress_cases(n,profile,args.egress_kind);result['cases'].append(dict(id='http-socks5',result='pass'))
  if args.scenario=='extended':
   result['current_case']='mixed-paths';n2=run.node(args.controller);n3=run.node(0,extra=True)
   for ns,indices in [([n2,n],[0,1]),([n3,n,n2],[1,2,0])]:
    route=run.route(ns,indices);v,url,cfg=run.subscribe(route,profile);client,_=run.client(cfg);w=workers[run.client_host];stream=lib.ident('path');w.call('stream-open',key=client.key,stream=stream);assert w.call('stream-request',stream=stream)
    for worker in workers:write(folder/('path-'+str(len(ns))+'-host'+str(worker.index)+'.json'),worker.call('snapshot'))
    w.call('stream-close',stream=stream);client.stop();run.cleanup_subscription(v);run.archive(route);result['cases'].append(dict(id='mixed-'+str(len(ns)),result='pass',order=[dict(node=x['id'],host='AB'[x['host']],state=str(x['path'].parent)) for x in ns]));write(folder/'result.json',result)
   result['current_case']='subscription-lifecycle';route=run.route([n3,n,n2],[0,1,0]);alternate=run.route([n3],1);run.lifecycle(route,alternate,profile);result['cases'].append(dict(id='lifecycle',result='pass'))
  result['result']='pass'
 except Exception as exc:
  write(folder/'error.txt',traceback.format_exc());result['result']='fail';result['error_type']=type(exc).__name__;print(args.phase,'failed; private diagnostics saved',flush=True)
 finally:
  if run:
   secrets_=run.urls+run.passwords+[u.rsplit('/',1)[-1] for u in run.urls]
   for w in workers:
    try:
     scan=w.call('scan',secrets=secrets_);write(folder/('log-scan-%d.json'%w.index),scan);result.setdefault('secret_log_findings',{})['AB'[w.index]]=scan
    except Exception:pass
  for w in workers:
   try:result.setdefault('cleanup',{})['AB'[w.index]]=w.call('finish');w.close()
   except Exception:write(folder/('cleanup-error-%d.txt'%w.index),traceback.format_exc());result.setdefault('cleanup',{})['AB'[w.index]]={'failed':True}
  result['ended']=lib.stamp();write(folder/'result.json',result);print(json.dumps({k:v for k,v in result.items() if k not in ('secret_log_findings',)}),flush=True)
if __name__=='__main__':main()
