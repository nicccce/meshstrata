#!/usr/bin/env python3
"""Exercise ordinary built binaries locally. All secrets stay in ignored private output."""
import base64
import concurrent.futures
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import socket
import sqlite3
import ssl
import subprocess
import tempfile
import time
import urllib.request
import urllib.error

ROOT = Path(__file__).resolve().parents[2]

def main():
    os.umask(0o077)
    out = Path(tempfile.mkdtemp(prefix="i01-", dir=ROOT / ".local"))
    private = out / "private"
    private.mkdir()
    def save(path, value):
        path.write_text(json.dumps(value) if not isinstance(value, str) else value)
        path.chmod(0o600)
    def port():
        with socket.socket() as s:
            s.bind(("127.0.0.1", 0))
            return s.getsockname()[1]
    web, control = port(), port()
    origin = f"https://127.0.0.1:{web}"
    provision = private / "keys"
    controller_cfg = dict(state_dir=str(private / "controller"),log_level="info", listen_address=f"127.0.0.1:{web}", secure=dict(
        tls_cert_file=str(provision / "web-cert.pem"),tls_key_file=str(provision / "web-key.pem"),web_ca_file=str(provision / "web-ca.pem"),allowed_origins=[origin],
        current_key_id="v1",key_files={"v1":str(provision / "master.key")},bootstrap_file=str(provision / "bootstrap.json"),
        control_listen_address=f"127.0.0.1:{control}",controller_url=f"https://127.0.0.1:{control}",server_names=["127.0.0.1"]))
    cp, ap = private / "controller.json", private / "agent.json"
    save(cp, controller_cfg)
    cb, ab = ROOT / "bin/meshstrata-controller", ROOT / "bin/meshstrata-agent"
    children = []
    def command(binary, action, config, data=None, success=True):
        result = subprocess.run([binary,action,"-config",config],input=data,capture_output=True,timeout=40)
        if (result.returncode == 0) != success:
            raise AssertionError(f"{action} unexpected exit {result.returncode}")
        return result
    def start(binary, config, name):
        log = open(private / f"{name}.log", "ab")
        p = subprocess.Popen([binary,"run","-config",config],stdout=log,stderr=log)
        log.close(); children.append(p)
        return p
    def stop(p):
        if p.poll() is None:
            p.terminate();p.wait(timeout=15)
            assert p.returncode == 0, "ungraceful exit"
    def wait(fn):
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            try:
                if fn(): return
            except (OSError, urllib.error.URLError): pass
            time.sleep(.1)
        raise AssertionError("condition timed out")
    try:
        command(cb,"prepare-local",cp)
        command(cb,"prepare-local",cp,success=False)
        trust = ssl.create_default_context(cafile=str(provision / "web-ca.pem"))
        jar = http.cookiejar.CookieJar()
        admin = urllib.request.build_opener(urllib.request.HTTPSHandler(context=trust),urllib.request.HTTPCookieProcessor(jar))
        anonymous = urllib.request.build_opener(urllib.request.HTTPSHandler(context=trust))
        def request(path,body=None,status=200,headers=None,client=admin):
            h={"Content-Type":"application/json"}
            if body is not None: h["Origin"]=origin
            h.update(headers or {})
            req=urllib.request.Request(origin+path,data=None if body is None else json.dumps(body).encode(),headers=h)
            try:r=client.open(req,timeout=5)
            except urllib.error.HTTPError as e:r=e
            assert r.code==status, f"{path}: expected {status}, got {r.code}"
            raw=r.read();return json.loads(raw) if raw else None
        c=start(cb,cp,"controller")
        wait(lambda:request("/readyz"))
        instance=request("/api/v1/system")["instance_id"]
        request("/api/v1/nodes",status=401,client=anonymous)
        password=secrets.token_urlsafe(24)
        login={"username":"admin","password":password}
        request("/api/v1/auth/initialize",login,status=403)
        command(cb,"initialize-admin",cp,json.dumps(login).encode())
        request("/api/v1/auth/login",login,status=204)
        session=request("/api/v1/auth/session")
        csrf={"X-CSRF-Token":session["csrf_token"]}
        issue=dict(expected_revision=0,idempotency_key="integration-create",name="I01",connection_mode="direct",role="node",control_address="127.0.0.1",proxy_address="127.0.0.1")
        request("/api/v1/nodes",issue,status=403)
        request("/api/v1/nodes",issue,status=403,headers={**csrf,"Origin":"https://wrong.invalid"})
        material=request("/api/v1/nodes",issue,headers=csrf)
        again=request("/api/v1/nodes",issue,headers=csrf)
        assert material==again,"material replay changed"
        node=material["node_id"]
        ca,token=private/"ca.pem",private/"token"
        save(ca,material["ca_pem"]);save(token,material["token"])
        agent_cfg=dict(state_dir=str(private/"agent"),log_level="info",control=dict(mode="control_only",controller_url=material["controller_url"],ca_file=str(ca),ca_fingerprint=material["ca_fingerprint"],node_id=node,token_file=str(token)))
        save(ap,agent_cfg)
        # Two real CLI identities race one unused token against the same real
        # Cipher/Guard/Repository/database composition. Exactly one can enroll.
        race_material=request("/api/v1/nodes",{**issue,"idempotency_key":"race-create","name":"Race"},headers=csrf)
        race_token=private/"race-token";save(race_token,race_material["token"])
        race_paths=[]
        for n in range(2):
            race_cfg={**agent_cfg,"state_dir":str(private/f"race-agent-{n}"),"control":{**agent_cfg["control"],"node_id":race_material["node_id"],"token_file":str(race_token)}}
            race_path=private/f"race-{n}.json";save(race_path,race_cfg);race_paths.append(race_path)
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            results=list(pool.map(lambda cfg:subprocess.run([ab,"enroll","-config",cfg],capture_output=True,timeout=40).returncode,race_paths))
        assert sorted(results)==[0,1], "single-use token race was not exclusive"
        command(ab,"enroll",ap)
        identity=private/"agent/identity/management-identity.json"
        before=hashlib.sha256(identity.read_bytes()).hexdigest()
        command(ab,"enroll",ap) # Same durable CSR safely recovers the committed identity.
        assert hashlib.sha256(identity.read_bytes()).hexdigest()==before
        identity_data=json.loads(identity.read_text())
        node_cert,node_key=private/"node.pem",private/"node-key.pem"
        save(node_cert,base64.b64decode(identity_data["certificate_pem"]).decode())
        key_text=identity_data["key_der"]
        save(node_key,"-----BEGIN PRIVATE KEY-----\n"+"\n".join(key_text[i:i+64] for i in range(0,len(key_text),64))+"\n-----END PRIVATE KEY-----\n")
        node_trust=ssl.create_default_context(cafile=str(ca));node_trust.load_cert_chain(node_cert,node_key)
        node_client=urllib.request.build_opener(urllib.request.HTTPSHandler(context=node_trust))
        try:node_client.open(material["controller_url"]+"/control/v1/tasks/task_"+"1"*32+"/bundle",timeout=5)
        except urllib.error.HTTPError as e:assert e.code==501
        renew=urllib.request.Request(material["controller_url"]+"/enroll/v1/renew",data=json.dumps({"csr_pem":base64.b64decode(identity_data["csr_pem"]).decode()}).encode(),headers={"Content-Type":"application/json"})
        assert json.load(node_client.open(renew,timeout=5))["node_id"]==node
        a=start(ab,ap,"agent")
        path=f"/api/v1/nodes/{node}"
        wait(lambda:request(path)["control"]=="online")
        assert request(path)["verification"]=="not_checked"
        command(ab,"healthcheck",ap)
        command(ab,"enroll",ap,success=False) # Identity lock covers the CLI too.
        stop(c)
        command(ab,"healthcheck",ap) # Controller stop leaves Agent alive.
        c=start(cb,cp,"controller")
        wait(lambda:request("/readyz"))
        assert request("/api/v1/system")["instance_id"]==instance
        wait(lambda:request(path)["control"]=="online")
        stop(a)
        wait(lambda:request(path)["control"]=="offline")
        a=start(ab,ap,"agent")
        wait(lambda:request(path)["control"]=="online")
        assert hashlib.sha256(identity.read_bytes()).hexdigest()==before
        node_data=request(path)
        task=request(path+"/remove",dict(expected_revision=node_data["revision"],idempotency_key="remove"),status=202,headers=csrf)
        request(path,status=404)
        try:
            node_client.open(material["controller_url"]+"/control/v1",timeout=5)
            raise AssertionError("removed certificate reconnected")
        except (urllib.error.URLError, ssl.SSLError): pass
        time.sleep(2)
        command(ab,"enroll",ap,success=False)
        with sqlite3.connect(private/"controller/controller.db") as db:
            assert db.execute("SELECT applied,verification,cleanup_state,result_sequence FROM task_nodes WHERE task_id=?",(task["task_id"],)).fetchone()==(0,"not_checked","pending",0)
        stop(a);stop(c)
        # Missing key and corrupted CA must fail without replacing identity.
        key=provision/"master.key";original=key.read_bytes();key.unlink()
        command(cb,"run",cp,success=False);key.write_bytes(original);key.chmod(0o600)
        with sqlite3.connect(private/"controller/controller.db") as db:
            cert=db.execute("SELECT certificate FROM b02_management_ca").fetchone()[0]
            db.execute("UPDATE b02_management_ca SET certificate=x'01'")
        command(cb,"run",cp,success=False)
        with sqlite3.connect(private/"controller/controller.db") as db:
            assert db.execute("SELECT certificate FROM b02_management_ca").fetchone()[0]==b'\x01'
            db.execute("UPDATE b02_management_ca SET certificate=?",(cert,))
        # A later listener failure releases the earlier listener and component lock.
        with socket.socket() as occupied:
            occupied.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
            occupied.bind(("127.0.0.1",control));occupied.listen()
            command(cb,"run",cp,success=False)
            with socket.socket() as probe:
                probe.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
                probe.bind(("127.0.0.1",web))
        c=start(cb,cp,"controller");wait(lambda:request("/readyz"));stop(c)
        save(out/"evidence.json",dict(result="passed",baseline=subprocess.check_output(["git","rev-parse","HEAD"],cwd=ROOT,text=True).strip(),scope="working tree ordinary binaries; authentication/enrollment/control only",identity_persisted=True,business_execution="disabled",removal_cleanup="pending",private_evidence=str(private)))
        print(f"PASS stage1 binary integration: {out}")
    finally:
        for p in reversed(children):
            if p.poll() is None:
                p.terminate()
                try:p.wait(timeout=15)
                except subprocess.TimeoutExpired:p.kill();p.wait()

if __name__=="__main__":main()
