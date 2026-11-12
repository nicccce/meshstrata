#!/usr/bin/env python3
"""Validate ONE stage-1 overlay on an explicit committed baseline; no index writes."""
import argparse
import fcntl
import io
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
}
PACKAGES = {
    "B01": ["./controller/internal/auth/...", "./controller/internal/secrets/..."],
    "B02": ["./controller/internal/enrollment/...", "./controller/internal/control/...",
            "./agent/internal/enrollment/...", "./agent/internal/control/...", "./internal/pki/..."],
    "B03": ["./internal/mihomo/...", "./agent/internal/mihomo/..."],
}
SOURCE_SUFFIXES = {".go", ".sql", ".sh", ".py", ".md", ".json", ".yaml", ".yml", ".txt"}


def run(args, cwd, env=None):
    subprocess.run(args, cwd=cwd, env=env, check=True)


def overlay_file(source, target):
    if source.is_symlink() or not source.is_file():
        raise SystemExit(f"source must be a regular file: {source.relative_to(ROOT)}")
    if source.suffix not in SOURCE_SUFFIXES:
        raise SystemExit(f"non-source file in module overlay: {source.relative_to(ROOT)}")
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(source, target)


def prepare(module, base):
    revision = subprocess.check_output(
        ["git", "rev-parse", "--verify", f"{base}^{{commit}}"], cwd=ROOT, text=True).strip()
    parent = ROOT / ".local/backend-check" / module
    parent.mkdir(parents=True, exist_ok=True)
    target = Path(tempfile.mkdtemp(prefix="run-", dir=parent))
    archive = subprocess.check_output(["git", "archive", revision], cwd=ROOT)
    with tarfile.open(fileobj=io.BytesIO(archive)) as snapshot:
        snapshot.extractall(target, filter="data")
    for rel in ["internal/secretstore/secretstore.go", "internal/adminauth/adminauth.go",
                f"{STORAGE}/database.go"]:
        if not (target / rel).is_file():
            raise SystemExit("baseline must contain the frozen I00 interfaces")
    versions = sorted(p.name[:3] for p in (target / STORAGE / "migrations").glob("*.sql"))
    if versions != ["001", "002"]:
        raise SystemExit("stage-1 validation requires the pre-I01 baseline (001/002 only)")
    copied = []
    for rel in OWNED[module]:
        source, dest = ROOT / rel, target / rel
        # Removing the baseline's owned subtree also reflects module deletions.
        if dest.is_dir():
            shutil.rmtree(dest)
        elif dest.exists():
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
    for source in sorted((ROOT / STORAGE / "queries").glob(f"{module.lower()}_*.sql")):
        overlay_file(source, target / source.relative_to(ROOT))
        copied.append(str(source.relative_to(ROOT)))
    if module == "B02":
        for source in sorted((ROOT / STORAGE).glob("b02_*_test.go")):
            overlay_file(source, target / source.relative_to(ROOT))
            copied.append(str(source.relative_to(ROOT)))
    migrations = target / STORAGE / "migrations"
    slot = {"B01": 3, "B02": 4, "B03": 5}[module]
    # Earlier stage-1 slots are scratch-only no-ops, never sibling proposals.
    for n in range(3, slot):
        (migrations / f"{n:03}_scratch_placeholder.sql").write_text(
            "-- Isolated validation only: sibling migration intentionally absent.\nSELECT 1;\n")
    proposal = ROOT / STORAGE / "proposals" / f"{module}.sql"
    dest = migrations / f"{slot:03}_{module.lower()}.sql"
    if proposal.exists():
        overlay_file(proposal, dest)
        copied.append(str(proposal.relative_to(ROOT)))
    else:
        dest.write_text("-- This module has no migration proposal yet.\nSELECT 1;\n")
    depfile = ROOT / "documents/backend-delivery" / f"{module}.deps"
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
                "note": "isolated module validation, NOT integrated validation"}
    import json
    (target / "VALIDATION.json").write_text(json.dumps(manifest, indent=2) + "\n")
    print(f"Isolated {module} snapshot: {target}\nBaseline: {revision}", flush=True)
    return target, deps


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("module", choices=OWNED)
    parser.add_argument("--base", required=True, help="exact I00 commit assigned by coordinator")
    parser.add_argument("--prepare-only", action="store_true")
    args = parser.parse_args()
    # Preparation and compilation both share one cross-process lock. Never run
    # go/sqlc directly while another developer owns this lock.
    cache = ROOT / ".local/cache"
    cache.mkdir(parents=True, exist_ok=True)
    with (cache / "backend-check.lock").open("w") as lock:
        print("Waiting for backend-check lock", flush=True)
        fcntl.flock(lock, fcntl.LOCK_EX)
        target, deps = prepare(args.module, args.base)
        if args.prepare_only:
            return
        env = dict(os.environ, GOMAXPROCS="1", CGO_ENABLED="0",
                   GOCACHE=str(cache / "go"), GOTMPDIR=str(cache / "go-tmp"))
        Path(env["GOTMPDIR"]).mkdir(parents=True, exist_ok=True)
        for dep in deps:
            module, version = dep.rsplit("@", 1)
            run(["go", "mod", "edit", f"-require={module}@{version}"], target, env)
        if deps:
            run(["go", "mod", "tidy"], target, env)
        run(["sqlc", "generate"], target, env)
        packages = PACKAGES[args.module].copy()
        missing = [p for p in packages if not any((target / p[2:-4]).rglob("*.go"))]
        if missing:
            raise SystemExit("module source incomplete; not a validation pass: " + ", ".join(missing))
        packages += ["./controller/internal/storage", "./controller/api"]
        run(["go", "test", "-p", "1", *packages], target, env)
        run(["go", "vet", "-p", "1", *packages], target, env)
        print("PASS: isolated module checks only; I01 integration still required", flush=True)


if __name__ == "__main__":
    main()
