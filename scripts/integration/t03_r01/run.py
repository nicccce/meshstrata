#!/usr/bin/env python3
"""Single bounded cross-host R01 case; ordinary fixed CLIs, no SQL or product edits."""
import argparse, base64, contextlib, datetime as dt, hashlib, json, os, pathlib
import importlib.util, re, select, subprocess, sys, time, traceback
from preflight import ROOT, COMMIT, SSH, IPS, ssh, write
sys.path.append(str(ROOT/'scripts/integration/t03'))
spec=importlib.util.spec_from_file_location('t03_base_run',ROOT/'scripts/integration/t03/run.py')
base=importlib.util.module_from_spec(spec);spec.loader.exec_module(base)
BaseRun,lib=base.Run,base.lib
sys.modules['run']=base
from transport import Worker as BaseWorker
from restart import immutable_task

class Worker(BaseWorker):
    def __init__(self, index, local, remote):
        self.index=index; self.local=local; self.remote=pathlib.Path(remote); self.seq=0
        source=(ROOT/'scripts/integration/t03/worker.py').read_text()
        prefix, loop=source.rsplit('try:\n for line in sys.stdin:',1)
        source=prefix+(pathlib.Path(__file__).with_name('remote.py')).read_text()+'\ntry:\n for line in sys.stdin:'+loop
        for data, name in [(source.encode(),'worker.py'), ((ROOT/'scripts/integration/i03c/local.py').read_bytes(),'helpers/i03c/local.py')]:
            p=ssh(index,'umask 077; mkdir -p '+str(self.remote/name).rsplit('/',1)[0]+' && cat > '+remote+'/'+name,data)
            assert p.returncode==0, 'worker upload'
        self.log=open(local/('worker-ssh-%d.log'%index),'ab',buffering=0)
        command='MESHSTRATA_PRODUCT_COMMIT='+COMMIT+' PYTHONDONTWRITEBYTECODE=1 python3 '+remote+'/worker.py '+remote
        self.p=subprocess.Popen(SSH+['root@'+IPS[index],command],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=self.log)

class Trace:
    def __init__(self, http, run): self.http=http; self.run=run; self.seen={}
    def __getattr__(self, key): return getattr(self.http,key)
    def __setattr__(self,key,value):
        if key=='csrf': self.http.csrf=value
        else: object.__setattr__(self,key,value)
    def raw(self, method, path, data=None, *args, **kwargs):
        started=lib.stamp(); result=self.http.raw(method,path,data,*args,**kwargs)
        import http.cookies
        for header in result[1].get_all('Set-Cookie',[]):
            cookies=http.cookies.SimpleCookie();cookies.load(header)
            self.run.passwords.extend(cookie.value for cookie in cookies.values() if cookie.value)

        if path.startswith('/api/v1/'):
            raw,_,status=result
            try: value=json.loads(raw) if raw else None
            except ValueError: value=None
            # Raw API DTOs stay private. No body/URL/config in console or public report.
            if not path.endswith('/download-link') and not path.endswith('/credentials'):
                key=(method,path,status); digest=hashlib.sha256(raw).hexdigest()
                if method!='GET' or self.seen.get(key)!=digest:
                    self.seen[key]=digest; self.run.trace.append(dict(started=started,ended=lib.stamp(),method=method,path=path,status=status,value=value))
                    write(self.run.root/'api-timeline.json',self.run.trace)
        return result
    def api(self, method, path, data=None, status=(200,202), **kwargs):
        raw,_,_=self.raw(method,path,data,status,**kwargs); return json.loads(raw) if raw else None

class Run(BaseRun):
    def __init__(self,*args):
        super().__init__(*args); self.trace=[]; self.checks=[]; self.targets=[]; self.subscriptions={}; self.routes={}; self.phase_started=time.monotonic()
    def remote_file(self,i,path,data):
        if isinstance(data,dict) and 'log_level' in data: data=dict(data,log_level='info')
        return super().remote_file(i,path,data)
    def login(self):
        super().login()
        self.passwords.append(self.http.csrf)
        self.passwords.extend(cookie.value for cookie in self.http.jar if cookie.value)
        if not isinstance(self.http,Trace): self.http=Trace(self.http,self)
    def check(self,name,fn,bound=None):
        item=dict(id=name,started=lib.stamp(),result='running'); self.checks.append(item); write(self.root/'checks.json',self.checks)
        start=time.monotonic()
        try:
            out=fn(); item['result']='pass'; return out
        except Exception:
            item['result']='fail'; raise
        finally:
            item.update(ended=lib.stamp(),elapsed_seconds=round(time.monotonic()-start,3))
            if bound: item['bound_seconds']=bound
            write(self.root/'checks.json',self.checks)
    def observe_node(self,key,hi):
        return self.check('cold-current-inventory-'+str(hi),lambda:super(Run,self).observe_node(key,hi),55)
    def subscribe(self,route,profile,expiry=1200):
        out=self.check('prepared-confirmed-subscription',lambda:super(Run,self).subscribe(route,profile,expiry),90)
        self.subscriptions[out[0]['id']]=out[0]; return out
    def route(self,*args,**kwargs):
        out=super().route(*args,**kwargs); self.routes[out['id']]=out; return out
    def cleanup_subscription(self,v):
        return self.check('delete-cleanup-'+v['id'],lambda:super(Run,self).cleanup_subscription(v),90)
    def pins_proof(self,node,pins,label):
        proof=self.workers[node['host']].call('runtime-proof',state=str(node['path'].parent/'s'),pins=[p['id'] for p in pins])
        write(self.root/(label+'.json'),proof);return proof
    def capture(self):
        # One snapshot only on failure. Never repeat a failed dependent branch.
        task_ids=set()
        for event in list(self.trace):
            value=event.get('value')
            if isinstance(value,dict):
                if event['path']=='/api/v1/subscriptions' and value.get('id'): self.subscriptions.setdefault(value['id'],value)
                if value.get('task_id'): task_ids.add(value['task_id'])
                for op in value.get('operations',[]):
                    for field in ('task_id','cleanup_task_id'):
                        if op.get(field): task_ids.add(op[field])
        for key in task_ids:
            with contextlib.suppress(Exception):write(self.root/('task-final-'+key+'.json'),self.http.api('GET','/api/v1/tasks/'+key))
        for node in self.agents:
            with contextlib.suppress(Exception):write(self.root/('preparation-final-'+node['id']+'.json'),self.http.api('GET','/api/v1/nodes/'+node['id']+'/preparation'))
        for key in self.targets:
            with contextlib.suppress(Exception):write(self.root/('target-final-'+key+'.json'),self.http.api('GET','/api/v1/egress-targets/'+key))
        for key in self.subscriptions:
            with contextlib.suppress(Exception):write(self.root/('subscription-final-'+key+'.json'),self.http.api('GET','/api/v1/subscriptions/'+key))
        for worker in self.workers:
            with contextlib.suppress(Exception):write(self.root/('snapshot-final-'+str(worker.index)+'.json'),worker.call('snapshot'))
    def target(self,node,kind,negative=False):
        worker=self.workers[node['host']]
        spec=dict(node_id=node['id'],type=kind,interface='',host='127.0.0.1',dial_ip='127.0.0.1',port=18081 if kind=='http' else 18082)
        pin=self.http.api('POST','/api/v1/egress-targets',dict(expected_revision=0,idempotency_key=lib.ident('target'),name='R01 '+kind,target=spec))['pin'];self.targets.append(pin['id'])
        def auth(password):
            return self.http.api('POST','/api/v1/egress-targets/'+pin['id']+'/credentials',dict(expected_revision=pin['revision'],idempotency_key=lib.ident('auth'),pin=pin,username='t03',password=password))['pin']
        if negative:
            password='intentional-invalid-'+self.aux_auth;self.passwords.append(password);pin=auth(password)
            rejected=worker.call('proxy-stats')[kind]['rejected']; deadline=self.validate_target(pin)
            lib.wait_for(lambda:worker.call('proxy-stats')[kind]['rejected']>rejected,max(0,min(10,deadline-time.monotonic())))
            detail=self.http.api('GET','/api/v1/egress-targets/'+pin['id']);assert detail['verification']!='verified' and not detail.get('observation',{}).get('current')
            self.record(kind+'_wrong_auth_rejected_no_current')
        pin=auth(self.aux_auth);before=worker.call('proxy-stats')[kind];start=time.monotonic();deadline=self.validate_target(pin)
        def current():
            detail=self.http.api('GET','/api/v1/egress-targets/'+pin['id']);return detail if detail.get('observation',{}).get('current') else None
        detail=lib.wait_for(current,max(0,deadline-time.monotonic()));elapsed=time.monotonic()-start;assert elapsed<=30
        after=worker.call('proxy-stats')[kind];assert after['accepted']>before['accepted'] and after['rejected']>before['rejected']
        assert detail['pin']==pin and detail['observation']['target']==pin and detail['observation']['business_udp']=='not_checked'
        write(self.root/(kind+'-current.json'),dict(elapsed_seconds=elapsed,bound_seconds=30,detail=detail,before=before,after=after,canary=worker.call('canary',echo=self.echo)))
        self.record(kind+'_exact_pin_current_independent_echo_negative_auth'); return pin
    def policy(self,node,target):
        return self.http.api('POST','/api/v1/egress-policies',dict(expected_revision=0,idempotency_key=lib.ident('policy'),name='R01 fixed',node_id=node['id'],dns=node['dns']['pin'],targets=[target],rules=[dict(destination='MATCH',target=target,no_resolve=False)]))['pin']
    def egress(self,node,profile,kind,negative=False,retain=False):
        pin=self.check(kind+'-observation',lambda:self.target(node,kind,negative),30 if not negative else None)
        policy=self.policy(node,pin);route=self.route([node],policy=policy);v,_,cfg=self.subscribe(route,profile);client,request=self.client(cfg);request()
        self.pins_proof(node,[policy],kind+'-active-runtime');self.record(kind+'_actual_forward')
        if not retain:
            self.workers[node['host']].call('proxy-stop',kind=kind);lib.wait_for(lambda:self.denied(request),12)
            assert self.workers[node['host']].call('canary',echo=self.echo)['independent_http'];self.record(kind+'_stopped_exit_refused_canary_alive')
            client.stop();self.cleanup_subscription(v);self.archive(route)
            lib.wait_for(lambda:self.workers[node['host']].call('check-free',ports=[cfg['proxies'][0]['port']]),90)
        return dict(pin=pin,policy=policy,route=route,subscription=v,config=cfg,client=client,request=request)

def observe(run,node,profile):
    # A real bad credential exercises failure isolation; no fake observation/SQL.
    run.check('failure-and-new-pin-recovery',lambda:run.target(node,'socks5',True))
    before=lib.wait_for(lambda:run.current_observation(node['id']),55)['observation']
    def refreshed():
        value=run.current_observation(node['id']);return value and value['observation']!=before
    lib.wait_for(refreshed,55)
    run.record('background_inventory_refresh_after_admin_validate_failure')
    node['process'].stop();node['process']=run.launch(run.agent,node['path'],'agent-reconnect')
    lib.wait_for(lambda:run.current_observation(node['id']) and run.http.api('GET','/api/v1/nodes/'+node['id'])['control']=='online',55)
    run.record('normal_agent_reconnect_current_inventory_recovered')

def sequence(run,node,profile):
    first=run.egress(node,profile,'http')
    before=run.http.api('GET','/api/v1/routes/'+first['route']['id'])
    second=run.egress(node,profile,'socks5',True,retain=True)
    proof=run.pins_proof(node,[first['pin'],second['pin']],'sequence-candidate-pins')
    assert len(proof['active'])==1
    names=proof['active'][0]['live_egress_names']
    assert any(second['pin']['id'] in name for name in names) and not any(first['pin']['id'] in name for name in names)
    run.http.api('GET','/api/v1/egress-policies/'+first['policy']['id'])
    after=run.http.api('GET','/api/v1/routes/'+first['route']['id']);assert before==after
    write(run.root/'old-route-history-unchanged.json',dict(before=before,after=after))
    run.record('old_archived_route_history_unchanged_new_socks_task_confirmed')
    run.workers[node['host']].call('proxy-stop',kind='socks5');lib.wait_for(lambda:run.denied(second['request']),12)
    assert run.workers[node['host']].call('canary',echo=run.echo)['independent_http'];run.record('socks5_stopped_exit_refused_canary_alive')
    second['client'].stop();run.cleanup_subscription(second['subscription']);run.archive(second['route'])

def restart(run,node,profile):
    route=run.route([node]);v,_,cfg=run.subscribe(route,profile);client,request=run.client(cfg)
    keys=[o['task_id'] for o in v['operations'] if o.get('task_id')];assert keys and all(o['state']=='confirmed' for o in v['operations'])
    before={k:immutable_task(run.http.api('GET','/api/v1/tasks/'+k)) for k in keys};write(run.root/'immutable-task-before.json',before)
    worker=run.workers[run.client_host];worker.call('stream-open',key=client.key,stream='retained');assert worker.call('stream-request',stream='retained')
    run.cp.stop();run.cp=run.launch(run.controller,run.ccfg,'controller-restart');lib.wait_for(lambda:run.http.api('GET','/readyz'));run.login()
    node['process'].stop();node['process']=run.launch(run.agent,node['path'],'agent-restart');lib.wait_for(lambda:run.http.api('GET','/api/v1/nodes/'+node['id'])['control']=='online',40)
    request();assert worker.call('stream-request',stream='retained')
    after={k:immutable_task(run.http.api('GET','/api/v1/tasks/'+k)) for k in keys};assert before==after;write(run.root/'immutable-task-after.json',after)
    run.record('exact_original_task_artifact_confirmed_retained_and_new_tcp_after_both_restarts')
    worker.call('stream-close',stream='retained');client.stop();run.cleanup_subscription(v);run.archive(route)
    lib.wait_for(lambda:run.workers[node['host']].call('check-free',ports=[cfg['proxies'][0]['port']]),90)
    run.record('cleanup_port_rebind_confirmed')

def wrongpin(run,cfg,request):
    bad=json.loads(json.dumps(cfg));proxy=bad['proxies'][0]
    if 'fingerprint' in proxy:proxy['fingerprint']='0'*64
    elif 'reality-opts' in proxy:proxy['reality-opts']['public-key']=base64.urlsafe_b64encode(os.urandom(32)).decode().rstrip('=')
    else:
        # Mihomo TLS leaf pin uses fingerprint for the generated TLS protocols.
        proxy['fingerprint']='0'*64
    worker=run.workers[run.client_host];key=lib.ident('wrongpin');node=run.current_ingress
    result=worker.call('client',key=key,config=bad,hosts={'test.local':IPS[node['host']]},ca=(run.root/'controller-keys/web-ca.pem').read_text())
    try:assert not worker.call('request',key=key),'wrong business pin forwarded'
    finally:worker.call('stop',pid=result['pid'])
    request();run.record('wrong_business_pin_rejected_correct_client_retained')

def history(run,node,profile):
    # Original all: keep the four published routes (delete only subscriptions),
    # keep two/three-hop subscriptions and both proxy-exit subscriptions alive.
    other=run.node(run.ci);extra=run.node(0,extra=True)
    nodes=[node,other,extra] if node['host']==1 else [node,other,extra]
    for idx,protocol in enumerate(('ss2022','trojan','vless','hysteria2')):
        def entry():
            route=run.route([node],idx);v,_,cfg=run.subscribe(route,profile);client,request=run.client(cfg)
            if idx in (1,2,3):wrongpin(run,cfg,request)
            client.stop();run.cleanup_subscription(v);run.record('history_entry_'+protocol);return route
        alternate=run.check('history-entry-'+protocol,entry)
    for indices in ([0,1],[1,2,3]):
        route=run.route(nodes[:len(indices)],indices);v,_,cfg=run.subscribe(route,profile);client,request=run.client(cfg)
        worker=run.workers[run.client_host];stream='path-'+str(len(indices));worker.call('stream-open',key=client.key,stream=stream);assert worker.call('stream-request',stream=stream)
        for w in run.workers:write(run.root/('path-'+str(len(indices))+'-'+str(w.index)+'.json'),w.call('snapshot'))
        worker.call('stream-close',stream=stream);run.record('history_mixed_'+str(len(indices))+'_actual_tcp')
    run.egress(nodes[-1],profile,'http',retain=True);run.egress(nodes[-1],profile,'socks5',retain=True)
    # Remote lifecycle additionally tests provider refresh, retained stream and
    # Agent restart, while preserving the original all ordering to offline expiry.
    run.check('history-lifecycle-through-offline-expiry-delete',lambda:run.lifecycle(route,alternate,profile))
    # All residual ordinary subscriptions are cleaned, then routes archived.
    for key,v in list(run.subscriptions.items()):
        current=run.http.api('GET','/api/v1/subscriptions/'+key)
        if current.get('deleted_at'):continue
        if all(o['cleanup']=='confirmed' for o in current['operations']):continue
        run.cleanup_subscription(v)
    for route in reversed(list(run.routes.values())):run.archive(route)
    for n in nodes:lib.wait_for(lambda:run.workers[n['host']].call('check-free',ports=[29443,29444,29445] if n is extra else [28443,28444,28445,28446]),90)
    run.record('history_all_last_references_real_port_release')

def sharing(run,node,profile):
    middle=node if node['host']==1 else run.node(1)
    terminal=node if node['host']==0 else run.node(0)
    entry=run.node(0,extra=True);nodes=[entry,middle,terminal]
    def proof(label):
        snapshots={w.index:w.call('snapshot') for w in run.workers}
        for i,value in snapshots.items():write(run.root/(label+'-'+str(i)+'.json'),value)
        ports=[]
        for n in [middle,terminal]:
            prefix=str(n['path'].parent/'s')+'/'
            processes=[p for p in snapshots[n['host']]['managed_processes'] if any(a.startswith(prefix) for a in p['argv'])]
            assert len(processes)==1
            ports.append(sorted({int(x['local'].split(':')[1],16) for x in processes[0]['sockets'] if x['state']=='0A' and x['transport'].startswith('tcp')}))
        return ports
    route=run.route(nodes,[0,1,0]);a,_,ca=run.subscribe(route,profile);pa,ra=run.client(ca);b,_,cb=run.subscribe(route,profile);pb,rb=run.client(cb)
    assert ca['proxies'][0]['password']!=cb['proxies'][0]['password'] and ca['proxies'][0]['port']!=cb['proxies'][0]['port']
    worker=run.workers[run.client_host]
    for name,p in [('a',pa),('b',pb)]:worker.call('stream-open',key=p.key,stream=name);assert worker.call('stream-request',stream=name)
    old=proof('shared');assert all(len(p)==1 for p in old);run.record('same_suffix_shared_one_internal_listener')
    policy=run.route_policies[route['id']];view=run.http.api('GET','/api/v1/egress-policies/'+policy['id'])
    new=run.http.api('PATCH','/api/v1/egress-policies/'+policy['id'],dict(expected_revision=policy['revision'],idempotency_key=lib.ident('revision'),name='R01 next policy',node_id=view['node_id'],dns=view['dns'],targets=view['targets'],rules=view['rules']))['pin']
    assert new['revision']>policy['revision'];alternate=run.route(nodes,[0,1,0],policy=new);c,_,cc=run.subscribe(alternate,profile);pc,rc=run.client(cc)
    forked=proof('forked');assert all(set(a)<set(b) and len(b)==2 for a,b in zip(old,forked))
    run.pins_proof(terminal,[policy,new],'active-two-policy-pins');run.record('distinct_exact_policy_pin_distinct_internal_listeners')
    pc.stop();run.cleanup_subscription(c);run.archive(alternate)
    for n,old_ports,new_ports in zip([middle,terminal],old,forked):lib.wait_for(lambda:run.workers[n['host']].call('check-free',ports=list(set(new_ports)-set(old_ports))),90)
    assert worker.call('stream-request',stream='a') and worker.call('stream-request',stream='b');run.record('new_pin_cleanup_old_pin_streams_live')
    run.cleanup_subscription(a);assert not worker.call('stream-request',stream='a') and worker.call('stream-request',stream='b');assert proof('one-reference')==old;pa.stop()
    run.record('one_reference_removed_other_identity_stream_live')
    run.cleanup_subscription(b);assert not worker.call('stream-request',stream='b');pb.stop();run.archive(route)
    for n,ports in zip([middle,terminal],old):lib.wait_for(lambda:run.workers[n['host']].call('check-free',ports=ports),90)
    run.record('last_reference_cleanup_real_port_release')
    write(run.root/'sharing-summary.json',dict(old_policy=policy,new_policy=new,shared_ports=old,forked_ports=forked))

def main():
    parser=argparse.ArgumentParser();parser.add_argument('private_root');parser.add_argument('--phase',required=True);parser.add_argument('--controller',required=True,type=int,choices=[0,1]);parser.add_argument('--scenario',required=True,choices=['observe','correct-first','sequence','restart','history','sharing'])
    args=parser.parse_args();assert re.fullmatch(r'r[0-9]+-[a-z-]+',args.phase);os.umask(0o077)
    local=pathlib.Path(args.private_root).resolve();mapping=json.loads((local/'mapping.json').read_text());assert mapping['commit']==COMMIT
    folder=local/args.phase;folder.mkdir(mode=0o700);workers=[];run=None
    sources=list(pathlib.Path(__file__).parent.glob('*.py'))+list((ROOT/'scripts/integration/t03').glob('*.py'))+[ROOT/'scripts/integration/i03c/local.py']
    result=dict(product_commit=COMMIT,scenario=args.scenario,controller='AB'[args.controller],phase=args.phase,started=lib.stamp(),tool_sha256={str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sources})
    write(folder/'result.json',result);source=folder/'source';source.mkdir(mode=0o700)
    for p in sources:write(source/(p.parent.name+'-'+p.name),p.read_bytes())
    try:
        for i in range(2):workers.append(Worker(i,folder,mapping['remote']))
        run=Run(folder,workers,args.controller,args.phase);run.start_controller();run.materials();node=run.node(1-args.controller);profile=run.profile()
        if args.scenario=='correct-first':run.egress(node,profile,'socks5')
        else:globals()[args.scenario](run,node,profile)
        result['result']='pass'
    except Exception as exc:
        write(folder/'error.txt',traceback.format_exc());result.update(result='fail',error_type=type(exc).__name__);print(args.phase,'failed; private diagnostics saved',flush=True)
    finally:
        if run:
            run.capture();result['checks']=run.checks
            generated=run.urls+run.passwords+[u.rsplit('/',1)[-1] for u in run.urls]
            for w in workers:
                with contextlib.suppress(Exception):result.setdefault('secret_log_findings',{})['AB'[w.index]]=w.call('scan',secrets=generated)
        for w in workers:
            try:result.setdefault('cleanup',{})['AB'[w.index]]=w.call('finish');w.close()
            except Exception:write(folder/('cleanup-error-'+str(w.index)+'.txt'),traceback.format_exc());result.setdefault('cleanup',{})['AB'[w.index]]=dict(failed=True)
        result['ended']=lib.stamp();write(folder/'result.json',result)
        print(json.dumps({k:v for k,v in result.items() if k not in ('tool_sha256','secret_log_findings','checks')}),flush=True)
        if result.get('result')!='pass':sys.exit(1)
if __name__=='__main__':main()
