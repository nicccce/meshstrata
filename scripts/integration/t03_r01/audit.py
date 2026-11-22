#!/usr/bin/env python3
"""Read-only final host audit. Removes only the recorded run root when explicitly selected."""
import argparse,json,pathlib,re,sys,traceback
from preflight import *
def listeners(raw):
 return sorted(' '.join(l.split()) for l in raw.splitlines() if re.match(r'^(tcp|udp)\s',l))
def main():
 p=argparse.ArgumentParser();p.add_argument('private_root');p.add_argument('--remove-owned-root',action='store_true');args=p.parse_args();local=pathlib.Path(args.private_root);mapping=json.loads((local/'mapping.json').read_text());remote=mapping['remote'];assert re.fullmatch(r'/root/ms-t03-r01-\d{8}-\d{6}',remote)
 summaries=[]
 for i in range(2):
  hashes=ssh(i,'cd '+remote+' && sha256sum -c SHA256SUMS');write(local/('final-artifact-'+str(i)+'.txt'),hashes.stdout+hashes.stderr);assert hashes.returncode==0 and hashes.stdout.count(b': OK')==9,'final artifact verification failed'
  command='''ss -lntup 2>/dev/null || netstat -lntup
printf '\\nT03_PROCESSES\n'
for p in /proc/[0-9]*/exe; do e=$(readlink "$p" 2>/dev/null || true); case "$e" in RUNROOT/*) printf '%s %s\\n' "$p" "$e";; esac; done
printf '\\nT03_SOCKETS\n'
find RUNROOT -type s 2>/dev/null
printf '\\nT03_MEMORY\\n'
cat /proc/meminfo
cat /proc/vmstat
'''.replace('RUNROOT',remote)
  r=ssh(i,command);write(local/('host%d-after.txt'%i),r.stdout+r.stderr);assert r.returncode==0,'audit command failed'
  raw=r.stdout.decode();before=(local/('host%d-before.txt'%i)).read_text();old=listeners(before);new=listeners(raw.split('T03_PROCESSES')[0]);proc=raw.split('T03_PROCESSES\n')[1].split('T03_SOCKETS')[0].strip();sock=raw.split('T03_SOCKETS\n')[1].split('T03_MEMORY')[0].strip();item=dict(host='AB'[i],artifact_sha256_checks=9,original_listeners_unchanged=old==new,before_listener_count=len(old),after_listener_count=len(new),run_executables_absent=not proc,run_sockets_absent=not sock,oom_kill_before=int(re.search(r'^oom_kill (\d+)',before,re.M).group(1)),oom_kill_after=int(re.search(r'^oom_kill (\d+)',raw,re.M).group(1)))
  if args.remove_owned_root:
   assert old==new and not proc and not sock,'cleanup audit not clear'
   # Exact recorded root, no glob, no host network or other deployment changes.
   r=ssh(i,'rm -rf -- '+remote+' && test ! -e '+remote);write(local/('remove-owned-%d.txt'%i),r.stdout+r.stderr);item['owned_root_removed']=r.returncode==0
  summaries.append(item)
 write(local/'final-audit.json',summaries);print(json.dumps(summaries))
if __name__=='__main__':
 try:main()
 except Exception:
  write(pathlib.Path(sys.argv[1])/'final-audit-error.txt',traceback.format_exc());print('audit incomplete; inspect private evidence');sys.exit(1)
