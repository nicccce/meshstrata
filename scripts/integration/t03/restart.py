#!/usr/bin/env python3
"""One normal Node Default task/digest persistence check with real authorized traffic."""
import hashlib,json,os,pathlib,sys,traceback
from preflight import COMMIT,write
from transport import Worker
from run import Run,lib

def immutable_task(value):
 # Recovery may refresh confirmed_at. Compare artifact identity and confirmed
 # facts, and still require a real confirmation on both sides.
 assert value['state']=='succeeded'
 nodes=[]
 for node in value['nodes']:
  assert node['applied'] and node['verification']=='verified' and node['confirmed_at']
  nodes.append({k:node[k] for k in ('node_id','target_revision','target_digest','applied','verification','rollback','cleanup','issue') if k in node})
 return {k:value[k] for k in ('id','kind','subject','state','deadline','created_at')} | {'nodes':sorted(nodes,key=lambda node:node['node_id'])}

def main():
 os.umask(0o077);local=pathlib.Path(sys.argv[1]).resolve();phase=sys.argv[2];folder=local/phase;folder.mkdir(mode=0o700);mapping=json.loads((local/'mapping.json').read_text());workers=[];run=None;result=dict(commit=COMMIT,phase=phase,started=lib.stamp(),cases=[])
 try:
  snapshot=folder/'source';snapshot.mkdir(mode=0o700)
  for p in pathlib.Path(__file__).parent.glob('*.py'):write(snapshot/p.name,p.read_bytes())
  result['test_source_sha256']={p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in pathlib.Path(__file__).parent.glob('*.py')}
  workers=[Worker(i,folder,mapping['remote']) for i in range(2)];run=Run(folder,workers,0,phase);run.start_controller();run.materials();node=run.node(1);profile=run.profile();route=run.route([node]);v,url,cfg=run.subscribe(route,profile);client,request=run.client(cfg);keys=[o['task_id'] for o in v['operations'] if o.get('task_id')];assert keys;before={k:immutable_task(run.http.api('GET','/api/v1/tasks/'+k)) for k in keys};write(folder/'immutable-task-before.json',before);w=workers[0];w.call('stream-open',key=client.key,stream='retained');assert w.call('stream-request',stream='retained');run.cp.stop();run.cp=run.launch(run.controller,run.ccfg,'controller');lib.wait_for(lambda:run.http.api('GET','/readyz'));run.login();node['process'].stop();node['process']=run.launch(run.agent,node['path'],'agent-restart');lib.wait_for(lambda:run.http.api('GET','/api/v1/nodes/'+node['id'])['control']=='online',40);request();assert w.call('stream-request',stream='retained');after={k:immutable_task(run.http.api('GET','/api/v1/tasks/'+k)) for k in keys};write(folder/'immutable-task-after.json',after);assert before==after,'normal restart changed original task artifact or confirmed facts';run.record('normal_controller_agent_restart_original_task_id_digest_results_unchanged_stream_live');w.call('stream-close',stream='retained');client.stop();run.cleanup_subscription(v);run.archive(route);lib.wait_for(lambda:workers[1].call('check-free',ports=[cfg['proxies'][0]['port']]),90);run.record('normal_cleanup_repeated_request_port_free');result.update(result='pass',cases=[dict(id='normal-restart-original-task-unchanged',result='pass',task_ids=keys)])
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
