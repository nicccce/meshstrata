#!/usr/bin/env python3
"""Whitelist export after the aggregate scan; never print raw product logs/RPC."""
import collections,datetime as dt,hashlib,json,pathlib,re,shlex,sys,tarfile
from preflight import write
root=pathlib.Path(sys.argv[1]);assert (root/'final-log-scan.json').exists()
offsets={e['host']:e['timezone_offset'] for e in json.loads((root/'clock-readonly-final.json').read_text())}
phases=sorted(p.parent.name for p in root.glob('r*/result.json'));events={p:[] for p in phases};codes={p:collections.Counter() for p in phases}
messages={'Observation requested','Observation recorded','Observation failed','Observation not recorded','Observation unavailable','Observation slot expired','Egress observation unavailable'}
for i in range(2):
 with tarfile.open(root/('host'+str(i)+'-logs.tar.gz'),'r:gz') as tar:
  for member in tar:
   phase=next((p for p in phases if member.name.startswith(p+'-')),None)
   if not phase or not member.isfile():continue
   for line in tar.extractfile(member).read().decode(errors='replace').splitlines():
    try:fields=dict(part.split('=',1) for part in shlex.split(line) if '=' in part)
    except ValueError:continue
    prefix=re.match(r'^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}) (?:DEBUG|INFO|WARN|ERROR) (.*?)(?= [a-z_]+=|$)',line)
    if prefix:
     fields['msg']=prefix[2]
     offset=offsets['AB'[i]];seconds=(int(offset[1:3])*60+int(offset[3:]))*60*(1 if offset[0]=='+' else -1)
     wall=dt.datetime.strptime(prefix[1],'%Y/%m/%d %H:%M:%S').replace(tzinfo=dt.timezone(dt.timedelta(seconds=seconds)))
     fields['time']=wall.astimezone(dt.timezone.utc).isoformat().replace('+00:00','Z')
    code=fields.get('code','')
    if re.fullmatch('[A-Z][A-Z_]{2,63}',code):codes[phase][code]+=1
    if fields.get('msg') not in messages:continue
    out=dict(host='AB'[i],message=fields['msg'])
    for key in ('time','node','session','request','node_revision','egress','stage','code','age_ms','ttl_ms'):
     value=fields.get(key)
     if value is None:continue
     if key in ('node','session','request') and not re.fullmatch('[a-z_]+_[0-9a-f]{32}',value):continue
     if key=='time' and not re.fullmatch(r'[0-9TZ:+.\-]+',value):continue
     if key=='stage' and not re.fullmatch('[a-z_]{1,32}',value):continue
     if key=='code' and not re.fullmatch('[A-Z][A-Z_]{2,63}',value):continue
     if key in ('node_revision','age_ms','ttl_ms') and not re.fullmatch('-?[0-9]+',value):continue
     if key=='egress' and value not in ('true','false'):continue
     out[key]=value
    events[phase].append(out)
summary={}
for phase in phases:
 out=sorted(events[phase],key=lambda e:e.get('time',''));write(root/phase/'observation-safe-timeline.json',out)
 stages=collections.Counter(e.get('stage','unknown') for e in out)
 requests={e['request'] for e in out if 'request' in e}
 sent={e['request']:e for e in out if e['message']=='Observation requested' and 'request' in e}
 recorded={e['request'] for e in out if e['message']=='Observation recorded' and 'request' in e}
 summary[phase]=dict(events=len(out),request_ids=len(requests),sent=len(sent),recorded=len(recorded),stages=dict(stages),codes=dict(codes[phase]),safe_timeline_sha256=hashlib.sha256((root/phase/'observation-safe-timeline.json').read_bytes()).hexdigest())
write(root/'safe-observation-summary.json',summary)
print(json.dumps(summary,indent=2))
