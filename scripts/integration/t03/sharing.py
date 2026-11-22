#!/usr/bin/env python3
"""Bounded same-suffix reuse, policy-revision isolation and last-reference cleanup."""
import hashlib,json,os,pathlib,sys,traceback
from preflight import COMMIT,write
from transport import Worker
from run import Run,lib

def main():
 os.umask(0o077);local=pathlib.Path(sys.argv[1]).resolve();phase=sys.argv[2];folder=local/phase;folder.mkdir(mode=0o700);mapping=json.loads((local/'mapping.json').read_text());workers=[];run=None;result=dict(commit=COMMIT,phase=phase,started=lib.stamp(),cases=[])
 def proof(label,nodes):
  snapshots={w.index:w.call('snapshot') for w in workers}
  for i,value in snapshots.items():write(folder/(label+'-host'+str(i)+'.json'),value)
  ports=[]
  for n in nodes:
   prefix=str(n['path'].parent/'s')+'/'
   processes=[p for p in snapshots[n['host']]['managed_processes'] if any(a.startswith(prefix) for a in p['argv'])];assert len(processes)==1,'node process must be unique'
   ports.append(sorted({int(s['local'].split(':')[1],16) for s in processes[0]['sockets'] if s['state']=='0A' and s['transport'].startswith('tcp')}))
  return ports
 try:
  source=folder/'source';source.mkdir(mode=0o700)
  for p in pathlib.Path(__file__).parent.glob('*.py'):write(source/p.name,p.read_bytes())
  result['test_source_sha256']={p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in pathlib.Path(__file__).parent.glob('*.py')}
  workers=[Worker(i,folder,mapping['remote']) for i in range(2)];run=Run(folder,workers,0,phase);run.start_controller();run.materials();middle=run.node(1);terminal=run.node(0);entry=run.node(0,extra=True);nodes=[entry,middle,terminal];profile=run.profile();route=run.route(nodes,[0,1,0]);a,ua,ca=run.subscribe(route,profile);pa,ra=run.client(ca);b,ub,cb=run.subscribe(route,profile);pb,rb=run.client(cb)
  assert ca['proxies'][0]['password']!=cb['proxies'][0]['password'] and ca['proxies'][0]['port']!=cb['proxies'][0]['port'] and a['entries'][0]['identity_id']!=b['entries'][0]['identity_id']
  w=workers[0]
  for name,p in [('a',pa),('b',pb)]:w.call('stream-open',key=p.key,stream=name);assert w.call('stream-request',stream=name)
  old=proof('shared',[middle,terminal]);assert all(len(p)==1 for p in old);run.record('same_suffix_two_subscriptions_one_internal_listener_per_downstream')
  policy=run.route_policies[route['id']];view=run.http.api('GET','/api/v1/egress-policies/'+policy['id']);new=run.http.api('PATCH','/api/v1/egress-policies/'+policy['id'],dict(expected_revision=policy['revision'],idempotency_key=lib.ident('policy-revision'),name='T03 next policy revision',node_id=view['node_id'],dns=view['dns'],targets=view['targets'],rules=view['rules']))['pin'];assert new['revision']>policy['revision'];alternate=run.route(nodes,[0,1,0],policy=new);c,uc,cc=run.subscribe(alternate,profile);pc,rc=run.client(cc);w.call('stream-open',key=pc.key,stream='c');assert w.call('stream-request',stream='c');forked=proof('forked',[middle,terminal]);assert all(set(x)<set(y) and len(y)==2 for x,y in zip(old,forked));run.record('different_terminal_policy_revision_has_distinct_internal_listener_each')
  w.call('stream-close',stream='c');pc.stop();run.cleanup_subscription(c);run.archive(alternate)
  for node,old_ports,new_ports in zip([middle,terminal],old,forked):
   released=list(set(new_ports)-set(old_ports));lib.wait_for(lambda:workers[node['host']].call('check-free',ports=released),90)
  assert w.call('stream-request',stream='a') and w.call('stream-request',stream='b');run.record('policy_fork_cleanup_frees_only_its_ports_shared_streams_live')
  run.cleanup_subscription(a);assert not w.call('stream-request',stream='a');assert w.call('stream-request',stream='b');after_first=proof('one-reference',[middle,terminal]);assert after_first==old;pa.stop();run.record('remove_one_subscription_keeps_shared_internal_listeners_other_stream_live')
  run.cleanup_subscription(b);assert not w.call('stream-request',stream='b');pb.stop();run.archive(route)
  for node,ports in zip([middle,terminal],old):lib.wait_for(lambda:workers[node['host']].call('check-free',ports=ports),90)
  run.record('last_reference_archive_real_cleanup_ports_free')
  result.update(result='pass',cases=[dict(id='shared-suffix-policy-revision-cleanup',result='pass',order=[dict(node=n['id'],host='AB'[n['host']]) for n in nodes],shared_ports=old,forked_ports=forked,old_policy=policy,new_policy=new)])
 except Exception as e:write(folder/'error.txt',traceback.format_exc());result.update(result='fail',error_type=type(e).__name__);print(phase,'failed; private diagnostics saved',flush=True)
 finally:
  if run:
   generated=run.urls+run.passwords+[u.rsplit('/',1)[-1] for u in run.urls]
   for w in workers:
    try:result.setdefault('secret_log_findings',{})['AB'[w.index]]=w.call('scan',secrets=generated)
    except Exception:result.setdefault('secret_log_findings',{})['AB'[w.index]]={'scan_failed':True}
  for w in workers:
   try:result.setdefault('cleanup',{})['AB'[w.index]]=w.call('finish');w.close()
   except Exception:result.setdefault('cleanup',{})['AB'[w.index]]={'failed':True};write(folder/('cleanup-error-'+str(w.index)+'.txt'),traceback.format_exc())
  result['ended']=lib.stamp();write(folder/'result.json',result);print(json.dumps({k:v for k,v in result.items() if k not in ['test_source_sha256','secret_log_findings']}),flush=True)
if __name__=='__main__':main()
