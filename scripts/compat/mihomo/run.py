#!/usr/bin/env python3
"""Run the opt-in real-kernel suite in ONE prepared B03 baseline snapshot.

Does not download, install, or stop any service. Must not be called while the
caller already holds backend-check.lock. Private configs/logs remain in .local.
"""
import argparse
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path
import platform
import signal
import subprocess
import tempfile

BASE = "aa993fbf817ed89a9bbe668018a78d3fc49258b9"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--workspace", type=Path, required=True)
    parser.add_argument("--snapshot", type=Path)
    parser.add_argument("--integrated", action="store_true")
    parser.add_argument("--binary", type=Path, default=Path("/usr/local/libexec/meshstrata/mihomo-v1.19.33"))
    args = parser.parse_args()
    workspace = args.workspace.resolve()
    if args.integrated:
        if args.snapshot: raise SystemExit("choose integrated or snapshot")
        snapshot = workspace
        baseline = subprocess.check_output(["git", "rev-parse", "HEAD"],cwd=workspace,text=True).strip()
        files = sorted(p for folder in ("agent/internal/mihomo","internal/mihomo","scripts/compat/mihomo") for p in (workspace/folder).rglob("*") if p.is_file() and "__pycache__" not in str(p))
        source_sha256 = None
    else:
        if args.snapshot is None: raise SystemExit("provide --integrated or --snapshot")
        snapshot = args.snapshot.resolve()
        if not snapshot.is_relative_to(workspace / ".local/backend-check/B03"):
            raise SystemExit("snapshot must be an isolated B03 baseline checkout")
        manifest = json.loads((snapshot / "VALIDATION.json").read_text())
        if manifest["base"] != BASE or manifest["module"] != "B03": raise SystemExit("wrong validation baseline")
        baseline, source_sha256 = BASE, None
    lock = json.loads((snapshot / "scripts/compat/mihomo/version-lock.json").read_text())
    if platform.system() != "Linux" or platform.machine() != "x86_64":
        raise SystemExit("only the locked Linux amd64 asset is validated by this runner")
    if hashlib.sha256(args.binary.read_bytes()).hexdigest() != lock["binary_sha256"]:
        raise SystemExit("pinned binary digest mismatch")
    cache = workspace / ".local/cache"
    with (cache / "backend-check.lock").open("w") as handle:
        print("Waiting for backend-check lock", flush=True)
        fcntl.flock(handle, fcntl.LOCK_EX)
        if args.integrated:
            source_sha256 = hashlib.sha256(b"".join(str(p.relative_to(workspace)).encode()+b"\0"+p.read_bytes() for p in files)).hexdigest()
        parent = workspace / ".local/b03"
        parent.mkdir(exist_ok=True)
        # This short .local path also keeps Unix sockets below sockaddr_un's
        # limit. Private configs, keys, and kernel logs never leave this tree.
        out = Path(tempfile.mkdtemp(prefix="compat-", dir=parent))
        runtime = out / "private"
        runtime.mkdir(mode=0o700)
        env = dict(os.environ, GOMAXPROCS="1", CGO_ENABLED="0",
                   GOCACHE=str(cache / "go"), GOTMPDIR=str(cache / "go-tmp"),
                   B03_MIHOMO_BINARY=str(args.binary), B03_ARTIFACT_DIR=str(runtime))
        before = subprocess.check_output(["ss", "-lntup"], text=True)
        with open(out / "test.log", "w", opener=lambda p, f: os.open(p, f, 0o600)) as log:
            child = subprocess.Popen(["go", "test", "-p", "1", "-count=1", "-timeout", "8m",
                                      "-run", "^TestRealKernelCompatibility$", "-v",
                                      "./agent/internal/mihomo"], cwd=snapshot, env=env,
                                     stdout=log, stderr=subprocess.STDOUT,
                                     start_new_session=True)
            try:
                exit_code = child.wait(timeout=540)
            except subprocess.TimeoutExpired:
                exit_code = 124
            finally:
                # The group was created exclusively for this test invocation.
                # Catch orphaned children even if Go's test timeout aborts cleanup.
                try:
                    os.killpg(child.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                child.wait()
        after = subprocess.check_output(["ss", "-lntup"], text=True)
        for name, data in [("host-before.txt", before), ("host-after.txt", after)]:
            p = out / name
            p.write_text(data)
            p.chmod(0o600)
        if (runtime / "evidence.json").exists():
            p = out / "evidence.json"
            p.write_bytes((runtime / "evidence.json").read_bytes())
            p.chmod(0o600)
        meta = dict(baseline=baseline, integrated=args.integrated, source_sha256=source_sha256, snapshot=str(snapshot), runtime=str(runtime),
                    checked_at=datetime.datetime.now(datetime.timezone.utc).isoformat(),
                    command="go test -p 1 -count=1 -timeout 8m -run ^TestRealKernelCompatibility$ -v ./agent/internal/mihomo",
                    exit_code=exit_code,
                    host_listeners_unchanged=sorted(before.splitlines()) == sorted(after.splitlines()),
                    kernel=lock)
        (out / "RUN.json").write_text(json.dumps(meta, indent=2) + "\n")
        (out / "RUN.json").chmod(0o600)
        print(f"Evidence: {out}\nPrivate runtime: {runtime}\nExit: {exit_code}", flush=True)
        print((out / "test.log").read_text(), end="")
        raise SystemExit(exit_code or (0 if meta["host_listeners_unchanged"] else 1))


if __name__ == "__main__":
    main()
