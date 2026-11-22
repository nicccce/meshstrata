#!/usr/bin/env python3
"""Private SSH transport and read-only host inventory for T03 (no credentials in output)."""
import datetime, hashlib, json, os, pathlib, re, subprocess
ROOT=pathlib.Path(__file__).resolve().parents[3]
COMMIT=os.environ['MESHSTRATA_PRODUCT_COMMIT']
assert COMMIT=='a9398959b19cc5ffa0ab71e8a03efbe73aa14aa8'
assert re.fullmatch(r'[0-9a-f]{40}',COMMIT),'full product commit required'
ART=ROOT/'.local/stage3-artifacts'/COMMIT
SERVER_NOTES=(ROOT/'.local/test-servers.md').read_text()
IPS=list(dict.fromkeys(re.findall(r'\b(?:\d{1,3}\.){3}\d{1,3}\b',SERVER_NOTES)))
IDENTITY=str(pathlib.Path(re.search(r'-i\s+(\S+)',SERVER_NOTES).group(1)).expanduser())
assert len(IPS)==2
SSH=['ssh','-i',IDENTITY,'-o','BatchMode=yes','-o','StrictHostKeyChecking=yes','-o','ConnectTimeout=10']

def ssh(i,command,data=None,timeout=45):
    try:
        return subprocess.run(SSH+['root@'+IPS[i],command],input=data,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=timeout)
    except subprocess.TimeoutExpired as exc:
        return subprocess.CompletedProcess([],124,exc.stdout or b'',(exc.stderr or b'')+b'\nSSH operation timed out\n')

def write(p,b):
    p.write_bytes(b if isinstance(b,bytes) else json.dumps(b,indent=2).encode());p.chmod(0o600)

def main():
    os.umask(0o077)
    p=subprocess.run(['sha256sum','-c','SHA256SUMS'],cwd=ART,capture_output=True);assert p.returncode==0 and p.stdout.count(b': OK')==9
    assert (ART/'COMMIT').read_text().strip()==COMMIT
    for name in ['controller','agent']:
        v=json.loads(subprocess.check_output([str(ART/('meshstrata-'+name)),'version']));assert COMMIT in v.values()
    runid=datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%d-%H%M%S')
    local=ROOT/'.local'/('meshstrata-t03-r01-'+runid);local.mkdir(mode=0o700)
    remote='/root/ms-t03-r01-'+runid
    write(local/'mapping.json',dict(ips=IPS,remote=remote,commit=COMMIT))
    command='''set -eu
id
hostname
cat /etc/os-release
uname -a
cat /proc/1/comm
systemctl list-units --type=service --state=running --no-pager 2>/dev/null || rc-status -a
date -u '+%Y-%m-%dT%H:%M:%SZ'
cat /proc/meminfo
cat /proc/vmstat
 df -Pk /root /tmp
ip -j address 2>/dev/null || ip address
ip route
ss -lntup 2>/dev/null || netstat -lntup
ps -eo pid,ppid,lstart,args 2>/dev/null || ps w
for t in python3 curl openssl ss tcpdump sha256sum timeout; do command -v "$t" || true; done
printf 'SSH_SOURCE=%s\n' "$SSH_CONNECTION"
'''
    results=[]
    for i in range(2):
        p=ssh(i,command);write(local/('host%d-before.txt'%i),p.stdout+p.stderr)
        summary=dict(host='AB'[i],ssh_exit=p.returncode)
        if p.returncode==0:
            s=p.stdout.decode();summary.update(os=re.search(r'^PRETTY_NAME=(.*)$',s,re.M).group(1).strip('"'),mem_kib=int(re.search(r'MemTotal:\s+(\d+)',s).group(1)),inventory='private saved')
        results.append(summary)
    write(local/'preflight.json',results)
    print(json.dumps(dict(private_root=str(local),hosts=results)))
if __name__=='__main__':main()
