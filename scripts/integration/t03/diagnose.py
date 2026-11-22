#!/usr/bin/env python3
"""Copy only process logs for a phase whose generated-secret scan passed."""
import json,pathlib,re,sys
from preflight import *
local=pathlib.Path(sys.argv[1]);phase=sys.argv[2];m=json.loads((local/'mapping.json').read_text());result=json.loads((local/phase/'result.json').read_text());assert all(not v for v in result['secret_log_findings'].values()),'log scan did not pass'
for i in range(2):
 r=ssh(i,'find '+m['remote']+' -maxdepth 1 -name "'+phase+'-*.log" -type f -exec cat {} \\;');write(local/phase/('safe-process-logs-%d.txt'%i),r.stdout+r.stderr)
 s=r.stdout.decode()
 for ip in IPS:s=s.replace(ip,'<host>')
 s=re.sub(r'https?://[^\s\x22]+','<url>',s)
 print('AB'[i],s[-4500:])
