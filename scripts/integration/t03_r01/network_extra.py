#!/usr/bin/env python3
"""Bounded extra Ubuntu pool diagnosis, only after all active cases have stopped."""
import importlib.util,json,os,pathlib,sys
from preflight import ROOT,IPS,write
spec=importlib.util.spec_from_file_location('r01_driver',pathlib.Path(__file__).with_name('run.py'));module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
local=pathlib.Path(sys.argv[1]);mapping=json.loads((local/'mapping.json').read_text());folder=local/'extra-pool-network';folder.mkdir(mode=0o700);workers=[];rows=[]
ports=[['tcp',29443],['tcp',29444],['tcp',29445]]
try:
 for i in range(2):workers.append(module.Worker(i,folder,mapping['remote']))
 workers[0].call('probe-start',ports=ports,allowed=IPS)
 for i,host,label in [(0,'127.0.0.1','Ubuntu-loopback'),(0,IPS[0],'Ubuntu-public'),(1,IPS[0],'Alpine-to-Ubuntu')]:
  value=workers[i].call('probe',host=host,ports=ports);rows.append(dict(path=label,probes=value));print(label,[(x['port'],x['pass_'],x['error']) for x in value],flush=True)
finally:
 write(local/'extra-pool-network.json',rows)
 for w in workers:
  print('extra network cleanup','AB'[w.index],w.call('finish'));w.close()
