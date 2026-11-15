#!/usr/bin/env python3
"""Validate ONE module overlay on an explicit committed baseline; no index writes."""
import argparse
import fcntl
import io
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile


ROOT = Path(__file__).resolve().parents[1]
STORAGE = "controller/internal/storage"
# Frozen interfaces are intentionally absent. Only implementation-owned sources
# and this module's SQL proposals are copied from the actively edited checkout.
OWNED = {
    "B01": ["controller/internal/auth", "controller/internal/secrets",
            f"{STORAGE}/queries/settings.sql"],
    "B02": ["controller/internal/enrollment", "controller/internal/control",
            "agent/internal/enrollment", "agent/internal/control", "internal/pki",
            f"{STORAGE}/nodes.go", f"{STORAGE}/queries/nodes.sql"],
    "B03": ["internal/mihomo", "agent/internal/mihomo", "scripts/compat/mihomo"],
    "B04": ["controller/internal/releases", "controller/internal/ports",
            f"{STORAGE}/ledger.go", f"{STORAGE}/queries/tasks.sql", f"{STORAGE}/queries/references.sql"],
    "B05": ["agent/internal/runtime", "agent/internal/checkpoint", "agent/internal/authorization"],
}
PACKAGES = {
    "B01": ["./controller/internal/auth/...", "./controller/internal/secrets/..."],
    "B02": ["./controller/internal/enrollment/...", "./controller/internal/control/...",
            "./agent/internal/enrollment/...", "./agent/internal/control/...", "./internal/pki/..."],
    "B03": ["./internal/mihomo/...", "./agent/internal/mihomo/..."],
    "B04": ["./controller/internal/releases/...", "./controller/internal/ports/..."],
    "B05": ["./agent/internal/runtime/...", "./agent/internal/checkpoint/...", "./agent/internal/authorization/..."],
}
SOURCE_SUFFIXES = {".go", ".sql", ".sh", ".py", ".md", ".json", ".yaml", ".yml", ".txt"}


# Historical 001-005 hashes reviewed at I02-P; never read active migration bytes.
STAGE2_MIGRATIONS = {'001_initial.sql': '549eebb41b63decb04ab49cfd4e9ab905be8187d1b6620db7fe561cf4ae38699',
 '002_foundation.sql': '07495b64a4bf03ac762f418cf3d7f960701c86cbdfa141c8a9cc097741dba93f',
 '003_b01.sql': '19c53ecbd6668d3eab58e7a043ba191e750ce9590bb027fb106ea39ea62a8d78',
 '004_b02.sql': 'c80a1ab72d1d5c349855c392626ff6c5e8dde64cceb3c3b2a54c4c9b038a4a74',
 '005_b03.sql': '981210aadaf3a298f6ed797ce86de7dd248135ac96173a30819e861f737ff82d'}

def run(args, cwd, env=None):
    subprocess.run(args, cwd=cwd, env=env, check=True)


def safe_source(source):
    try:
        rel = source.relative_to(ROOT)
        if ".." in rel.parts:
            raise ValueError("parent traversal")
    except ValueError:
        raise SystemExit("source outside workspace")
    for parent in [ROOT, *[ROOT.joinpath(*rel.parts[:i]) for i in range(1, len(rel.parts)+1)]]:
        if parent.is_symlink():
            raise SystemExit(f"symlink not allowed: {rel}")
    return source


def overlay_file(source, target):
    safe_source(source)
    if source.is_symlink() or not source.is_file():
        raise SystemExit(f"source must be a regular file: {source.relative_to(ROOT)}")
    if source.suffix not in SOURCE_SUFFIXES:
        raise SystemExit(f"non-source file in module overlay: {source.relative_to(ROOT)}")
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(source, target)


def prepare(module, base):
    if module in {"B04", "B05"} and not re.fullmatch(r"[0-9a-f]{40}", base):
        raise SystemExit("stage-2 requires the assigned full commit hash")
    revision = subprocess.check_output(
        ["git", "rev-parse", "--verify", f"{base}^{{commit}}"], cwd=ROOT, text=True).strip()
    parent = ROOT / ".local/backend-check" / module
    safe_source(parent)
    parent.mkdir(parents=True, exist_ok=True)
    target = Path(tempfile.mkdtemp(prefix="run-", dir=parent))
    archive = subprocess.check_output(["git", "archive", revision], cwd=ROOT)
    with tarfile.open(fileobj=io.BytesIO(archive)) as snapshot:
        for entry in snapshot.getmembers():
            path = Path(entry.name)
            if path.is_absolute() or ".." in path.parts or entry.issym() or entry.islnk() or not (entry.isfile() or entry.isdir()):
                raise SystemExit("unsafe baseline archive entry")
            if entry.name.startswith("controller/web/") or entry.name.startswith("controller/internal/webui/dist/") and path.name != ".gitkeep":
                continue
            snapshot.extract(entry, target, filter="data")
    for rel in ["internal/secretstore/secretstore.go", "internal/adminauth/adminauth.go",
                f"{STORAGE}/database.go"]:
        if not (target / rel).is_file():
            raise SystemExit("baseline must contain the frozen I00 interfaces")
    versions = sorted(p.name[:3] for p in (target / STORAGE / "migrations").glob("*.sql"))
    stage2 = module in {"B04", "B05"}
    baseline_hashes = {p.name: hashlib.sha256(p.read_bytes()).hexdigest()
                       for p in (target / STORAGE / "migrations").glob("*.sql")}
    if stage2:
        if baseline_hashes != STAGE2_MIGRATIONS:
            raise SystemExit("stage-2 baseline requires exact frozen formal 001-005 migrations")
        for rel in ["internal/control/bundle.go", "internal/control/state.go"]:
            if not (target / rel).is_file():
                raise SystemExit("stage-2 baseline missing I02-P frozen contract")
    elif versions != ["001", "002"]:
        raise SystemExit("stage-1 validation requires the pre-I01 baseline (001/002 only)")
    copied = []
    removed = set()
    # Reflect deletions of module-owned query/test files as well as directories.
    for pattern in [f"queries/{module.lower()}_*.sql", f"{module.lower()}_*_test.go"]:
        for old in (target / STORAGE).glob(pattern):
            removed.add(str(old.relative_to(target)))
            old.unlink()
    for rel in OWNED[module]:
        source, dest = safe_source(ROOT / rel), target / rel
        # Removing the baseline's owned subtree also reflects module deletions.
        if dest.is_dir():
            removed.update(str(p.relative_to(target)) for p in dest.rglob("*") if p.is_file())
            shutil.rmtree(dest)
        elif dest.exists():
            removed.add(rel)
            dest.unlink()
        if source.is_symlink():
            raise SystemExit(f"symlink not allowed: {rel}")
        if source.is_dir():
            for path in sorted(source.rglob("*")):
                if any(part in {"node_modules", ".local", ".git", "__pycache__"}
                       for part in path.relative_to(source).parts):
                    continue
                if path.is_symlink():
                    raise SystemExit(f"symlink not allowed: {path.relative_to(ROOT)}")
                if path.is_file():
                    overlay_file(path, target / path.relative_to(ROOT))
                    copied.append(str(path.relative_to(ROOT)))
        elif source.is_file():
            overlay_file(source, dest)
            copied.append(rel)
    safe_source(ROOT / STORAGE / "queries")
    for source in sorted((ROOT / STORAGE / "queries").glob(f"{module.lower()}_*.sql")):
        overlay_file(source, target / source.relative_to(ROOT))
        copied.append(str(source.relative_to(ROOT)))
    if module in {"B02", "B04"}:
        for source in sorted((ROOT / STORAGE).glob(f"{module.lower()}_*_test.go")):
            overlay_file(source, target / source.relative_to(ROOT))
            copied.append(str(source.relative_to(ROOT)))
    migrations = target / STORAGE / "migrations"
    slot = {"B01": 3, "B02": 4, "B03": 5, "B04": 6, "B05": 7}[module]
    # Earlier stage-1 slots are scratch-only no-ops, never sibling proposals.
    for n in range(6 if stage2 else 3, slot):
        (migrations / f"{n:03}_scratch_placeholder.sql").write_text(
            "-- Isolated validation only: sibling migration intentionally absent.\nSELECT 1;\n")
    proposal = safe_source(ROOT / STORAGE / "proposals" / f"{module}.sql")
    dest = migrations / f"{slot:03}_{module.lower()}.sql"
    if proposal.exists():
        overlay_file(proposal, dest)
        copied.append(str(proposal.relative_to(ROOT)))
    else:
        if stage2:
            raise SystemExit("stage-2 requires explicit module SQL proposal (reasoned SELECT 1 if no Controller migration)")
        dest.write_text("-- This module has no migration proposal yet.\nSELECT 1;\n")
    depfile = safe_source(ROOT / "documents/backend-delivery" / f"{module}.deps")
    deps = []
    if depfile.exists():
        if depfile.is_symlink():
            raise SystemExit("dependency proposal must be a regular file")
        for line in depfile.read_text().splitlines():
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            if not re.fullmatch(r"[A-Za-z0-9._~/-]+@v[0-9]+\.[0-9]+\.[0-9]+[A-Za-z0-9.+-]*", line):
                raise SystemExit("dependency proposal requires exact module@vX.Y.Z versions")
            deps.append(line)
    manifest = {"module": module, "base": revision, "overlay": copied, "dependencies": deps,
                "status": "prepared_not_checked", "checks": [],
                "deleted": sorted(removed - set(copied)),
                "baseline_migrations": baseline_hashes,
                "overlay_sha256": {rel: hashlib.sha256((dest if rel == str(proposal.relative_to(ROOT)) else target / rel).read_bytes()).hexdigest() for rel in copied},
                "scratch_migrations": [f"{n:03}" for n in range(6 if stage2 else 3, slot)],
                "note": "isolated module validation, NOT integrated validation"}
    (target / "VALIDATION.json").write_text(json.dumps(manifest, indent=2) + "\n")
    print(f"Isolated {module} snapshot: {target}\nBaseline: {revision}", flush=True)
    return target, deps


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("module", choices=OWNED)
    parser.add_argument("--base", required=True, help="fixed I00 (stage 1) or I02-P (stage 2) commit assigned by coordinator")
    parser.add_argument("--prepare-only", action="store_true")
    args = parser.parse_args()
    # Preparation and compilation both share one cross-process lock. Never run
    # go/sqlc directly while another developer owns this lock.
    cache = ROOT / ".local/cache"
    safe_source(cache)
    cache.mkdir(parents=True, exist_ok=True)
    safe_source(cache / "backend-check.lock")
    with (cache / "backend-check.lock").open("w") as lock:
        print("Waiting for backend-check lock", flush=True)
        fcntl.flock(lock, fcntl.LOCK_EX)
        target, deps = prepare(args.module, args.base)
        manifest_path = target / "VALIDATION.json"
        manifest = json.loads(manifest_path.read_text())
        def status(value):
            manifest["status"] = value
            manifest_path.write_text(json.dumps(manifest, indent=2) + "\n")
        if args.prepare_only:
            status("prepare_only_NOT_PASSED")
            print("PREPARED ONLY: no module checks performed; NOT a pass", flush=True)
            return
        status("checking")
        env = dict(os.environ, GOMAXPROCS="1", CGO_ENABLED="0", GOFLAGS="-p=1",
                   GOCACHE=str(cache / "go"), GOTMPDIR=str(cache / "go-tmp"))
        Path(env["GOTMPDIR"]).mkdir(parents=True, exist_ok=True)
        try:
            packages = PACKAGES[args.module].copy()
            missing = []
            for package in packages:
                files = list((target / package[2:-4]).rglob("*.go"))
                # A directory containing just `package name` or tests is no implementation.
                if not any(not p.name.endswith("_test.go") and re.search(
                        r"(?m)^\s*func\s", p.read_text()) for p in files):
                    missing.append(package)
            if missing:
                raise SystemExit("module source incomplete/empty; not a validation pass: " + ", ".join(missing))
            for dep in deps:
                module, version = dep.rsplit("@", 1)
                run(["go", "mod", "edit", f"-require={module}@{version}"], target, env)
            if deps:
                run(["go", "mod", "tidy"], target, env)
            commands = [["sqlc", "generate"],
                        ["go", "test", "-p", "1", *packages, "./internal/control", "./controller/internal/storage", "./controller/api"],
                        ["go", "vet", "-p", "1", *packages, "./internal/control", "./controller/internal/storage", "./controller/api"]]
            for command in commands:
                try:
                    run(command, target, env)
                except subprocess.CalledProcessError as error:
                    manifest["checks"].append({"command": command, "exit_code": error.returncode})
                    raise
                manifest["checks"].append({"command": command, "exit_code": 0})
                status("checking")
        except BaseException:
            status("failed_NOT_PASSED")
            raise
        status("passed_isolated_only")
        print("PASS: isolated module checks only; integration still required", flush=True)


if __name__ == "__main__":
    main()
