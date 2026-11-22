#!/usr/bin/env python3
"""One fresh correct-credentials-first SOCKS5 control; preserves negative-first failures."""
import hashlib,json,os,pathlib,sys,time,traceback
from preflight import COMMIT,write
from transport import Worker
from run import Run,lib

def main():
 os.umask(0o077);local=pathlib.Path(sys.argv[1]).resolve();phase=sys.argv[2];folder=local/phase;folder.mkdir(mode=0o700);mapping=json.loads((local/'mapping.json').read_text());workers=[];run=None;target=None;result=dict(commit=COMMIT,phase=phase,started=lib.stamp(),cases=[])
 try:
  snapshot=folder/'source';snapshot.mkdir(mode=0o700)
  for p in pathlib.Path(__file__).parent.glob('*.py'):write(snapshot/p.name,p.read_bytes())
  result['test_source_sha256']={p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in pathlib.Path(__file__).parent.glob('*.py')}
  workers=[Worker(i,folder,mapping['remote']) for i in range(2)];run=Run(folder,workers,0,phase);run.start_controller();run.materials();node=run.node(1);profile=run.profile();spec=dict(node_id=node['id'],type='socks5',interface='',host='127.0.0.1',dial_ip='127.0.0.1',port=18082);target=run.http.api('POST','/api/v1/egress-targets',dict(expected_revision=0,idempotency_key=lib.ident('target'),name='T03 correct-first SOCKS5',target=spec))['pin'];target=run.http.api('POST','/api/v1/egress-targets/'+target['id']+'/credentials',dict(expected_revision=target['revision'],idempotency_key=lib.ident('correct'),pin=target,username='t03',password=run.aux_auth))['pin'];deadline=run.validate_target(target);lib.wait_for(lambda:run.http.api('GET','/api/v1/egress-targets/'+target['id']).get('observation',{}).get('current'),max(0,deadline-time.monotonic()));run.record('correct_first_socks5_normal_mtls_observation')
  policy=run.http.api('POST','/api/v1/egress-policies',dict(expected_revision=0,idempotency_key=lib.ident('policy'),name='T03 SOCKS5',node_id=node['id'],dns=node['dns']['pin'],targets=[target],rules=[dict(destination='MATCH',target=target,no_resolve=False)]))['pin'];route=run.route([node],policy=policy);v,url,cfg=run.subscribe(route,profile);client,request=run.client(cfg);run.record('correct_first_socks5_actual_end_to_end_tcp')
  keys=[o['task_id'] for o in v['operations'] if o.get('task_id')];before={k:run.http.api('GET','/api/v1/tasks/'+k) for k in keys};run.cp.stop();run.cp=run.launch(run.controller,run.ccfg,'controller');lib.wait_for(lambda:run.http.api('GET','/readyz'));run.login();node['process'].stop();node['process']=run.launch(run.agent,node['path'],'agent-restart');lib.wait_for(lambda:run.http.api('GET','/api/v1/nodes/'+node['id'])['control']=='online',40);request();after={k:run.http.api('GET','/api/v1/tasks/'+k) for k in keys};assert before==after,'normal restart changed original task DTO';write(folder/'immutable-task-before.json',before);write(folder/'immutable-task-after.json',after);run.record('normal_controller_agent_restart_original_task_id_digest_results_unchanged')
  workers[1].call('proxy-stop',kind='socks5');lib.wait_for(lambda:run.denied(request),12);run.record('correct_first_socks5_stopped_exit_no_direct_fallback');client.stop();run.cleanup_subscription(v);run.archive(route);result.update(result='pass',cases=[dict(id='correct-first-socks5',result='pass')])
 except Exception as e:write(folder/'error.txt',traceback.format_exc());result.update(result='fail',error_type=type(e).__name__);print(phase,'failed; private diagnostics saved',flush=True)
 finally:
  if run:
   try:
    if target:write(folder/'target-final-dto.json',run.http.api('GET','/api/v1/egress-targets/'+target['id']))
    for w in workers:write(folder/('proxy-stats-'+str(w.index)+'.json'),w.call('proxy-stats'));write(folder/('snapshot-final-'+str(w.index)+'.json'),w.call('snapshot'))
   except Exception:write(folder/'snapshot-error.txt',traceback.format_exc())
   generated=run.urls+run.passwords+[u.rsplit('/',1)[-1] for u in run.urls]
   for w in workers:
    try:result.setdefault('secret_log_findings',{})['AB'[w.index]]=w.call('scan',secrets=generated)
    except Exception:result.setdefault('secret_log_findings',{})['AB'[w.index]]={'scan_failed':True}
  for w in workers:
   try:result.setdefault('cleanup',{})['AB'[w.index]]=w.call('finish');w.close()
   except Exception:result.setdefault('cleanup',{})['AB'[w.index]]={'failed':True};write(folder/('cleanup-error-'+str(w.index)+'.txt'),traceback.format_exc())
  result['ended']=lib.stamp();write(folder/'result.json',result);print(json.dumps({k:v for k,v in result.items() if k not in ['test_source_sha256','secret_log_findings']}),flush=True)
if __name__=='__main__':main()
