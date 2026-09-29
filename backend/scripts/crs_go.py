#!/usr/bin/env python3
"""Run CRS Go builds/tests with the verified, read-only mihomo log overlay."""

import hashlib
import json
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import sys
import tempfile

BACKEND = Path(__file__).resolve().parents[1]
PATCH_DIR = BACKEND / "patches" / "mihomo-v1.19.31"


def verify_file(path, expected):
    actual = hashlib.sha256(path.read_bytes()).hexdigest()
    if actual != expected:
        raise ValueError(f"SHA256 mismatch for {path}: expected {expected}, got {actual}")


def overlay_files(go, temporary):
    manifest = json.loads((PATCH_DIR / "manifest.json").read_text())
    module = json.loads(subprocess.check_output(
        [go, "list", "-m", "-json", manifest["module"]], cwd=BACKEND, text=True))
    if module.get("Version") != manifest["version"] or module.get("Replace"):
        raise ValueError("mihomo version/replacement differs from the reviewed overlay")
    original = Path(module["Dir"]) / "log" / "log.go"
    replacement = PATCH_DIR / manifest["replacement_file"]
    verify_file(original, manifest["original_sha256"])
    verify_file(replacement, manifest["replacement_sha256"])
    verify_file(PATCH_DIR / "log.go.patch", manifest["patch_sha256"])
    # Go forbids overlays beneath GOMODCACHE. Isolate this one module instead
    # of modifying the shared cache or vendoring the dependency graph.
    isolated = temporary / "mihomo"
    shutil.copytree(module["Dir"], isolated, copy_function=shutil.copyfile)
    for directory, _, _ in os.walk(isolated):
        Path(directory).chmod(0o700)
    modfile = temporary / "crs.mod"
    modfile.write_text((BACKEND / "go.mod").read_text() +
                       f'\nreplace {manifest["module"]} => {json.dumps(str(isolated))}\n')
    shutil.copyfile(BACKEND / "go.sum", temporary / "crs.sum")
    return {"Replace": {str(isolated / "log" / "log.go"): str(replacement)}}, modfile


def main(args):
    if not args or args[0] not in {"build", "test", "vet"}:
        raise ValueError("usage: crs_go.py {build|test|vet} [go arguments]")
    flags = args[1:] + shlex.split(os.environ.get("GOFLAGS", ""))
    if any(flag.split("=", 1)[0] in {"-overlay", "-modfile"} for flag in flags):
        raise ValueError("an additional Go overlay/modfile would bypass the reviewed CRS patch")
    go = os.environ.get("CRS_GO", "go")
    with tempfile.TemporaryDirectory(prefix="crs-go-overlay-") as temporary:
        directory = Path(temporary)
        overlay, modfile = overlay_files(go, directory)
        path = directory / "overlay.json"
        path.write_text(json.dumps(overlay))
        return subprocess.run([go, args[0], "-modfile", str(modfile), "-overlay", str(path), *args[1:]], cwd=BACKEND).returncode


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv[1:]))
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print(f"CRS Go wrapper: {error}", file=sys.stderr)
        sys.exit(1)
