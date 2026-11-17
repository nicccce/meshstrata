#!/usr/bin/env python3
"""Run stage two using only ordinary binaries, loopback TLS and isolated state."""
import argparse
import base64
from contextlib import contextmanager
from datetime import datetime, timedelta, timezone
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import signal
import shutil
import socket
import socketserver
import sqlite3
import ssl
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[2]
ARTIFACT = ROOT / "scripts/compat/mihomo/version-lock.json"


def note(message):
    print(message, flush=True)


def utc(seconds=0):
    return (datetime.now(timezone.utc) + timedelta(seconds=seconds)).isoformat().replace("+00:00", "Z")


def encode(value):
    return json.dumps(value, separators=(",", ":")).encode()


def save(path, value):
    path.write_bytes(value if isinstance(value, bytes) else value.encode() if isinstance(value, str) else encode(value))
    path.chmod(0o600)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def uid(prefix):
    return prefix + "_" + secrets.token_hex(16)


def reserve():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    return s


def port():
    with reserve() as s:
        return s.getsockname()[1]


def wait(condition, name, seconds=30):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            value = condition()
            if value:
                return value
        except (OSError, urllib.error.URLError):
            pass
        time.sleep(0.15)
    raise AssertionError(f"timed out waiting for {name}")


def command(binary, action, config, data=None, error=None):
    result = subprocess.run([binary, action, "-config", config], input=None if data is None else encode(data),
                            capture_output=True, timeout=45)
    if error is not None:
        assert result.returncode != 0 and error in result.stderr.decode(), (action, result.stderr.decode())
        return
    assert result.returncode == 0, (action, result.stderr.decode())
    return json.loads(result.stdout)


def end(proc, kill=False, require_clean=True):
    if proc is None or proc.poll() is not None:
        return
    if kill:
        proc.kill()
    else:
        proc.terminate()
    try:
        proc.wait(timeout=15)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait(timeout=5)
        raise
    if not kill and require_clean:
        assert proc.returncode == 0, ("unexpected exit", proc.returncode)


class Echo(socketserver.BaseRequestHandler):
    def handle(self):
        with self.request as conn:
            conn.settimeout(160)
            while data := conn.recv(4096):
                conn.sendall(data)


class EchoServer(socketserver.ThreadingTCPServer):
    daemon_threads = True


def receive(conn, count):
    data = b""
    while len(data) < count:
        part = conn.recv(count - len(data))
        if not part:
            raise EOFError()
        data += part
    return data


def roundtrip(conn):
    payload = secrets.token_bytes(32)
    conn.sendall(payload)
    assert receive(conn, len(payload)) == payload


class Lab:
    def __init__(self, args, root):
        self.root = root
        self.cb = args.bin_dir / "meshstrata-controller"
        self.ab = args.bin_dir / "meshstrata-agent"
        self.inspect = args.inspect
        self.peer_binary = args.peer
        self.kernel = root / "mihomo"
        with args.mihomo.open("rb") as f:
            assert hashlib.file_digest(f, "sha256").hexdigest() == json.loads(ARTIFACT.read_bytes())["binary_sha256"]
        shutil.copyfile(args.mihomo, self.kernel)
        self.kernel.chmod(0o700)
        self.children = []
        self.agents = []
        self.controller = None
        self.origin = "https://127.0.0.1:" + str(port())
        self.control = "https://127.0.0.1:" + str(port())
        self.state = root / "controller"
        self.cp = root / "controller.json"
        keys = root / "keys"
        save(self.cp, dict(state_dir=str(self.state), log_level="info", listen_address=self.origin[8:],
                           secure=dict(tls_cert_file=str(keys / "web-cert.pem"),
                                       tls_key_file=str(keys / "web-key.pem"), web_ca_file=str(keys / "web-ca.pem"),
                                       current_key_id="stage2", key_files={"stage2": str(keys / "master.key")},
                                       allowed_origins=[self.origin], bootstrap_file=str(keys / "bootstrap.json"),
                                       control_listen_address=self.control[8:], controller_url=self.control,
                                       server_names=["127.0.0.1"])))
        command(self.cb, "prepare-local", self.cp)
        self.trust = ssl.create_default_context(cafile=str(keys / "web-ca.pem"))
        self.admin = urllib.request.build_opener(urllib.request.HTTPSHandler(context=self.trust),
                                                 urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        self.anonymous = urllib.request.build_opener(urllib.request.HTTPSHandler(context=self.trust))
        self.start_controller()
        login = dict(username="admin", password=secrets.token_urlsafe(24))
        command(self.cb, "initialize-admin", self.cp, login)
        self.api("/api/v1/auth/login", login, 204)
        self.csrf = {"X-CSRF-Token": self.api("/api/v1/auth/session")["csrf_token"]}
        self.api("/api/v1/tasks", status=401, client=self.anonymous)

    def spawn(self, argv, name):
        with (self.root / (name + ".log")).open("ab") as log:
            child = subprocess.Popen(argv, stdout=log, stderr=log, env={**os.environ, "GOMAXPROCS": "1"})
        self.children.append(child)
        return child

    def start_controller(self):
        self.controller = self.spawn([self.cb, "run", "-config", self.cp], "controller")
        wait(lambda: self.api("/readyz", client=self.anonymous), "Controller readiness")

    def stop_controller(self, kill=False):
        end(self.controller, kill=kill)
        self.controller = None

    def api(self, path, data=None, status=200, headers=None, client=None):
        h = {"Content-Type": "application/json", "Origin": self.origin}
        h.update(headers or {})
        req = urllib.request.Request(self.origin + path, data=None if data is None else encode(data), headers=h)
        try:
            r = (client or self.admin).open(req, timeout=4)
        except urllib.error.HTTPError as e:
            r = e
        with r:
            assert r.code == status, (path, status, r.code)
            raw = r.read()
            return json.loads(raw) if raw else None

    def db(self, sql, *values):
        with sqlite3.connect(f"file:{self.state}/controller.db?mode=ro", uri=True) as db:
            return db.execute(sql, values).fetchall()

    def agent(self, name, prepare=True):
        issue = dict(expected_revision=0, idempotency_key=uid("create"), name=name, role="node",
                     connection_mode="direct", control_address="127.0.0.1", proxy_address="127.0.0.1")
        material = self.api("/api/v1/nodes", issue, headers=self.csrf)
        a = Agent(self, name, material)
        self.agents.append(a)
        if prepare:
            a.prepare()
        return a

    def task(self, task_id):
        return self.api("/api/v1/tasks/" + task_id)

    def state_is(self, task_id, state, seconds=45):
        def check():
            task = self.task(task_id)
            assert task["state"] in (state, "queued", "running"), task
            return task if task["state"] == state else None
        try:
            return wait(check, f"{task_id} {state}", seconds)
        except AssertionError:
            note(f"task diagnostic: {self.task(task_id)}")
            note(f"target diagnostic: {self.db('SELECT node_id,phase,fetched FROM b04_targets WHERE task_id=?', task_id)}")
            raise

    def submit(self, nodes, kind="publish", *, deadline=90, error=None, stop=True):
        value = dict(mutation=dict(expected_revision=1, idempotency_key=uid("submit")),
                     plan=dict(kind=kind, subject=dict(id=uid("release"), revision=1),
                               deadline=utc(deadline), nodes=nodes))
        if stop:
            self.stop_controller()
        return command(self.cb, "submit-release", self.cp, value, error=error)

    def action(self, task, name, status=202, headers=True):
        return self.api("/api/v1/tasks/" + task["id"] + "/" + name,
                        dict(expected_revision=task["revision"], idempotency_key=uid(name)),
                        status=status, headers=self.csrf if headers else None)

    def close(self):
        failures = []
        for proc in reversed(self.children):
            try:
                end(proc, require_clean=False)
            except (AssertionError, OSError, subprocess.TimeoutExpired) as exc:
                failures.append(str(exc))
        for a in self.agents:
            if a.runtime.exists():
                try:
                    command(self.ab, "stop-runtime", a.config)
                except AssertionError as exc:
                    failures.append(str(exc))
        leftover = []
        for item in Path("/proc").glob("[0-9]*/exe"):
            try:
                if item.readlink() == self.kernel:
                    leftover.append(item.parent.name)
            except (FileNotFoundError, PermissionError):
                pass
        assert not failures and not leftover, (failures, leftover)
        note("cleanup: isolated processes stopped; no process uses the test Mihomo executable")

    @contextmanager
    def client(self, listener):
        with tempfile.TemporaryDirectory(dir=self.root, prefix="client-") as directory:
            directory = Path(directory)
            local = port()
            config = directory / "client.json"
            save(config, {"socks-port": local, "bind-address": "127.0.0.1", "allow-lan": False, "mode": "rule",
                          "proxies": [dict(name="stage2", type="ss", server="127.0.0.1", port=listener["port"],
                                           cipher=listener["cipher"], password=listener["credential"]["password"], udp=True)],
                          "rules": ["MATCH,stage2"]})
            process = self.spawn([self.kernel, "-d", directory, "-f", config], "client")
            try:
                wait(lambda: listening(local), "independent SOCKS client")
                yield local
            finally:
                end(process, require_clean=False)


class Agent:
    def __init__(self, lab, name, material):
        self.lab = lab
        self.name = name
        self.node = material["node_id"]
        self.state = lab.root / name
        self.runtime = self.state / "runtime"
        self.config = lab.root / (name + ".json")
        ca, token = lab.root / (name + ".ca"), lab.root / (name + ".token")
        save(ca, material["ca_pem"])
        save(token, material["token"])
        self.value = dict(state_dir=str(self.state), log_level="info",
                          control=dict(mode="runtime", controller_url=material["controller_url"], ca_file=str(ca),
                                       ca_fingerprint=material["ca_fingerprint"], node_id=self.node, token_file=str(token)),
                          runtime=dict(mihomo_binary=str(lab.kernel)))
        save(self.config, self.value)
        command(lab.ab, "enroll", self.config)
        self.process = None
        self.order = 0

    def prepare(self):
        command(self.lab.ab, "prepare-runtime", self.config)
        key = self.runtime / "checkpoint.key"
        before = digest(key.read_bytes())
        command(self.lab.ab, "prepare-runtime", self.config, error="agent control dependency unavailable")
        assert digest(key.read_bytes()) == before

    def peer(self, mode, payload=None, other=None, *, rejected=False):
        args = [self.lab.peer_binary, "-config", self.config, "-mode", mode]
        if other:
            args += ["-other-config", other.config, "-wrong-ca", self.lab.root / "keys/web-ca.pem"]
        result = subprocess.run(args, input=payload, capture_output=True, timeout=30)
        if rejected:
            assert result.returncode != 0 and ("bad certificate" in result.stderr.decode() or
                                               "bad handshake" in result.stderr.decode()), result.stderr.decode()
            return
        assert result.returncode == 0, (mode, result.stderr.decode())
        return json.loads(result.stdout) if result.stdout else None

    def start(self, online=True):
        self.process = self.lab.spawn([self.lab.ab, "run", "-config", self.config], self.name)
        if online:
            wait(lambda: self.details()["control"] == "online", self.name + " online")

    def stop(self, kill=False):
        end(self.process, kill=kill)
        self.process = None

    def details(self):
        return self.lab.api("/api/v1/nodes/" + self.node)

    def pid(self):
        return json.loads((self.runtime / "process/kernel-process.json").read_text())["PID"]

    def connection_counts(self):
        path = self.runtime / "api/m.sock"
        with socket.socket(socket.AF_UNIX) as sock:
            sock.settimeout(2)
            sock.connect(str(path))
            with sock.makefile("rb") as stream:
                sock.sendall(b"GET /connections HTTP/1.0\r\nHost: localhost\r\n\r\n")
                reply = stream.read()
        data = json.loads(reply.split(b"\r\n\r\n", 1)[1])
        return sorted(c["metadata"].get("inboundName", "") for c in data.get("connections") or [])

    def checkpoint(self, live=False):
        if not live:
            return self.inspect_state(self.state)
        # Copy the authenticated anchor BEFORE the atomically replaced state.
        # The isolated observer may advance its own copied watermark only.
        with tempfile.TemporaryDirectory(dir=self.lab.root, prefix="observe-") as out:
            state = Path(out)
            runtime = state / "runtime"
            ledger = runtime / "checkpoint"
            ledger.mkdir(parents=True, mode=0o700)
            shutil.copyfile(self.runtime / "checkpoint.key", runtime / "checkpoint.key")
            for file in ("initialized.aead", "checkpoint.aead"):
                shutil.copyfile(self.runtime / "checkpoint" / file, ledger / file)
            return self.inspect_state(state)

    def inspect_state(self, path):
        return json.loads(subprocess.check_output([self.lab.inspect, "-state", path, "-node", self.node], timeout=12))

    def listener(self, expires_at, fixed_port=None, generation=1):
        self.order += 1
        return dict(listener_id=f"listener_{self.order:032x}" if self.name == "a" else f"listener_{self.order+1000:032x}",
                    node_id=self.node, protocol="ss2022", address="127.0.0.1", port=fixed_port or port(),
                    transport="tcp_udp", business_udp=True, cipher="2022-blake3-aes-128-gcm",
                    authorization=dict(entry_id=uid("entry"), identity_id=uid("identity"),
                                       generation=generation, expires_at=expires_at, revoked_at=None),
                    credential=dict(ref=uid("credential"), audience=self.node,
                                    password=base64.b64encode(secrets.token_bytes(16)).decode(), uuid=""),
                    server_names=[], short_ids=[])

    def target(self, listeners, *, deps=(), tombs=(), reject=False):
        revision = self.details()["revision"]
        nodes = [a.node for a in deps]
        dns = dict(bootstrap=["1.1.1.1"], resolvers=["1.1.1.1"], ipv6=False)
        rules, egress = ("REJECT", "reject") if reject else ("DIRECT", "direct")
        def pin(name, value):
            return dict(id=uid(name), revision=1, digest=digest(encode(value)))
        bundle = dict(format=1, node_id=self.node, target_revision=revision + 1, revocation_epoch=1,
                      rules=dict(default=rules, pin=pin("rules", rules)), dns=dict(**dns, pin=pin("dns", dns)),
                      egress=dict(mode=egress, pin=pin("egress", egress)), resources=[],
                      listeners=sorted(listeners, key=lambda x: x["listener_id"]),
                      tombstones=sorted(tombs, key=lambda x: x["identity_id"]))
        return dict(node_id=self.node, expected_revision=revision, template=bundle, depends_on=nodes)


def listening(number):
    with socket.create_connection(("127.0.0.1", number), timeout=0.5):
        return True


@contextmanager
def via_socks(local, target):
    with socket.create_connection(("127.0.0.1", local), timeout=3) as conn:
        conn.sendall(b"\x05\x01\x00")
        assert receive(conn, 2) == b"\x05\x00"
        conn.sendall(b"\x05\x01\x00\x01" + socket.inet_aton("127.0.0.1") + target.to_bytes(2, "big"))
        result = receive(conn, 4)
        assert result[:2] == b"\x05\x00", result
        assert result[3] == 1
        receive(conn, 6)
        yield conn


def tomb(listener, reason):
    return dict(identity_id=listener["authorization"]["identity_id"],
                generation=listener["authorization"]["generation"], epoch=1, at=utc(), reason=reason)


def state_pair(lab, task, agent):
    rows = lab.db("""SELECT n.artifact_digest,n.effective_digest,r.payload,r.payload_digest
                     FROM b04_node_state n JOIN b04_results r USING(node_id) WHERE r.task_id=?
                     AND n.node_id=? AND r.verification='verified'""", task, agent.node)
    artifact, effective, payload, payload_digest = rows[0]
    assert digest(payload) == payload_digest
    message = json.loads(payload)
    assert artifact == message["target_digest"] and effective == message["effective_digest"] and artifact != effective
    return artifact, effective


def exercise(lab):
    a, b = lab.agent("a"), lab.agent("b")
    a.start()
    al = a.listener(utc(600))
    short = b.listener(utc(50))
    keep = b.listener(utc(600))
    first_a, first_b = a.target([al], deps=[b]), b.target([short, keep])
    lab.submit([first_a, first_b], stop=False, error="already in use")
    first_id = lab.submit([first_a, first_b])["task_id"]
    assert lab.db("SELECT fetched FROM b04_targets WHERE task_id=?", first_id) == [(0,), (0,)]
    lab.start_controller()
    wait(lambda: a.details()["control"] == "online", "initial reconnect")
    assert lab.task(first_id)["state"] in ("queued", "running")
    assert lab.db("SELECT phase,fetched FROM b04_targets WHERE task_id=? AND node_id=?", first_id, a.node) == [("queued", 0)]
    b.start()
    first = lab.state_is(first_id, "succeeded")
    assert all(n["applied"] and n["verification"] == "verified" for n in first["nodes"])
    payloads = lab.db("SELECT node_id,confirmed_at,received_at FROM b04_results WHERE task_id=? ORDER BY received_at", first_id)
    assert [p[0] for p in payloads] == [b.node, a.node] and payloads[0][2] < payloads[1][2]
    base_artifact, base_effective = state_pair(lab, first_id, b)
    lab.action(first, "rollback", status=403, headers=False)
    assert all(x["id"] != "" for x in lab.api("/api/v1/tasks?limit=1")["items"])
    note("ordinary binaries: stored-task restart, two isolated nodes, DAG order, verified SHA-256 pair, Guard")

    previous_pid = a.pid()
    a.stop()
    assert Path(f"/proc/{previous_pid}/exe").readlink() == lab.kernel
    assert a.checkpoint()["outbox"] == 0
    a.start()
    assert a.pid() == previous_pid
    note("ordinary Agent exit, receipt drain, same-PID adoption")

    with reserve() as blocked:
        blocked.listen()
        conflict = a.listener(utc(600), blocked.getsockname()[1])
        conflict_id = lab.submit([a.target([al, conflict])])["task_id"]
        lab.start_controller()
        failed = lab.state_is(conflict_id, "failed")
        assert failed["nodes"][0]["verification"] == "failed" and failed["nodes"][0]["rollback"] == "confirmed", failed
        uncertain = lab.action(failed, "retry", status=409)
        assert uncertain["error"]["code"] == "GRANT_ORIGIN_UNCERTAIN", uncertain
        assert listening(al["port"])
    note("real occupied listener: failed application, verified rollback, explicit ambiguous-origin retry error")

    # The Controller stays offline until both real connection outcomes are checked.
    echo = EchoServer(("127.0.0.1", 0), Echo)
    threading.Thread(target=echo.serve_forever, daemon=True).start()
    try:
        with lab.client(short) as stale, lab.client(keep) as retained:
            with via_socks(stale, echo.server_address[1]) as old, via_socks(retained, echo.server_address[1]) as good:
                roundtrip(old)
                roundtrip(good)
                lab.stop_controller(kill=True)
                expiry = datetime.fromisoformat(short["authorization"]["expires_at"]).timestamp()
                original_pid = b.pid()
                while time.time() < expiry - 0.5:
                    roundtrip(old)
                    roundtrip(good)
                    time.sleep(0.25)

                note(f"before expiry, inbound names {b.connection_counts()}")
                def old_closed():
                    try:
                        roundtrip(old)
                    except (EOFError, ConnectionResetError, BrokenPipeError):
                        return True
                    return False
                wait(old_closed, "expired established transport close", seconds=14)
                assert b.pid() == original_pid, "local expiry restarted the serving Mihomo"
                assert Path(f"/proc/{original_pid}/exe").readlink() == lab.kernel
                note(f"after old close, inbound names {b.connection_counts()}")
                roundtrip(good)
                id_short = short["authorization"]["identity_id"]
                assert lab.db("SELECT DISTINCT state FROM b04_ports WHERE identity_id=?", id_short) == [("reserved",)]
                lab.start_controller()
                wait(lambda: lab.db("SELECT DISTINCT state FROM b04_ports WHERE identity_id=?", id_short) == [("released",)],
                     "restriction and port confirmation")
                exp = lab.db("""SELECT b.cleanup_state,b.payload,b.payload_digest FROM b04_results b
                                JOIN b04_restrictions r USING(task_id,node_id,sequence)
                                WHERE r.identity_id=? ORDER BY b.sequence""", id_short)
                assert [row[0] for row in exp] == ["pending", "confirmed"] and all(digest(raw) == sha for _, raw, sha in exp)
                assert lab.task(first_id)["state"] == "succeeded"
                after = lab.db("SELECT artifact_digest,effective_digest FROM b04_node_state WHERE node_id=?", b.node)[0]
                assert after[0] == base_artifact and after[1] != base_effective
                roundtrip(good)
    finally:
        echo.shutdown()
        echo.server_close()
    note("offline expiry: old session closed, other identity retained, terminal evidence and port release after receipt")

    rollback_id = lab.action(first, "rollback")["task_id"]
    rollback = lab.state_is(rollback_id, "succeeded")
    assert all(n["rollback"] == "confirmed" for n in rollback["nodes"]), rollback
    kept = lab.db("SELECT payload FROM b04_results WHERE task_id=? AND node_id=? AND verification='verified'", rollback_id, b.node)[0][0]
    b.stop(kill=True)
    checkpoint = b.checkpoint()
    assert checkpoint["outbox"] == 0 and checkpoint["tombstones"] >= 1, checkpoint
    assert checkpoint["checkpoint"]["Verified"]["artifact_digest"] != base_artifact
    before_replay = lab.db("SELECT count(*) FROM b04_results")
    for mode in ("replay", "conflict", "wrong-digest"):
        b.peer(mode, payload=kept)
    b.peer("restriction-pages", payload=encode(dict(
        Checkpoint=json.loads(kept), Events=[json.loads(raw) for _, raw, _ in exp])))
    assert lab.db("SELECT count(*) FROM b04_results") == before_replay
    b.start()
    reuse = b.listener(utc(600), short["port"])
    reuse_id = lab.submit([b.target([keep, reuse], tombs=[tomb(short, "expired")])])["task_id"]
    lab.start_controller()
    lab.state_is(reuse_id, "succeeded")
    note("filtered rollback and port reuse; 104 exact terminal restriction receipts across two overlapping Hello pages")

    # Remove the checkpoint key after normal Agent exit, while its owned kernel remains active.
    a.stop()
    key = a.runtime / "checkpoint.key"
    saved = key.with_suffix(".test-save")
    key.rename(saved)
    try:
        command(lab.ab, "run", a.config, error="runtime checkpoint key unavailable")
        with socket.socket() as check:
            assert check.connect_ex(("127.0.0.1", al["port"])) != 0
    finally:
        saved.rename(key)
    a.start()
    incompatible = lab.root / "downgrade.json"
    downgraded = {"state_dir": str(a.state), "control": {**a.value["control"], "mode": "control_only"}}
    save(incompatible, downgraded)
    a.stop()
    command(lab.ab, "run", incompatible, error="runtime state requires runtime mode")
    a.start()
    note("missing checkpoint key stopped only the isolated managed process; downgrade refused")
    live = crash_boundaries(lab, a, al)
    fallback = response_loss(lab, b, [keep, reuse])
    clear_and_remove(lab, a, live)
    cold_expiry(lab, b, [keep, reuse, fallback])
    assert lab.db("PRAGMA foreign_key_check") == []
    assert lab.db("SELECT count(*) FROM schema_migrations") == [(7,)]


def crash_boundaries(lab, a, existing):
    # With the isolated kernel suspended, the real Agent can persist Candidate
    # but cannot receive a reload response. The helper verifies the boundary
    # after SIGKILL while the Agent state lock is released.
    next_listener = a.listener(utc(600))
    target = a.target([existing, next_listener])
    task = lab.submit([target], deadline=120)["task_id"]
    pid = a.pid()
    render_files = set((a.runtime / "config").glob("effective-*.json"))
    assert Path(f"/proc/{pid}/exe").readlink() == lab.kernel
    os.kill(pid, signal.SIGSTOP)
    try:
        lab.start_controller()
        wait(lambda: set((a.runtime / "config").glob("effective-*.json")) - render_files,
             "durable render before reload", seconds=25)
        wait(lambda: a.checkpoint(live=True)["checkpoint"]["Candidate"]["task_id"] == task,
             "durable Candidate before reload", seconds=15)
        a.stop(kill=True)
        cp = a.checkpoint()["checkpoint"]
        assert cp["Candidate"]["task_id"] == task and cp["Applied"]["task_id"] != task, cp
    finally:
        os.kill(pid, signal.SIGCONT)
    a.start()
    assert a.pid() != pid, "uncertain kernel survived candidate-only crash recovery"
    lab.state_is(task, "succeeded")

    # Observe an actual Applied checkpoint while the independent traffic
    # verification is still running. It must recover through fresh evidence.
    middle_listener = a.listener(utc(600))
    listeners = [existing, next_listener, middle_listener]
    task = lab.submit([a.target(listeners)], deadline=120)["task_id"]
    lab.start_controller()

    def unverified():
        cp = a.checkpoint(live=True)["checkpoint"]
        return cp["Applied"] and cp["Applied"]["task_id"] == task and cp["Verified"] is None

    wait(unverified, "durable Applied before Verified", seconds=25)
    a.stop(kill=True)
    cp = a.checkpoint()["checkpoint"]
    assert cp["Applied"]["task_id"] == task and cp["Verified"] is None, cp
    assert lab.db("SELECT count(*) FROM b04_results WHERE task_id=?", task) == [(0,)]
    a.start()
    lab.state_is(task, "succeeded")

    # A known real listening socket signals activation. Suspend Controller
    # processing before verification, then verify Agent persisted its outbox.
    final_listener = a.listener(utc(600))
    listeners.append(final_listener)
    task = lab.submit([a.target(listeners)], deadline=120)["task_id"]
    lab.start_controller()
    wait(lambda: listening(final_listener["port"]), "server listener before receipt", seconds=25)
    os.kill(lab.controller.pid, signal.SIGSTOP)
    try:
        assert lab.db("SELECT count(*) FROM b04_results WHERE task_id=?", task) == [(0,)]
        wait(lambda: a.checkpoint(live=True)["outbox"] > 0, "verified before receipt", seconds=25)
        a.stop(kill=True)
        cp = a.checkpoint()
        assert cp["outbox"] >= 1 and cp["checkpoint"]["Verified"]["task_id"] == task, cp
        assert lab.db("SELECT count(*) FROM b04_results WHERE task_id=?", task) == [(0,)]
        lab.stop_controller(kill=True)
    finally:
        if lab.controller and lab.controller.poll() is None:
            os.kill(lab.controller.pid, signal.SIGCONT)
    pid = a.pid()
    a.start(online=False)
    lab.start_controller()
    lab.state_is(task, "succeeded")
    time.sleep(0.6)
    a.stop()
    assert a.checkpoint()["outbox"] == 0 and a.pid() == pid
    a.start()
    note("process fault boundaries: Candidate before reload, Applied without verification, Verified/outbox before receipt")
    return listeners


def clear_and_remove(lab, agent, listeners):
    rejected_id = lab.submit([agent.target(listeners, reject=True)])["task_id"]
    lab.start_controller()
    rejected = lab.state_is(rejected_id, "failed")
    assert rejected["nodes"][0]["verification"] == "failed" and rejected["nodes"][0]["rollback"] == "confirmed"
    cleared_id = lab.submit([agent.target([], tombs=[tomb(item, "revoked") for item in listeners])],
                            kind="revoke")["task_id"]
    lab.start_controller()
    cleared = lab.state_is(cleared_id, "succeeded")
    assert cleared["nodes"][0]["applied"] and cleared["nodes"][0]["cleanup"] == "confirmed"
    for item in listeners:
        with socket.socket() as s:
            assert s.connect_ex(("127.0.0.1", item["port"])) != 0
    agent.stop()
    before = agent.checkpoint()["checkpoint"]["Verified"]
    assert before["task_id"] == cleared_id
    agent.start()
    assert lab.db("SELECT reconciliation FROM b04_node_state WHERE node_id=?", agent.node) == [("matched",)]
    agent.stop()
    revision = agent.details()["revision"]
    removed = lab.api(f"/api/v1/nodes/{agent.node}/remove",
                      dict(expected_revision=revision, idempotency_key=uid("remove")), status=202, headers=lab.csrf)
    assert lab.task(removed["task_id"])["nodes"][0]["cleanup"] == "pending"
    agent.peer("fetch-drop", rejected=True)
    note("REJECT stayed failed, explicit zero-listener cleanup verified, removed management certificate denied")


def cold_expiry(lab, agent, retained):
    short = agent.listener(utc(25))
    original = lab.submit([agent.target([*retained, short])])["task_id"]
    lab.start_controller()
    lab.state_is(original, "succeeded")
    agent.stop()
    command(lab.ab, "stop-runtime", agent.config)
    lab.stop_controller()
    expiry = datetime.fromisoformat(short["authorization"]["expires_at"]).timestamp()
    wait(lambda: time.time() >= expiry + 1, "TTL with both node processes stopped", seconds=35)
    agent.start(online=False)
    wait(lambda: listening(retained[0]["port"]), "offline Agent startup filtered old artifact")
    with socket.socket() as s:
        assert s.connect_ex(("127.0.0.1", short["port"])) != 0
    lab.start_controller()
    wait(lambda: lab.db("SELECT DISTINCT state FROM b04_ports WHERE identity_id=?",
                        short["authorization"]["identity_id"]) == [("released",)], "boot restriction evidence")
    note("cold restart after TTL: old startup artifact filtered before control connection")


def response_loss(lab, other, retained):
    interrupted = lab.agent("interrupted")
    known = interrupted.listener(utc(600))
    known_task = lab.submit([interrupted.target([known])])["task_id"]
    lab.start_controller()
    interrupted.start()
    lab.state_is(known_task, "succeeded")
    time.sleep(0.5)
    interrupted.stop()
    uncertain = interrupted.listener(utc(600))
    target = interrupted.target([known, uncertain])
    original = lab.submit([target], deadline=26)["task_id"]
    lab.start_controller()
    first = interrupted.peer("fetch-drop", other=other)
    assert first["task_id"] == original
    assert lab.db("SELECT fetched FROM b04_targets WHERE task_id=?", original) == [(1,)]
    assert interrupted.checkpoint()["outbox"] == 0
    again = interrupted.peer("fetch-drop")
    assert first == again
    end_state = lab.state_is(original, "timed_out", seconds=40)
    assert lab.action(end_state, "retry", status=409)["error"]["code"] == "GRANT_ORIGIN_UNCERTAIN"
    replacement = interrupted.listener(utc(600), uncertain["port"])
    intents = [tomb(uncertain, "revoked"), tomb(known, "revoked")]
    lab.submit([interrupted.target([replacement], tombs=intents)], kind="cleanup", error="PORT_IN_USE")
    lab.start_controller()
    cleanup = lab.submit([interrupted.target([], tombs=intents)], kind="cleanup", deadline=35)["task_id"]
    lab.start_controller()
    interrupted.start()
    assert wait(lambda: lab.task(cleanup)["nodes"][0]["cleanup"] == "pending",
                "unknown grant cleanup remains pending")
    wait(lambda: lab.db("SELECT DISTINCT state FROM b04_ports WHERE identity_id=?",
                        known["authorization"]["identity_id"]) == [("released",)], "known subset cleared")
    assert lab.db("SELECT DISTINCT state FROM b04_ports WHERE identity_id=?",
                  uncertain["authorization"]["identity_id"]) == [("pending_cleanup",)]
    assert lab.db("SELECT count(*) FROM b04_restrictions WHERE identity_id=? AND task_id=?",
                  known["authorization"]["identity_id"], known_task) == [(2,)]
    fallback = other.listener(utc(600))
    fresh = lab.submit([other.target([*retained, fallback])])["task_id"]
    lab.start_controller()
    wait(lambda: other.details()["control"] == "online", "healthy fallback Agent reconnected", seconds=62)
    lab.state_is(fresh, "succeeded")
    note("first response lost after Open: original replay, terminal retry rejected, known subset released, fresh authorized endpoint verified")
    return fallback


def exercise_materials(lab):
    a = lab.agent("a")
    materials = a.runtime / "config/materials"
    cert = dict(id=uid("cert"), purpose="tls_certificate")
    key = dict(id=uid("tlskey"), purpose="tls_private_key")
    reality = dict(id=uid("reality"), purpose="reality_private_key")

    def path(ref):
        return materials / (ref["id"] + "." + ref["purpose"])

    subprocess.run(["openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256",
                    "-nodes", "-days", "1",
                    "-subj", "/CN=stage2.invalid", "-addext", "subjectAltName=DNS:stage2.invalid",
                    "-out", path(cert), "-keyout", path(key)], check=True, capture_output=True, timeout=10)
    path(cert).chmod(0o600)
    path(key).chmod(0o600)
    x25519 = subprocess.check_output(["openssl", "genpkey", "-algorithm", "X25519", "-outform", "DER"], timeout=10)
    assert x25519.startswith(bytes.fromhex("302e020100300506032b656e04220420")) and len(x25519) == 48
    save(path(reality), base64.urlsafe_b64encode(x25519[-32:]).rstrip(b"="))

    class TLSHello(socketserver.BaseRequestHandler):
        def handle(self):
            self.request.settimeout(3)
            try:
                with tls_config.wrap_socket(self.request, server_side=True):
                    pass
            except (OSError, ssl.SSLError):
                pass

    tls_config = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    tls_config.minimum_version = ssl.TLSVersion.TLSv1_3
    tls_config.load_cert_chain(path(cert), path(key))
    destination = EchoServer(("127.0.0.1", 0), TLSHello)
    threading.Thread(target=destination.serve_forever, daemon=True).start()
    try:
        a.value["runtime"]["materials"] = [cert, key, reality]
        save(a.config, a.value)
        a.start()
        expires = utc(480)
        trojan, hy2, vless = [a.listener(expires) for _ in range(3)]
        for listener, protocol, carrier in ((trojan, "trojan", "tcp"), (hy2, "hysteria2", "udp")):
            listener.update(protocol=protocol, transport=carrier, certificate=cert, private_key=key, cipher="")
        vless.update(protocol="vless", transport="tcp", cipher="", reality_key=reality,
                     reality_destination=f"127.0.0.1:{destination.server_address[1]}",
                     server_names=["stage2.invalid"], short_ids=["0123456789abcdef"])
        vless["credential"].update(password="", uuid=str(uuid.uuid4()))
        task = lab.submit([a.target([trojan, hy2, vless])])["task_id"]
        lab.start_controller()
        completed = lab.state_is(task, "succeeded", seconds=60)
        assert completed["nodes"][0]["verification"] == "verified"
        state_pair(lab, task, a)
        time.sleep(0.5)
        a.stop()
        assert a.checkpoint()["outbox"] == 0
        note("ordinary Agent material registration: Trojan, Hysteria 2 and VLESS TCP/UDP and wrong-credential production probes")
    finally:
        destination.shutdown()
        destination.server_close()


def main():
    args = argparse.ArgumentParser(description=__doc__)
    args.add_argument("--mihomo", required=True, type=lambda s: Path(s).resolve())
    args.add_argument("--bin-dir", type=lambda s: Path(s).resolve(), default=ROOT / "bin")
    args.add_argument("--inspect", type=lambda s: Path(s).resolve(), default=ROOT / "bin/stage2-inspect")
    args.add_argument("--peer", type=lambda s: Path(s).resolve(), default=ROOT / "bin/stage2-peer")
    args.add_argument("--suite", choices=("joint", "materials"), default="joint")
    args.add_argument("--logs", type=Path, default=ROOT / ".local/stage2-logs")
    args = args.parse_args()
    os.umask(0o077)
    with tempfile.TemporaryDirectory(prefix="ms2-") as directory:
        root = Path(directory)
        lab = Lab.__new__(Lab)
        try:
            Lab.__init__(lab, args, root)
            if args.suite == "materials":
                exercise_materials(lab)
            else:
                exercise(lab)
        finally:
            try:
                if hasattr(lab, "children"):
                    lab.close()
            finally:
                args.logs.mkdir(mode=0o700, parents=True, exist_ok=True)
                for log in root.glob("*.log"):
                    shutil.copyfile(log, args.logs / log.name)


if __name__ == "__main__":
    main()
