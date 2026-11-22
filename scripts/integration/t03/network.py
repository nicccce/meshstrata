#!/usr/bin/env python3
import json,pathlib,socket,sys,traceback
from preflight import *
from transport import Worker
os.umask(0o077)
local=pathlib.Path(sys.argv[1]);mapping=json.loads((local/'mapping.json').read_text());workers=[]
ports=[['tcp',8443],['tcp',9443],['tcp',28443],['tcp',28444],['tcp',28445],['udp',28446],['tcp',38443]]
results=[]
try:
 for i in range(2):
  w=Worker(i,local,mapping['remote']);workers.append(w);w.call('probe-start',ports=ports,allowed=IPS)
 for i,w in enumerate(workers):
  for destination,label in [('127.0.0.1','loopback'),(IPS[1-i],'peer')]:
   value=w.call('probe',host=destination,ports=ports);results.append(dict(source='AB'[i],destination=label,probes=value));print('AB'[i],label,[(v['transport'],v['port'],v['pass_']) for v in value],flush=True)
 for i in range(2):
  rows=[]
  for transport,port in ports:
   s=socket.socket(socket.AF_INET,socket.SOCK_STREAM if transport=='tcp' else socket.SOCK_DGRAM);s.settimeout(2);ok=False;error=None
   try:s.connect((IPS[i],port));s.sendall(b'meshstrata-t03-preflight');ok=s.recv(64)==b'meshstrata-t03-preflight'
   except OSError as e:error=type(e).__name__
   rows.append(dict(transport=transport,port=port,pass_=ok,error=error,source=s.getsockname()));s.close()
  results.append(dict(source='build',destination='AB'[i],probes=rows));print('build','AB'[i],[(v['transport'],v['port'],v['pass_']) for v in rows],flush=True)
except Exception:
 write(local/'network-error.txt',traceback.format_exc());print('network preflight error; private evidence saved',flush=True)
finally:
 write(local/'network.json',results)
 for w in workers:
  try:print('network cleanup','AB'[w.index],w.call('finish'));w.close()
  except Exception:write(local/('network-cleanup-error-%d.txt'%w.index),traceback.format_exc())
