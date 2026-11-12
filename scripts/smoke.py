"""Exercise standalone binaries from outside the source checkout."""

import json
from pathlib import Path
import re
import socket
import sqlite3
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


root = Path(__file__).resolve().parents[1]
controller = root / "bin/meshstrata-controller"
agent = root / "bin/meshstrata-agent"
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def eventually(check, process=None):
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if process is not None and process.poll() is not None:
            raise AssertionError(f"process exited early: {process.returncode}")
        try:
            result = check()
            if result:
                return result
        except (OSError, urllib.error.URLError, AssertionError):
            pass
        time.sleep(0.05)
    raise AssertionError("service did not become ready")


with tempfile.TemporaryDirectory(prefix="meshstrata-smoke-") as scratch:
    directory = Path(scratch)
    with socket.socket() as reservation:
        reservation.bind(("127.0.0.1", 0))
        port = reservation.getsockname()[1]
    base = f"http://127.0.0.1:{port}"
    configs = {}
    for name in ("controller", "agent"):
        cfg = {"state_dir": str(directory / name), "log_level": "warn"}
        if name == "controller":
            cfg["listen_address"] = f"127.0.0.1:{port}"
        path = directory / f"{name}.json"
        path.write_text(json.dumps(cfg))
        configs[name] = path

    def command(binary, action, name, expected=0):
        result = subprocess.run(
            [str(binary), action, "-config", str(configs[name])],
            cwd=directory, capture_output=True, text=True, timeout=10,
        )
        assert result.returncode == expected, (result.returncode, result.stderr)
        return json.loads(result.stdout) if expected == 0 else None

    for binary, name in ((controller, "controller"), (agent, "agent")):
        info = command(binary, "version", name)
        assert info["component"] == name
        assert info["commit"] != "unknown" and info["build_time"] != "unknown"
        command(binary, "check-config", name)
        assert not (directory / name).exists(), "config validation created state"

    def request(path):
        with opener.open(base + path, timeout=2) as response:
            return response.status, response.headers, response.read()

    def system():
        return json.loads(request("/api/v1/system")[2])

    processes = []
    log = (directory / "processes.log").open("w+")

    def start(binary, name):
        process = subprocess.Popen(
            [str(binary), "run", "-config", str(configs[name])],
            cwd=directory, stdout=log, stderr=log,
        )
        processes.append(process)
        return process

    def stop(process):
        process.terminate()
        assert process.wait(timeout=12) == 0, "graceful shutdown failed"

    def healthy_agent():
        result = subprocess.run(
            [str(agent), "healthcheck", "-config", str(configs["agent"])],
            cwd=directory, capture_output=True, text=True, timeout=5,
        )
        if result.returncode != 0:
            return None
        health = json.loads(result.stdout)
        assert health["component"] == "agent" and health["status"] == "ok"
        return health

    try:
        node = start(agent, "agent")
        eventually(healthy_agent, node)
        manager = start(controller, "controller")
        eventually(lambda: request("/readyz")[0] == 200, manager)
        identity = system()["instance_id"]
        assert identity
        assert json.loads(request("/healthz")[2])["status"] == "ok"
        status, headers, page = request("/")
        assert status == 200 and "text/html" in headers["Content-Type"]
        assets = re.findall(rb'(?:src|href)="(/assets/[^\"]+)"', page)
        assert assets, "no embedded frontend assets"
        for asset in assets:
            assert request(asset.decode())[0] == 200
        try:
            request("/api/unknown")
            raise AssertionError("unknown API returned success")
        except urllib.error.HTTPError as error:
            assert error.code == 404
            assert json.load(error)["error"]["code"] == "NOT_FOUND"
        command(agent, "run", "agent", expected=1)
        assert healthy_agent(), "duplicate startup disrupted Agent"
        stop(manager)
        assert healthy_agent(), "Controller shutdown disrupted Agent"
        manager = start(controller, "controller")
        eventually(lambda: request("/readyz")[0] == 200, manager)
        assert system()["instance_id"] == identity, "instance ID changed on restart"
        stop(node)
        assert not (directory / "agent/health.sock").exists()
        command(agent, "healthcheck", "agent", expected=1)
        assert request("/readyz")[0] == 200, "Agent shutdown disrupted Controller"
        node = start(agent, "agent")
        eventually(healthy_agent, node)
        for name in ("controller", "agent"):
            assert (directory / name).stat().st_mode & 0o777 == 0o700
        with sqlite3.connect(directory / "controller/controller.db") as db:
            assert db.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
            assert db.execute("SELECT COUNT(*) FROM schema_migrations").fetchone()[0] == len(list((root / "controller/internal/storage/migrations").glob("*.sql")))
            assert db.execute("PRAGMA foreign_key_check").fetchall() == []
        stop(node)
        stop(manager)
        assert not (directory / "agent/health.sock").exists()
    except BaseException:
        log.flush()
        log.seek(0)
        print(log.read())
        raise
    finally:
        for process in processes:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=12)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
        log.close()

print("Smoke checks passed: embedded assets, persistent SQLite, independent lifecycles, health and shutdown")
