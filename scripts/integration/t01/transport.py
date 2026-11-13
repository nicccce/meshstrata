#!/usr/bin/env python3
"""T01-N01 IPv4 transport evidence; private endpoints/raw captures never printed.

Inventory is read-only except creation of a unique 0700 evidence directory.
Diagnose requires tcpdump already available, never changes firewall or services.
The standalone netprobe echoes bounded random data, never forwards traffic.
"""
import argparse
import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import time

from audit import listening
from remote import ROOT, Host, KEY, COMMIT, quote, save, stamp, wait

INVENTORY = r'''
date -u; hostname; cat /etc/os-release; uname -m; head -5 /proc/meminfo; cat /proc/1/comm
ip -4 address; ip -4 route; ip -6 address; ip -6 route
(ss -lntup || netstat -lntup) 2>/dev/null
for x in tcpdump iptables-save ip6tables-save nft iptables ufw apk apt-get timeout; do command -v "$x" || true; done
cat /proc/sys/net/ipv6/bindv6only; cat /proc/sys/net/ipv4/ip_forward
iptables-save -c 2>&1; ip6tables-save -c 2>&1; nft -a list ruleset 2>&1; ufw status verbose 2>&1
ls /etc/iptables /etc/nftables* /etc/conf.d/iptables /etc/init.d/iptables /etc/init.d/nftables 2>/dev/null
cat /etc/conf.d/iptables /etc/conf.d/nftables /etc/iptables/rules-save /etc/nftables.nft 2>/dev/null
true
'''
PORTS = [8443, 9443, 28443, 28444, 28445, 28446, 38443]


def parallel(fn, items):
    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
        return list(pool.map(fn, items))


def inventory(h):
    h.ssh('test ! -e '+quote(h.root)+' && mkdir -m 700 '+quote(h.root)+
          ' && mkdir -m 700 '+' '.join(quote(h.root+'/'+d) for d in ['bin', 'private', 'evidence']))
    h.snapshot('before')
    raw = h.ssh(INVENTORY, check=False)
    save(h.out/'inventory-private.txt', raw.stdout+raw.stderr)
    assert ('\nID='+h.label+'\n').encode() in raw.stdout, 'host/platform list order mismatch'
    old = '/root/meshstrata-t01-20261113-1338'+('-alpine' if h.label == 'alpine' else '')
    fixed = h.ssh('cd '+quote(old+'/bin')+' && sha256sum -c SHA256SUMS && cat COMMIT')
    save(h.out/'fixed-artifacts.txt', fixed.stdout)
    route = h.ssh('ip -4 route get '+quote(h.address)+'; printf "%s\n" "$SSH_CONNECTION"').stdout
    save(h.out/'route-and-ssh-source.txt', route)
    return {'host': h.label, 'fixed_sha256_exit': fixed.returncode, 'inventory_saved': True}


def capture_start(h, peer, build_source, isolated=False):
    exe = h.ssh('command -v tcpdump').stdout.decode().strip()
    assert exe.startswith('/')
    filt = '(tcp or udp) and ('+' or '.join('port '+str(p) for p in PORTS)+') and (host '+peer.address+' or host '+build_source+')'
    if isolated:
        # Include loopback as a positive capture control and any NAT source.
        # Port is dedicated to this bounded random echo, with no business data.
        filt = '(tcp or udp) and port 38443'
    base = h.root+'/evidence/headers'
    # Headers only, no pcap or payload output. Only isolated random echo ports.
    child = f'echo "$$" > {quote(base+".pid")}; exec env TZ=UTC {quote(exe)} -i any -nn -tttt -l -s 96 -c 200 {quote(filt)}'
    inner = f'timeout -s INT 45 sh -c {quote(child)}; echo "$?" > {quote(base+".exit")}'
    h.ssh('nohup sh -c '+quote(inner)+' > '+quote(base+'.txt')+' 2>&1 < /dev/null &')
    wait(lambda: b'listening on' in h.get(base+'.txt'))
    return int(h.get(base+'.pid')), exe, base


def capture_stop(h, capture):
    pid, exe, base = capture
    if h.ssh('test -f '+quote(base+'.exit'), check=False).returncode:
        h.ssh(f'test "$(readlink /proc/{pid}/exe)" = '+quote(exe)+f' && kill -INT {pid}')
    wait(lambda: h.ssh('test -f '+quote(base+'.exit'), check=False).returncode == 0)
    save(h.out/'headers-private.txt', h.get(base+'.txt'))
    save(h.out/'capture-exit.txt', h.get(base+'.exit'))


def diagnose(hosts, out, isolated=False):
    captures, processes = [], []
    binary = out/'netprobe'
    result = {'product_commit': COMMIT, 'started_at': stamp(), 'helper_sha256': hashlib.sha256(binary.read_bytes()).hexdigest(), 'runs': []}
    try:
        for h, peer in [(hosts[0], hosts[1]), (hosts[1], hosts[0])]:
            h.put(h.root+'/bin/netprobe', binary.read_bytes())
            h.ssh('chmod 700 '+quote(h.root+'/bin/netprobe'))
            assert h.digest(h.root+'/bin/netprobe') == result['helper_sha256']
            h.snapshot('probe-before')
            assert not any(int(endpoint.rsplit(':', 1)[1]) in PORTS for _, endpoint in listening((h.out/'probe-before.txt').read_text()) if endpoint.rsplit(':', 1)[1].isdigit())
            build_source = h.ssh('printf "%s" "$SSH_CONNECTION"').stdout.decode().split()[0]
            save(h.out/'routes-private.txt', h.ssh('ip -4 route get '+quote(peer.address)+'; ip -4 route get '+quote(build_source)).stdout)
            captures.append((h, capture_start(h, peer, build_source, isolated)))
            args = ['-mode', 'server', '-duration', '35s', '-allow', ','.join(['127.0.0.1', peer.address, build_source])]
            if isolated:
                args += ['-tcp', '38443', '-udp', '38443']
            proc = h.start('netprobe', '', 'netprobe-server', args=args)
            processes.append((h, proc))
            wait(lambda: b'"event":"ready"' in h.get(proc[1]+'.log'))
            save(h.out/'probe-listeners-private.txt', h.ssh('(ss -lntup || netstat -lntup) 2>/dev/null').stdout)
        jobs = [(h, h, 'loopback', '127.0.0.1') for h in hosts]
        jobs += [(source, target, 'public-peer', target.address) for source, target in [(hosts[0], hosts[1]), (hosts[1], hosts[0])]]

        def remote_probe(job):
            source, target, scope, destination = job
            extra = ' -tcp 38443 -udp 38443' if isolated else ''
            r = source.ssh('env -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY -u http_proxy -u https_proxy -u all_proxy '+quote(source.root+'/bin/netprobe')+' -target '+quote(destination)+extra, timeout=15, check=False)
            save(source.out/(scope+'-to-'+target.label+'-private.jsonl'), r.stdout+r.stderr)
            return summarize(r.stdout, source.label, target.label, scope, r.returncode)

        result['runs'] += parallel(remote_probe, jobs)

        def build_probe(h):
            env = {k: v for k, v in os.environ.items() if k.lower() not in ['http_proxy', 'https_proxy', 'all_proxy']}
            args = [str(binary), '-target', h.address]
            if isolated:
                args += ['-tcp', '38443', '-udp', '38443']
            r = subprocess.run(args, capture_output=True, timeout=15, env=env)
            save(h.out/'build-to-public-private.jsonl', r.stdout+r.stderr)
            return summarize(r.stdout, 'build', h.label, 'public-build', r.returncode)
        result['runs'] += parallel(build_probe, hosts)
    finally:
        for h, proc in processes:
            result.setdefault('server_exit', {})[h.label] = h.stop(proc)
            save(h.out/'server-private.jsonl', h.get(proc[1]+'.log'))
        for h, capture in captures:
            capture_stop(h, capture)
        result['finished_at'] = stamp()
        save(out/'transport-summary.json', result)
    return result


def summarize(raw, source, target, scope, code):
    observations = []
    for line in raw.splitlines():
        v = json.loads(line)
        observations.append({'protocol': v['protocol'], 'port': int(v['destination'].rsplit(':', 1)[1]), 'attempt': v['attempt'], 'echo_matches': v.get('echo_matches', False), 'error_kind': 'timeout' if 'timeout' in v.get('error', '') else ('other' if v.get('error') else None), 'started_at': v['started_at'], 'seconds': round(v['seconds'], 3)})
    return {'source': source, 'target': target, 'scope': scope, 'exit': code, 'observations': observations}


def audit(hosts, out):
    result = {}
    for h in hosts:
        scan = h.ssh('for p in /proc/[0-9]*/exe; do v=$(readlink "$p" 2>/dev/null); case "$v" in '+h.root+'/bin/*) echo "$p $v";; esac; done').stdout
        assert not scan, h.label+' test process remains'
        capture_pid = int(h.get(h.root+'/evidence/headers.pid'))
        owner = h.ssh('readlink /proc/'+str(capture_pid)+'/exe', check=False).stdout.decode().strip()
        assert not owner.endswith('/tcpdump'), h.label+' capture remains'
        assert h.root.encode() not in h.ssh('cat /proc/net/unix').stdout
        h.snapshot('after')
        assert listening((h.out/'before.txt').read_text()) == listening((h.out/'after.txt').read_text())
        r = h.ssh(INVENTORY, check=False); save(h.out/'inventory-after-private.txt', r.stdout+r.stderr)
        modes = h.ssh('stat -c "%a %u %g" '+quote(h.root)+' '+quote(h.root+'/private')).stdout.decode().splitlines()
        assert modes == ['700 0 0', '700 0 0']
        old = '/root/meshstrata-t01-20261113-1338'+('-alpine' if h.label == 'alpine' else '')
        fixed = h.ssh('cd '+quote(old+'/bin')+' && sha256sum -c SHA256SUMS')
        save(h.out/'fixed-artifacts-final.txt', fixed.stdout)
        result[h.label] = {'remaining_test_processes': 0, 'capture_remaining': False, 'active_test_unix_sockets': 0, 'host_listeners_unchanged': True, 'modes': modes, 'fixed_sha256_exit': fixed.returncode}
        control = '/tmp/'+Path(h.root).name+'-'+h.label+'.sock'
        if Path(control).exists():
            r = subprocess.run(['ssh', '-i', KEY, '-S', control, '-O', 'exit', 'root@'+h.address], capture_output=True, timeout=10)
            assert r.returncode == 0
        assert not Path(control).exists()
    save(out/'cleanup.json', result)
    return result


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--run', required=True)
    p.add_argument('--phase', choices=['inventory', 'diagnose', 'audit'], required=True)
    p.add_argument('--isolated-only', action='store_true')
    a = p.parse_args()
    assert re.fullmatch(r'meshstrata-t01-n01-[a-zA-Z0-9-]+', a.run)
    os.umask(0o077)
    out = ROOT/'.local'/a.run
    out.mkdir(mode=0o700, exist_ok=True)
    ips = re.findall(r'^\|\s*`(\d+\.\d+\.\d+\.\d+)`\s*\|', (ROOT/'.local/test-servers.md').read_text(), re.M)
    assert len(ips) == 2 and len(set(ips)) == 2
    hosts = [Host(label, ip, a.run, out) for label, ip in zip(['ubuntu', 'alpine'], ips)]
    if a.phase == 'inventory':
        result = parallel(inventory, hosts)
    elif a.phase == 'diagnose':
        result = diagnose(hosts, out, a.isolated_only)
    else:
        result = audit(hosts, out)
    print(json.dumps(result))


if __name__ == '__main__':
    main()
