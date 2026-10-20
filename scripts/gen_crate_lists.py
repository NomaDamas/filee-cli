#!/usr/bin/env python3
"""Regenerates data/{rhwp,unhwp}-crates.tsv: the Rust crates inside the two native components, with the license each
crate declares and its Cargo.lock checksum. Run it when data/pins.json changes (docs/updating.md). Needs git and cargo.

The lists are the transitive normal (non-dev, non-build) dependencies for all targets, so they are a superset of what
one platform links. libunhwp is the Unhwp NuGet package's native library: built from the commit its nuspec names, with
the ffi feature. rhwp is built from its release tag with `cargo build --release --locked --bin rhwp`.
"""

import re
import sys
import tempfile
from pathlib import Path

from common import DATA, BuildError, fail, load_json, run


def lock_checksums(lock: Path) -> dict:
    checksums, name, version = {}, None, None
    for line in lock.read_text(encoding="utf-8").splitlines():
        if line == "[[package]]":
            name = version = None
        elif m := re.match(r'name = "(.*)"', line):
            name = m.group(1)
        elif m := re.match(r'version = "(.*)"', line):
            version = m.group(1)
        elif m := re.match(r'checksum = "(.*)"', line):
            checksums[(name, version)] = m.group(1)
    return checksums


def crate_list(source: Path, package: str, extra: list, label: str, skip: set) -> list:
    output = run(["cargo", "tree", "--locked", "--target", "all", "-e", "normal", "--prefix", "none", "-p", package,
                  *extra, "--format", "{p}|{l}"], cwd=source, capture=True)
    checksums = lock_checksums(source / "Cargo.lock")
    rows = set()
    for line in output.splitlines():
        if "|" not in line:
            continue
        package_part, _, license_part = line.partition("|")
        match = re.match(r"(\S+) v(\S+)", package_part)
        if not match or match.group(1) in skip:
            continue
        name, version = match.groups()
        if re.search(r"\((/|[A-Za-z]:\\)", package_part):
            continue  # a workspace member of the component itself (path dependency): covered by the component's own license
        license_expression = re.sub(r" \(\*\)$", "", license_part).strip().replace(" / ", " OR ").replace("/", " OR ")
        if not license_expression:
            raise BuildError(f"{label}: crate {name} {version} declares no license")
        checksum = checksums.get((name, version))
        if not checksum:
            raise BuildError(f"{label}: no Cargo.lock checksum for {name} {version} (path or git dependency?)")
        rows.add((name, version, license_expression, checksum))
    return sorted(rows)


def clone(url: str, ref: str, destination: Path) -> None:
    run(["git", "init", "-q", destination])
    run(["git", "-C", destination, "fetch", "-q", "--depth", "1", url, ref])
    run(["git", "-C", destination, "checkout", "-q", "--detach", "FETCH_HEAD"])


def main() -> int:
    try:
        pins = load_json(DATA / "pins.json")
        with tempfile.TemporaryDirectory() as tmp:
            for key, package, extra in (("rhwp", "rhwp", []), ("unhwp", "unhwp", ["--features", "ffi"])):
                pin = pins[key]
                source = Path(tmp) / key
                clone(pin["repository"], pin["commit"], source)
                rows = crate_list(source, package, extra, key, skip={package})
                target = DATA / f"{key}-crates.tsv"
                with open(target, "w", encoding="utf-8", newline="\n") as stream:
                    stream.write(f"# {key} {pin['version']} at {pin['commit']}: name<TAB>version<TAB>license<TAB>Cargo.lock sha256\n")
                    for row in rows:
                        stream.write("\t".join(row) + "\n")
                print(f"{target}: {len(rows)} crates")
        return 0
    except BuildError as error:
        return fail(error)


if __name__ == "__main__":
    sys.exit(main())
