#!/usr/bin/env python3
"""Builds one release archive: publishes Filee.Cli for a RID, adds the engines, writes notices, checks that no GUI
file slipped in, and creates dist/filee-cli-<version>-<rid>.tar.gz (zip for Windows).

Needs the pinned upstream checkout (scripts/fetch_upstream.py), the .NET SDK, and for macOS the codesign tool.

Usage: build_package.py --rid RID [--rhwp-dir DIR] [--dist DIR] [--codesign-identity ID]
  --rhwp-dir   a folder with rhwp built for this RID (scripts/build_rhwp_linux.py); required for linux-*, optional
               elsewhere (the release download pinned in upstream's engines.json is used otherwise)
"""

import argparse
import datetime
import hashlib
import io
import json
import os
import platform
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
import zipfile
from pathlib import Path

import generate_notices
from common import (DATA, RIDS, ROOT, UPSTREAM_DIR, BuildError, archive_name, download, extract_tar, fail, load_json, package_name,
                    read_upstream, rmtree, run, sha256_file)

CACHE = ROOT / ".cache"
WORK = ROOT / "work"

# Names that mean a GUI, tray, shell-integration or installer file. The package must contain none of them.
FORBIDDEN = ("avalonia", "sharphook", "uiohook", "communitytoolkit", "filee.app", "filee.exe", "filee.dll",
             "filee.platform", "fileeexplorermenu", "setup", "installer", "install.sh", "uninstall", ".app/", ".msi",
             "inno", "materialdesign", "simpleicons", "notosans")
ALLOWED_FILEE = {"filee-cli", "filee-cli.exe", "filee-cli.dll", "filee-cli.deps.json", "filee-cli.runtimeconfig.json",
                 "filee.core.dll", "filee.engines.dll"}


def dotnet_version() -> str:
    return run(["dotnet", "--version"], cwd=UPSTREAM_DIR, capture=True).strip()


def publish(rid: str, version: str, destination: Path) -> None:
    rmtree(destination)
    run(["dotnet", "publish", UPSTREAM_DIR / "src" / "Filee.Cli" / "Filee.Cli.csproj", "-c", "Release", "-r", rid,
         "--self-contained", "true", f"-p:Version={version}", "-p:FileeRelease=true", "-p:PublishReadyToRun=false",
         "-o", destination], cwd=UPSTREAM_DIR)  # cwd: upstream's global.json selects the SDK
    # Debug symbols and XML documentation are not needed to run (libSkiaSharp.pdb alone is 84 MB on Windows).
    for pattern in ("*.pdb", "*.xml"):
        for path in destination.rglob(pattern):
            path.unlink()


def extract_archive(archive: Path, destination: Path) -> None:
    """Extracts a .zip/.tar.gz/.tar.xz, refusing absolute paths, '..' and links."""
    destination.mkdir(parents=True, exist_ok=True)
    root = destination.resolve()
    if archive.name.endswith(".zip"):
        with zipfile.ZipFile(archive) as z:
            for name in z.namelist():
                if not (root / name).resolve().is_relative_to(root):
                    raise BuildError(f"Unsafe archive member: {name}")
            z.extractall(destination)
        return
    with tarfile.open(archive) as tar:
        for member in tar.getmembers():
            if not (root / member.name).resolve().is_relative_to(root) or not (member.isfile() or member.isdir()):
                raise BuildError(f"Unsafe archive member: {member.name} (links and special files are not allowed)")
    extract_tar(archive, destination)


def sole_folder(path: Path) -> Path:
    children = list(path.iterdir())
    return children[0] if len(children) == 1 and children[0].is_dir() else path


def fetch_engine(name: str, component: dict, scratch: Path) -> Path:
    """Downloads one pinned engine (SHA-256 checked) and returns the folder holding its files."""
    archive = download(component["url"], component["sha256"], CACHE / "downloads")
    suffix = component["kind"]
    named = scratch / f"{name}.{suffix}"
    shutil.copy2(archive, named)
    target = scratch / f"{name}-x"
    if suffix == "msi":
        if sys.platform != "win32":
            raise BuildError("The Windows 7-Zip MSI is unpacked with msiexec: build win-x64 on Windows")
        run(["msiexec.exe", "/a", str(named), "/qn", f"TARGETDIR={target}"])
        return target
    extract_archive(named, target)
    return sole_folder(target)


def add_engines(rid: str, package: Path, rhwp_dir: Path | None, scratch: Path) -> dict:
    manifest_path = UPSTREAM_DIR / "src" / "Filee.Engines" / "Infrastructure" / "engines.json"
    manifest = load_json(manifest_path)
    components = manifest["platforms"][rid]["components"] if rid in manifest["platforms"] else manifest["components"]
    pins = load_json(DATA / "pins.json")
    windows = rid.startswith("win-")
    engines = package / "engines"
    engines.mkdir()
    info = {"engines_json": str(manifest_path)}

    # rhwp
    rhwp = components["rhwp"]
    if rhwp["version"] != pins["rhwp"]["version"]:
        raise BuildError(f"upstream engines.json pins rhwp {rhwp['version']} but data/pins.json describes {pins['rhwp']['version']}; "
                         "update data/pins.json and the crate list (docs/updating.md)")
    rhwp_target = engines / "rhwp"
    if rhwp_dir is not None:
        if not (rhwp_dir / "rhwp").is_file():
            raise BuildError(f"{rhwp_dir} has no rhwp program")
        shutil.copytree(rhwp_dir, rhwp_target)
        info["rhwp"] = {"url": "built from source (scripts/build_rhwp_linux.py)"}
    elif rid.startswith("linux-"):
        raise BuildError("linux-* packages need --rhwp-dir (rhwp's Linux release needs a newer glibc than Ubuntu 22.04)")
    else:
        shutil.copytree(fetch_engine("rhwp", rhwp, scratch), rhwp_target)
        info["rhwp"] = {"url": rhwp["url"], "sha256": rhwp["sha256"]}
    program = rhwp_target / ("rhwp.exe" if windows else "rhwp")
    if not program.is_file():
        raise BuildError(f"{program} is missing")

    # 7-Zip
    zip_component = components["7zip"]
    if zip_component["version"] != pins["7zip"]["version"]:
        raise BuildError(f"upstream engines.json pins 7-Zip {zip_component['version']} but data/pins.json describes {pins['7zip']['version']}")
    source = fetch_engine("7zip", zip_component, scratch)
    zip_target = engines / "7zip"
    zip_target.mkdir()
    keep = ("7z.exe", "7z.dll", "License.txt", "readme.txt") if windows else ("7zz", "License.txt", "readme.txt")
    found = {p.name: p for p in source.rglob("*") if p.is_file() and p.name in keep}
    missing = [k for k in keep if k not in found]
    if missing:
        raise BuildError(f"7-Zip archive lacks {missing}")
    for name in keep:
        shutil.copy2(found[name], zip_target / name)
    info["7zip"] = {"url": zip_component["url"], "sha256": zip_component["sha256"]}

    if not windows:
        for path in (program, zip_target / "7zz", package / "filee-cli"):
            path.chmod(path.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)
    return info


def codesign_macos(package: Path, identity: str) -> None:
    """Every Mach-O file is code to sign (the .NET assemblies too: their signature goes into extended attributes)."""
    options = ["--force", "--sign", identity]
    if identity != "-":
        options += ["--timestamp", "--options", "runtime"]
    entitlements = ROOT / "data" / "entitlements.plist"
    files = [p for p in package.rglob("*") if p.is_file() and not p.is_symlink()]
    files.sort(key=lambda p: len(str(p)), reverse=True)
    main = package / "filee-cli"
    for path in files:
        if path == main or path.suffix in (".md", ".txt", ".json") or "licenses" in path.relative_to(package).parts:
            continue
        run(["codesign", *options, "--entitlements", entitlements, path])
    run(["codesign", *options, "--entitlements", entitlements, main])
    run(["codesign", "--verify", "--strict", main])


def check_no_gui(package: Path) -> list:
    """Fails when a GUI, tray, installer or shell-integration file is in the package; returns the file listing."""
    listing = []
    offenders = []
    for path in sorted(package.rglob("*")):
        relative = path.relative_to(package).as_posix()
        if path.is_dir():
            continue
        listing.append(relative)
        lowered = relative.lower()
        if lowered.startswith("licenses/") or lowered in ("third-party-notices.md", "upstream.md", "readme.md"):
            continue  # texts that talk about those components
        base = os.path.basename(lowered)
        if base.startswith("filee") and base not in ALLOWED_FILEE:
            offenders.append(relative)
        elif any(word in lowered for word in FORBIDDEN) and not lowered.startswith("engines/"):
            offenders.append(relative)
    if offenders:
        raise BuildError("GUI, tray, installer or shell-integration files in the package:\n  " + "\n  ".join(offenders))
    return listing


def write_upstream_md(package: Path, rid: str, up: dict, info: dict, sdk: str, built: str, rhwp_note: str) -> None:
    patches = sorted(p.name for p in (ROOT / "patches").glob("*.patch"))
    patch_text = "\n".join(f"- `{p}` (sha256 {sha256_file(ROOT / 'patches' / p)})" for p in patches) or "- none (built from the unmodified upstream source)"
    ci = ""
    if os.environ.get("GITHUB_ACTIONS"):
        ci = (f"\n## Build\n\n- Workflow run: {os.environ.get('GITHUB_SERVER_URL')}/{os.environ.get('GITHUB_REPOSITORY')}/actions/runs/{os.environ.get('GITHUB_RUN_ID')}\n"
              f"- This repository at: `{os.environ.get('GITHUB_SHA')}`\n- Runner: {os.environ.get('RUNNER_OS')} {os.environ.get('RUNNER_ARCH')}\n")
    text = f"""# filee-cli {up['version']} ({rid})

**Unofficial redistribution** of the command line of Filee. Not made, reviewed or endorsed by the Filee author.

## Corresponding source

- Upstream: {up['repository']}
- Tag: `{up['tag']}`
- Commit: `{up['commit']}` ({up['repository']}/tree/{up['commit']})
- Projects built: `src/Filee.Cli` with `Filee.Core` and `Filee.Engines`. Nothing else of upstream is built or included.
- Patches applied to the upstream source:
{patch_text}
- Packaging revision: {up['revision']} (package version {up['version']} = upstream {up['upstream_version']} + packaging revision)
- Build and packaging scripts: this repository (see `README.md` there)

## Build

- Built (UTC): {built}
- .NET SDK: {sdk}
- Command: `dotnet publish src/Filee.Cli/Filee.Cli.csproj -c Release -r {rid} --self-contained true -p:Version={up['upstream_version']} -p:FileeRelease=true -p:PublishReadyToRun=false`
- After publishing, `*.pdb` and `*.xml` documentation files were deleted; nothing else was changed.
- Engines: rhwp ({rhwp_note}), 7-Zip ({info['7zip']['url']}, sha256 {info['7zip']['sha256']}).
{ci}"""
    (package / "UPSTREAM.md").write_text(text, encoding="utf-8", newline="\n")


PACKAGE_README = """# filee-cli {version} ({rid})

Unofficial redistribution of the command line of [Filee](https://github.com/KnifeLemon/Filee) (MIT). It contains no
GUI, tray, Explorer menu or installer. See `UPSTREAM.md` for the exact upstream commit, `LICENSE` and
`THIRD-PARTY-NOTICES.md` for licenses.

Run `{exe} version`, `{exe} formats`, `{exe} convert photo.png --to webp --json`. Put this folder (or a link to
`{exe}`) on your `PATH` to use it from other programs. The `engines/` folder must stay next to the program.

Larger engines (FFmpeg, Pandoc, LibreOffice, Ghostscript, calibre) are not included; if you install them yourself and
they are on your `PATH`, `{exe}` finds them.
{linux}"""

LINUX_NOTE = """
Linux needs the system libraries .NET itself needs, e.g. on Ubuntu 22.04:
`sudo apt-get install -y ca-certificates libc6 libgcc-s1 libicu70 libssl3 libstdc++6 tzdata zlib1g`
"""


def write_archive(package: Path, destination: Path, rid: str, epoch: int) -> None:
    top = package.name
    files = sorted(p for p in package.rglob("*") if p.is_file() or p.is_dir())
    if rid.startswith("win-"):
        with zipfile.ZipFile(destination, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
            for path in files:
                if path.is_file():
                    info = zipfile.ZipInfo(f"{top}/{path.relative_to(package).as_posix()}", date_time=datetime.datetime.fromtimestamp(epoch, datetime.timezone.utc).timetuple()[:6])
                    info.compress_type = zipfile.ZIP_DEFLATED
                    info.external_attr = (0o755 if os.access(path, os.X_OK) else 0o644) << 16
                    archive.writestr(info, path.read_bytes())
        return

    def normalize(info: tarfile.TarInfo) -> tarfile.TarInfo:
        info.uid = info.gid = 0
        info.uname = info.gname = ""
        info.mtime = epoch
        info.mode = 0o755 if (info.isdir() or info.mode & 0o111) else 0o644
        return info

    with tarfile.open(destination, "w:gz", compresslevel=9) as tar:
        tar.add(package, arcname=top, recursive=False, filter=normalize)
        for path in files:
            tar.add(path, arcname=f"{top}/{path.relative_to(package).as_posix()}", recursive=False, filter=normalize)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--rid", required=True, choices=RIDS)
    parser.add_argument("--rhwp-dir", type=Path)
    parser.add_argument("--dist", type=Path, default=ROOT / "dist")
    parser.add_argument("--codesign-identity", default="-", help="macOS signing identity ('-' = ad-hoc)")
    options = parser.parse_args()
    rid = options.rid
    try:
        up = read_upstream()
        if not (UPSTREAM_DIR / "Filee.slnx").is_file():
            raise BuildError("Run scripts/fetch_upstream.py first")
        if rid.startswith("osx-") != (sys.platform == "darwin") and rid.startswith("osx-"):
            raise BuildError("Build macOS packages on macOS (codesign)")
        epoch = int(os.environ.get("SOURCE_DATE_EPOCH") or run(["git", "-C", UPSTREAM_DIR, "log", "-1", "--format=%ct"], capture=True).strip())
        built = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
        name = package_name(rid)
        stage_root = WORK / rid
        rmtree(stage_root)
        scratch = stage_root / "scratch"
        scratch.mkdir(parents=True)
        package = stage_root / name
        publish_dir = stage_root / "publish"

        publish(rid, up["upstream_version"], publish_dir)
        shutil.copytree(publish_dir, package, symlinks=True)
        info = add_engines(rid, package, options.rhwp_dir, scratch)

        shutil.copy2(ROOT / "LICENSE", package / "LICENSE")
        generate_notices.generate(package, publish_dir, rid, info)
        exe = "filee-cli.exe" if rid.startswith("win-") else "filee-cli"
        (package / "README.md").write_text(PACKAGE_README.format(version=up["version"], rid=rid, exe=exe, linux=LINUX_NOTE if rid.startswith("linux-") else ""),
                                           encoding="utf-8", newline="\n")
        pins = load_json(DATA / "pins.json")
        rhwp_note = (f"built from source, tag v{pins['rhwp']['version']} commit {pins['rhwp']['commit']}, cargo +{pins['rhwp']['rust_toolchain']} build --release --locked --bin rhwp"
                     if options.rhwp_dir else f"release build {info['rhwp']['url']}, sha256 {info['rhwp']['sha256']}")
        write_upstream_md(package, rid, up, info, dotnet_version(), built, rhwp_note)

        if rid.startswith("osx-"):
            codesign_macos(package, options.codesign_identity)
        listing = check_no_gui(package)

        options.dist.mkdir(parents=True, exist_ok=True)
        archive = options.dist / archive_name(rid)
        if archive.exists():
            archive.unlink()
        write_archive(package, archive, rid, epoch)
        (options.dist / f"{name}.files.txt").write_text("\n".join(listing) + "\n", encoding="utf-8")
        size = archive.stat().st_size
        print(f"{archive} ({size / 1e6:.1f} MB), {len(listing)} files, sha256 {sha256_file(archive)}")
        return 0
    except BuildError as error:
        return fail(error)


if __name__ == "__main__":
    sys.exit(main())
