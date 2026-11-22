#!/usr/bin/env python3
"""R03 ordinary CLI regressions. Loopback only; no SQL, SSH or host networking."""
import argparse
import base64
import contextlib
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import socket
import tempfile
import threading
import time

spec = importlib.util.spec_from_file_location('i03c', Path(__file__).parents[1]/'i03c/local.py')
lib = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lib)


class CountedSocket:
    def __init__(self, sock, server):
        self.sock, self.server = sock, server
    def __getattr__(self, key):
        return getattr(self.sock, key)
    def sendall(self, data):
        self.sock.sendall(data)
        with self.server.stats_lock:
            if data.startswith(b'HTTP/1.1 407') or data == b'\x01\x01':
                self.server.stats['rejected'] += 1
            if data.startswith(b'HTTP/1.1 200') or data.startswith(b'\x05\x00\x00'):
                self.server.stats['accepted'] += 1


def counted(handler):
    class Handler(handler):
        def handle(self):
            self.request = CountedSocket(self.request, self.server)
            super().handle()
    return Handler


def immutable_task(value):
    # Confirmation may be refreshed by an authenticated recovery Hello. Its
    # timestamp is not the immutable artifact/confirmed-result identity.
    assert value['state'] == 'succeeded'
    nodes = []
    for n in value['nodes']:
        assert n['applied'] and n['verification'] == 'verified' and n['confirmed_at']
        nodes.append({k: n[k] for k in ('node_id', 'target_revision', 'target_digest', 'applied', 'verification', 'rollback', 'cleanup', 'issue') if k in n})
    return {k: value[k] for k in ('id', 'kind', 'subject', 'state', 'deadline', 'created_at')} | {'nodes': sorted(nodes, key=lambda n: n['node_id'])}


class Run(lib.Run):
    def cleanup(self, value, route):
        value = self.http.api('GET', '/api/v1/subscriptions/'+value['id'])
        body = dict(expected_revision=value['revision'], idempotency_key=lib.ident('delete'))
        self.http.api('DELETE', '/api/v1/subscriptions/'+value['id'], body)
        lib.wait_for(lambda: all(o['cleanup'] == 'confirmed' for o in self.http.api('GET', '/api/v1/subscriptions/'+value['id'])['operations']), 90)
        self.http.api('DELETE', '/api/v1/routes/'+route['id'], dict(expected_revision=route['revision'], idempotency_key=lib.ident('archive')))

    def validate(self, pin):
        deadline = time.monotonic()+30
        # Only a known busy slot is retried. Other errors retain their failure.
        def trigger():
            raw, _, status = self.http.raw('POST', '/api/v1/egress-targets/'+pin['id']+'/validate', dict(expected_revision=pin['revision'], idempotency_key=lib.ident('check'), pin=pin), status=(202, 409))
            if status == 409:
                assert json.loads(raw)['error']['code'] in ('OBSERVATION_BUSY', 'OBSERVATION_BACKOFF', 'OBSERVATION_UNAVAILABLE')
                return False
            return True
        lib.wait_for(trigger, min(20, max(0, deadline-time.monotonic())))
        return deadline

    def egress_case(self, node, profile, kind, negative):
        proxy = self.proxies[kind]
        target = dict(node_id=node['id'], type=kind, interface='', host='127.0.0.1', dial_ip='127.0.0.1', port=proxy.server_address[1])
        pin = self.http.api('POST', '/api/v1/egress-targets', dict(expected_revision=0, idempotency_key=lib.ident('target'), name='R03 '+kind, target=target))['pin']
        def auth(password):
            return self.http.api('POST', '/api/v1/egress-targets/'+pin['id']+'/credentials', dict(expected_revision=pin['revision'], idempotency_key=lib.ident('auth'), pin=pin, username=proxy.username, password=password))['pin']
        if negative:
            pin = auth('wrong-'+proxy.password)
            rejected = proxy.stats['rejected']
            deadline = self.validate(pin)
            lib.wait_for(lambda: proxy.stats['rejected'] > rejected, min(10, max(0, deadline-time.monotonic())))
            v = self.http.api('GET', '/api/v1/egress-targets/'+pin['id'])
            assert v['verification'] != 'verified' and not v.get('observation', {}).get('current')
            self.record(kind+'_wrong_auth_rejected_no_current_evidence')
        pin = auth(proxy.password)
        accepted, rejected = proxy.stats['accepted'], proxy.stats['rejected']
        deadline = self.validate(pin)
        observed = lib.wait_for(lambda: self.http.api('GET', '/api/v1/egress-targets/'+pin['id']).get('observation', {}).get('current'), max(0, deadline-time.monotonic()))
        elapsed = time.monotonic()-(deadline-30)
        assert elapsed <= 30, 'observation exceeded original total30s budget'
        if not hasattr(self, 'observation_timings'): self.observation_timings = []
        self.observation_timings.append(dict(kind=kind, mode='wrong-correct' if negative else 'correct-first', elapsed_ms=round(elapsed*1000)))
        assert observed and proxy.stats['accepted'] > accepted and proxy.stats['rejected'] > rejected
        detail = self.http.api('GET', '/api/v1/egress-targets/'+pin['id'])
        assert detail['observation']['target'] == pin and detail['observation']['business_udp'] == 'not_checked'
        self.record(kind+('_wrong_then_correct' if negative else '_correct_first')+'_exact_pin_current_echo_and_negative_auth')
        policy = self.http.api('POST', '/api/v1/egress-policies', dict(expected_revision=0, idempotency_key=lib.ident('policy'), name='R03', node_id=node['id'], dns=node['dns']['pin'], targets=[pin], rules=[dict(destination='MATCH', target=pin, no_resolve=False)]))['pin']
        route = self.route([node], policy=policy)
        value, _, cfg = self.subscribe(route, profile)
        client, request = self.client(cfg)
        # End-to-end bytes, not just proxy handshake counters, prove forwarding.
        request()
        proxy.shutdown(); proxy.server_close()
        lib.wait_for(lambda: self.denied(request), 12)
        self.record(kind+'_actual_tcp_stopped_selected_exit_no_direct_fallback')
        client.stop()
        self.cleanup(value, route)

    def restart_case(self, node, profile):
        route = self.route([node])
        value, _, cfg = self.subscribe(route, profile)
        client, request = self.client(cfg)
        keys = [o['task_id'] for o in value['operations'] if o.get('task_id')]
        assert keys and all(o['state'] == 'confirmed' for o in value['operations'])
        before = {k: immutable_task(self.http.api('GET', '/api/v1/tasks/'+k)) for k in keys}
        client_config = json.loads(Path(client.p.args[-1]).read_text())
        with socket.create_connection(('127.0.0.1', client_config['mixed-port']), timeout=5) as stream:
            stream.sendall(('CONNECT '+self.echo+' HTTP/1.1\r\nHost: '+self.echo+'\r\n\r\n').encode())
            header = b''
            while b'\r\n\r\n' not in header:
                header += stream.recv(1)
                assert len(header) < 4096
            assert b' 200 ' in header
            def echoed():
                payload = os.urandom(32)
                stream.sendall(payload)
                data = b''
                while len(data) < len(payload):
                    chunk = stream.recv(len(payload)-len(data))
                    assert chunk
                    data += chunk
                assert data == payload
            echoed()
            self.cp.stop(); self.cp = self.launch(self.controller, self.ccfg, 'controller-restart')
            lib.wait_for(lambda: self.http.api('GET', '/readyz'))
            self.login()
            node['process'].stop()
            node['process'] = self.launch(self.agent, node['path'], 'agent-restart')
            lib.wait_for(lambda: self.http.api('GET', '/api/v1/nodes/'+node['id'])['control'] == 'online', 40)
            echoed(); request()
        after_value = lib.wait_for(lambda: self.confirmed(value['id']), 30)
        assert [o.get('task_id') for o in after_value['operations']] == [o.get('task_id') for o in value['operations']]
        after = {k: immutable_task(self.http.api('GET', '/api/v1/tasks/'+k)) for k in keys}
        assert before == after
        self.record('node_default_prepared_to_exact_confirmed_task_restart_digest_result_and_retained_stream')
        client.stop(); self.cleanup(value, route)


def scan(run):
    secrets = run.urls+run.passwords+[u.rsplit('/', 1)[-1] for u in run.urls]
    if hasattr(run, 'business'):
        secrets += [v.decode() for k, v in run.business.items() if k in ('key', 'reality')]
    patterns = []
    for secret in secrets:
        if not secret: continue
        b = secret.encode()
        patterns += [b, base64.b64encode(b), b.hex().encode(), json.dumps(secret)[1:-1].encode()]
    logs = list(run.root.rglob('*.log'))
    for p in logs:
        raw = p.read_bytes()
        assert not any(v in raw for v in patterns), 'private process log scan failed'
    return dict(files=len(logs), patterns=len(patterns), findings=0)


def main():
    parser = argparse.ArgumentParser()
    for name in ('controller', 'agent', 'mihomo', 'evidence'):
        parser.add_argument('--'+name, type=Path, required=True)
    parser.add_argument('--scenario', choices=['restart', 'sequence', 'correct-first'], required=True)
    args = parser.parse_args()
    os.umask(0o077)
    assert hashlib.sha256(args.mihomo.read_bytes()).hexdigest() == lib.LOCKED_SHA
    servers = []
    with tempfile.TemporaryDirectory(prefix='mr-') as scratch:
        run = Run(Path(scratch), args.controller.resolve(), args.agent.resolve(), args.mihomo.resolve())
        target = lib.http.server.ThreadingHTTPServer(('127.0.0.1', 0), lib.Target)
        echo = lib.TCP(('127.0.0.1', 0), lib.Echo)
        run.target, run.echo, run.proxies = target, '127.0.0.1:%d'%echo.server_address[1], {}
        servers.extend([target, echo])
        for kind, handler in [('http', lib.HTTPProxy), ('socks5', lib.SOCKSProxy)]:
            proxy = lib.TCP(('127.0.0.1', 0), counted(handler))
            proxy.username, proxy.password = 'r03', lib.secrets.token_urlsafe(24)
            proxy.canary, proxy.target_port = run.echo, target.server_address[1]
            proxy.stats, proxy.stats_lock = dict(accepted=0, rejected=0), threading.Lock()
            run.proxies[kind] = proxy; run.passwords.append(proxy.password); servers.append(proxy)
        for server in servers:
            threading.Thread(target=server.serve_forever, daemon=True).start()
        outcome = 'fail'
        try:
            run.start_controller(); run.business_materials(); node = run.node(); profile = run.profile()
            if args.scenario == 'restart': run.restart_case(node, profile)
            elif args.scenario == 'sequence':
                run.egress_case(node, profile, 'http', False)
                run.egress_case(node, profile, 'socks5', True)
            else: run.egress_case(node, profile, 'socks5', False)
            outcome = 'pass'
        finally:
            run.close()
            scanned = scan(run)
            args.evidence.parent.mkdir(parents=True, exist_ok=True)
            evidence = dict(scenario=args.scenario, result=outcome, network='loopback IPv4 TCP, TUN off, numeric canary, no SSH', checks=run.evidence, log_scan=scanned, observation_timings=getattr(run, 'observation_timings', []), mihomo_sha256=lib.LOCKED_SHA, binary_sha256={name:hashlib.sha256(getattr(args,name).read_bytes()).hexdigest() for name in ('controller','agent')}, source_sha256={str(p.relative_to(Path(__file__).resolve().parents[3])):hashlib.sha256(p.read_bytes()).hexdigest() for p in (Path(__file__).resolve(), Path(lib.__file__))})
            lib.write(args.evidence, evidence)
            # Logs are copied only after successful secret scanning.
            logs = args.evidence.parent/(args.evidence.stem+'-logs'); logs.mkdir(exist_ok=True)
            for i, p in enumerate(run.root.rglob('*.log')): shutil.copy2(p, logs/('%03d.log'%i))
            for server in servers: server.shutdown(); server.server_close()
            if hasattr(run, 'reality'): run.reality.shutdown(); run.reality.server_close()
            print(json.dumps(evidence))

if __name__ == '__main__': main()
