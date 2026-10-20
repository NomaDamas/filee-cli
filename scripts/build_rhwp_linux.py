#!/usr/bin/env python3
"""Builds the Linux rhwp program from source on Ubuntu 22.04, so it runs on glibc 2.35 (rhwp's own Linux release needs
a newer glibc). The pinned commit, Rust toolchain and build steps come from upstream's build/build-linux-native.py in
the checked-out upstream tree (scripts/fetch_upstream.py), not from a copy kept here.

Native host (x64 on x64, arm64 on arm64): upstream's own build function runs unchanged.
Cross build (arm64 target on an x64 host, needed because GitHub's free plan has no private arm64 runners): the same
commit, toolchain and `cargo build --release --locked --bin rhwp`, plus `--target aarch64-unknown-linux-gnu` linked
with gcc-aarch64-linux-gnu. Both variants write the same folder layout as rhwp's release archive.

Usage: build_rhwp_linux.py --rid linux-x64|linux-arm64 --output DIR [--work DIR]
"""

import argparse
import importlib.util
import os
import platform
import shutil
import sys
import tempfile
from pathlib import Path

from common import UPSTREAM_DIR, BuildError, fail, run

TRIPLES = {"linux-x64": "x86_64-unknown-linux-gnu", "linux-arm64": "aarch64-unknown-linux-gnu"}
HOST_RID = {"x86_64": "linux-x64", "AMD64": "linux-x64", "aarch64": "linux-arm64", "arm64": "linux-arm64"}


def load_upstream_module():
    path = UPSTREAM_DIR / "build" / "build-linux-native.py"
    if not path.is_file():
        raise BuildError(f"{path} is missing; run scripts/fetch_upstream.py first")
    spec = importlib.util.spec_from_file_location("upstream_build_linux_native", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def cross_build(upstream, rid: str, output: Path, work: Path) -> None:
    triple = TRIPLES[rid]
    upstream.verify_rhwp_tag()
    source = work / "rhwp"
    upstream.fetch(upstream.RHWP_REPOSITORY, upstream.RHWP_COMMIT, str(source))
    toolchain = upstream.RUST_TOOLCHAIN
    run(["rustup", "toolchain", "install", toolchain, "--profile", "minimal", "--no-self-update",
         "--target", triple])
    linker_variable = "CARGO_TARGET_" + triple.upper().replace("-", "_") + "_LINKER"
    env = dict(os.environ, CARGO_TARGET_DIR=str(work / "rhwp-target"),
               **{linker_variable: "aarch64-linux-gnu-gcc", "CC_" + triple.replace("-", "_"): "aarch64-linux-gnu-gcc"})
    run(["cargo", f"+{toolchain}", "build", "--release", "--locked", "--bin", "rhwp", "--target", triple],
        cwd=source, env=env)
    program = work / "rhwp-target" / triple / "release" / "rhwp"
    output.mkdir(parents=True, exist_ok=True)
    shutil.copy2(program, output / "rhwp")
    for name in ("LICENSE", "README.md", "README_EN.md"):
        if (source / name).is_file():
            shutil.copy2(source / name, output / name)
    upstream.write_source_note(
        str(output), "rhwp", upstream.RHWP_REPOSITORY, upstream.RHWP_COMMIT,
        f"v{upstream.RHWP_VERSION}; cargo +{toolchain} build --release --locked --bin rhwp --target {triple}; "
        f"cross-compiled on {platform.machine()}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--rid", required=True, choices=sorted(TRIPLES))
    parser.add_argument("--output", required=True, help="folder to create (must not exist)")
    parser.add_argument("--work", help="scratch folder (default: a temporary one)")
    options = parser.parse_args()
    try:
        if not sys.platform.startswith("linux"):
            raise BuildError("rhwp for Linux is built on Linux")
        output = Path(options.output).resolve()
        if output.exists():
            raise BuildError(f"{output} already exists")
        work = Path(options.work).resolve() if options.work else Path(tempfile.mkdtemp(prefix="filee-rhwp-"))
        work.mkdir(parents=True, exist_ok=True)
        upstream = load_upstream_module()
        upstream.check()
        if HOST_RID.get(platform.machine()) == options.rid:
            upstream.build_rhwp(str(output), str(work))
        else:
            cross_build(upstream, options.rid, output, work)
        print(f"rhwp ready in {output}: {', '.join(sorted(p.name for p in output.iterdir()))}")
        return 0
    except BuildError as error:
        return fail(error)
    except Exception as error:  # upstream's BuildError is a different class
        return fail(error)


if __name__ == "__main__":
    sys.exit(main())
