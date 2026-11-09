"""Generate sqlc output in isolation and compare without rewriting source files."""

from pathlib import Path
import shutil
import subprocess
import tempfile


root = Path(__file__).resolve().parents[1]
with tempfile.TemporaryDirectory(prefix="meshstrata-sqlc-") as scratch:
    target = Path(scratch)
    shutil.copy2(root / "sqlc.yaml", target / "sqlc.yaml")
    storage = target / "internal/storage"
    storage.mkdir(parents=True)
    shutil.copytree(root / "internal/storage/migrations", storage / "migrations")
    shutil.copy2(root / "internal/storage/queries.sql", storage / "queries.sql")
    subprocess.run(["sqlc", "generate"], cwd=target, check=True)
    actual = root / "internal/storage/dbgen"
    expected = storage / "dbgen"
    read = lambda directory: {p.name: p.read_bytes() for p in directory.glob("*.go")}
    if read(actual) != read(expected):
        raise SystemExit("sqlc output is out of date; run make generate")
print("sqlc output is up to date")
