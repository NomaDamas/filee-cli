#!/usr/bin/env python3
"""Generates THIRD-PARTY-NOTICES.md and licenses/ for one package from what is actually in it.

Inputs, all machine-read (nothing is copied from upstream's THIRD-PARTY-NOTICES.md):
  * the dotnet publish folder and its filee-cli.deps.json  -> which NuGet packages and runtime files are in the output
  * the NuGet cache (nuspec + nupkg)                       -> license, copyright, source repository/commit, license files
  * data/*.json, data/*.tsv                                -> source pins for components with no NuGet source info
  * Magick.NET's Notice.txt                                -> the list of ImageMagick delegate libraries (checked
                                                              against data/delegates.json: any difference fails)
  * the pinned pdfium-binaries archive                     -> pdfium license files; its library is compared byte for
                                                              byte with the one in the package
  * crates.io .crate files (SHA-256 = Cargo.lock)          -> license files of the Rust crates in libunhwp and rhwp

The generator fails (non-zero exit) when it meets a file in the package it cannot attribute to a component, a package
without a resolvable license or source, a delegate library missing from data/delegates.json, or a checksum mismatch.
"""

import fnmatch
import io
import json
import os
import re
import shutil
import sys
import tarfile
import xml.etree.ElementTree as ET
import zipfile
from pathlib import Path

from common import DATA, ROOT, BuildError, download, fail, load_json, read_upstream, rmtree, sha256_file

CACHE = ROOT / ".cache"
LICENSE_FILE_PATTERN = re.compile(r"^(licen[cs]e|copying|unlicense|notice|copyright)([-_.].*)?$", re.IGNORECASE)
NATIVE_SUFFIXES = (".dll", ".so", ".dylib")

# Files in the publish folder that are .NET host plumbing or Filee itself, not third-party components.
FILEE_FILES = {"filee-cli", "filee-cli.exe", "filee-cli.dll", "filee-cli.deps.json", "filee-cli.runtimeconfig.json"}

# Standard-library ids used in license expressions that carry no extra text to ship.
EXPRESSION_WORDS = {"AND", "OR", "WITH"}


def nuget_root() -> Path:
    return Path(os.environ.get("NUGET_PACKAGES") or Path.home() / ".nuget" / "packages")


# ---------------------------------------------------------------------------------------------------------------------
# What is in the publish folder
# ---------------------------------------------------------------------------------------------------------------------

def package_files(deps: dict) -> tuple[dict, dict]:
    """(library key -> list of asset basenames, basename -> library key) from the RID's target in deps.json."""
    targets = deps["targets"]
    target = targets[[k for k in targets if "/" in k][0]]
    by_library, by_file = {}, {}
    for key, entry in target.items():
        names = []
        for section in ("runtime", "native", "resources"):
            for path in entry.get(section, {}):
                names.append(path)
        by_library[key] = names
        for path in names:
            by_file.setdefault(os.path.basename(path), key)
    return by_library, by_file


def files_in(publish: Path) -> set:
    found = set()
    for path in publish.rglob("*"):
        if path.is_file():
            found.add(path.relative_to(publish).as_posix())
    return found


def classify(publish: Path, deps: dict) -> dict:
    """Maps every file of the publish folder to its component and returns the components present in the output."""
    by_library, by_file = package_files(deps)
    present, unmapped = {}, []
    for relative in sorted(files_in(publish)):
        name = os.path.basename(relative)
        if name in FILEE_FILES or relative.endswith(".pdb"):
            continue
        library = by_file.get(name)
        if library is None:
            if name.endswith((".json", ".txt", ".md")):
                continue
            if name == "createdump" or name == "createdump.exe":  # part of the .NET runtime pack
                library = next((k for k in by_library if k.startswith("runtimepack.")), None)
            if library is None:
                unmapped.append(relative)
                continue
        present.setdefault(library, []).append(relative)
    native_unmapped = [u for u in unmapped if u.endswith(NATIVE_SUFFIXES) or "." not in os.path.basename(u)]
    if unmapped:
        print("Files not attributed to a library by deps.json:", *unmapped, sep="\n  ")
    if native_unmapped:
        raise BuildError("Executable code in the package that deps.json does not account for: " + ", ".join(native_unmapped))
    return present


# ---------------------------------------------------------------------------------------------------------------------
# NuGet metadata
# ---------------------------------------------------------------------------------------------------------------------

def read_nuspec(name: str, version: str) -> dict:
    folder = nuget_root() / name.lower() / version
    specs = list(folder.glob("*.nuspec"))
    if not specs:
        raise BuildError(f"{name} {version}: no nuspec in {folder} (restore first)")
    root = ET.parse(specs[0]).getroot()
    namespace = re.match(r"\{.*\}", root.tag).group(0) if root.tag.startswith("{") else ""
    metadata = root.find(f"{namespace}metadata")

    def text(tag):
        element = metadata.find(f"{namespace}{tag}")
        return element.text.strip() if element is not None and element.text else ""

    license_element = metadata.find(f"{namespace}license")
    repository = metadata.find(f"{namespace}repository")
    return {
        "license": license_element.text.strip() if license_element is not None and license_element.get("type") == "expression" else "",
        "license_file": license_element.text.strip() if license_element is not None and license_element.get("type") == "file" else "",
        "copyright": text("copyright"),
        "authors": text("authors"),
        "project_url": text("projectUrl"),
        "repo_url": repository.get("url", "") if repository is not None else "",
        "repo_commit": repository.get("commit", "") if repository is not None else "",
        "nupkg": next(iter(folder.glob("*.nupkg"))),
    }


def normalize_repo(url: str) -> str:
    url = url.strip()
    url = re.sub(r"^git://", "https://", url)
    url = re.sub(r"\.git$", "", url)
    return url.rstrip("/")


def nuget_component(key: str, sources: dict) -> dict:
    name, version = key.split("/")
    meta = read_nuspec(name, version)
    repo = meta["repo_url"]
    for pattern, override in sources["repository_override"].items():
        if fnmatch.fnmatch(name, pattern):
            repo = override
            break
    if not repo:
        repo = meta["project_url"]
    ref = meta["repo_commit"]
    note = ""
    override = sources["ref_override"].get(name)
    if not ref and override:
        ref, note = override["ref"], override["note"]
    if "github.com" not in repo and "gitlab" not in repo and "googlesource" not in repo:
        raise BuildError(f"{key}: no source repository URL (nuspec repository/projectUrl: {meta['repo_url']!r}, {meta['project_url']!r})")
    if not ref:
        raise BuildError(f"{key}: no source commit in the nuspec; add it to data/nuget-sources.json ref_override")
    license_expression = sources["license_override"].get(name) or meta["license"]
    if not license_expression:
        raise BuildError(f"{key}: no license expression in the nuspec; add it to data/nuget-sources.json license_override")
    return {
        "name": name, "version": version, "license": license_expression, "copyright": meta["copyright"] or meta["authors"],
        "repo": normalize_repo(repo), "ref": ref, "ref_note": note, "nupkg": meta["nupkg"],
    }


def nupkg_license_files(component: dict) -> list:
    """(member name, bytes) of license and notice files at the top of a package, or its docs/ folder."""
    members = []
    with zipfile.ZipFile(component["nupkg"]) as archive:
        for member in archive.namelist():
            base = os.path.basename(member)
            parent = os.path.dirname(member)
            if member.endswith("/") or parent not in ("", "docs"):
                continue
            if LICENSE_FILE_PATTERN.match(os.path.splitext(base)[0]) or LICENSE_FILE_PATTERN.match(base):
                members.append((base, archive.read(member)))
    return members


# ---------------------------------------------------------------------------------------------------------------------
# Magick.NET delegate libraries
# ---------------------------------------------------------------------------------------------------------------------

def parse_magick_notice(text: str) -> dict:
    text = text.replace("\r\n", "\n")
    return {match.group(1): match.group(2) for match in re.finditer(r"^\[ (\S+) (\S+) \(\d{4}-\d\d-\d\d\) \]$", text, re.MULTILINE)}


def delegate_table(notice_text: str, delegates: dict) -> list:
    listed = parse_magick_notice(notice_text)
    known = delegates["components"]
    problems = []
    for name, version in listed.items():
        if name not in known:
            problems.append(f"Notice.txt lists {name} {version}, which is not in data/delegates.json")
        elif known[name]["version"] != version:
            problems.append(f"{name}: Notice.txt says {version}, data/delegates.json says {known[name]['version']}")
    for name in known:
        if name not in listed:
            problems.append(f"data/delegates.json lists {name}, which Notice.txt no longer contains")
    if problems:
        raise BuildError("Magick.NET delegate libraries changed:\n  " + "\n  ".join(problems))
    return [known[name] | {"name": name} for name in listed]


# ---------------------------------------------------------------------------------------------------------------------
# Pinned binaries: pdfium
# ---------------------------------------------------------------------------------------------------------------------

PDFIUM_LIBRARY = {"win-x64": "bin/pdfium.dll", "linux-x64": "lib/libpdfium.so", "linux-arm64": "lib/libpdfium.so",
                  "osx-x64": "lib/libpdfium.dylib", "osx-arm64": "lib/libpdfium.dylib"}


def pdfium_license_files(rid: str, pins: dict, publish: Path, destination: Path) -> dict:
    """Downloads the pinned pdfium-binaries archive, proves the package's library is that build, copies its licenses."""
    asset = pins["pdfium"]["assets"][rid]
    url = f"{pins['pdfium']['repository']}/releases/download/{pins['pdfium']['release']}/{asset['file']}"
    archive = download(url, asset["sha256"], CACHE / "downloads")
    destination.mkdir(parents=True, exist_ok=True)
    library_name = os.path.basename(PDFIUM_LIBRARY[rid])
    with tarfile.open(archive) as tar:
        member = tar.getmember(PDFIUM_LIBRARY[rid])
        pinned_hash = None
        import hashlib
        pinned_hash = hashlib.sha256(tar.extractfile(member).read()).hexdigest()
        if sha256_file(publish / library_name) != pinned_hash:
            raise BuildError(f"{library_name} in the package is not the pdfium build pinned in data/pins.json ({asset['file']})")
        for member in tar.getmembers():
            if member.isfile() and (member.name == "LICENSE" or member.name.startswith("licenses/")):
                target = destination / member.name
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(tar.extractfile(member).read())
        version = tar.extractfile("VERSION").read().decode().split()
    return {"file": asset["file"], "url": url, "sha256": asset["sha256"],
            "version": ".".join(v.split("=")[1] for v in version)}


# ---------------------------------------------------------------------------------------------------------------------
# Rust crates
# ---------------------------------------------------------------------------------------------------------------------

def read_crates(name: str) -> list:
    rows = []
    for line in (DATA / f"{name}-crates.tsv").read_text(encoding="utf-8").splitlines():
        if line and not line.startswith("#"):
            rows.append(line.split("\t"))
    return rows


def crate_license_files(name: str, version: str, checksum: str) -> list:
    url = f"https://static.crates.io/crates/{name}/{name}-{version}.crate"
    crate = download(url, checksum, CACHE / "crates")
    prefix = f"{name}-{version}/"
    files = []
    with tarfile.open(crate) as tar:
        for member in tar.getmembers():
            if not member.isfile() or not member.name.startswith(prefix):
                continue
            relative = member.name[len(prefix):]
            if "/" in relative:
                continue
            if LICENSE_FILE_PATTERN.match(os.path.splitext(relative)[0]) or LICENSE_FILE_PATTERN.match(relative):
                files.append((relative, tar.extractfile(member).read()))
    return files


def spdx_ids(expression: str) -> list:
    cleaned = re.sub(r"\bWITH\s+[A-Za-z0-9.+-]+", "", expression)
    return [i for i in re.findall(r"[A-Za-z0-9.+-]+", cleaned) if i not in EXPRESSION_WORDS]


def spdx_text(identifier: str) -> bytes | None:
    path = DATA / "license-texts" / f"{identifier}.txt"
    return path.read_bytes() if path.is_file() else None


def write_crate_licenses(crates: list, destination: Path) -> list:
    """Copies each crate's license files; returns (crate row, note) where note explains a fallback."""
    results = []
    for name, version, expression, checksum in crates:
        folder = destination / f"{name}-{version}"
        files = crate_license_files(name, version, checksum)
        note = ""
        if files:
            folder.mkdir(parents=True, exist_ok=True)
            for filename, data in files:
                (folder / filename).write_bytes(data)
        else:
            chosen = next((i for i in spdx_ids(expression) if spdx_text(i)), None)
            if not chosen:
                raise BuildError(f"crate {name} {version} ({expression}) has no license file and no SPDX text is in data/license-texts")
            folder.mkdir(parents=True, exist_ok=True)
            (folder / f"{chosen}.txt").write_bytes(spdx_text(chosen))
            note = f"crate has no license file; SPDX text of {chosen} supplied"
        results.append(((name, version, expression, checksum), note))
    return results


# ---------------------------------------------------------------------------------------------------------------------
# Rendering
# ---------------------------------------------------------------------------------------------------------------------

def escape(value: str) -> str:
    return value.replace("|", "\\|").replace("\n", " ").strip()


def table(header: list, rows: list) -> str:
    lines = ["| " + " | ".join(header) + " |", "|" + "|".join("---" for _ in header) + "|"]
    lines += ["| " + " | ".join(escape(str(cell)) for cell in row) + " |" for row in rows]
    return "\n".join(lines) + "\n"


def link(url: str, label: str | None = None) -> str:
    return f"[{label or url}]({url})"


def tree_url(repo: str, ref: str) -> str:
    return f"{repo}/tree/{ref}"


def copy_spdx(identifiers: set, destination: Path) -> list:
    written = []
    destination.mkdir(parents=True, exist_ok=True)
    for identifier in sorted(identifiers):
        text = spdx_text(identifier)
        if text is not None:
            (destination / f"{identifier}.txt").write_bytes(text)
            written.append(identifier)
    return written


def generate(stage: Path, publish: Path, rid: str, engines: dict) -> None:
    """stage: the package folder; publish: the folder dotnet publish wrote (before engines were added)."""
    up = read_upstream()
    pins = load_json(DATA / "pins.json")
    sources = load_json(DATA / "nuget-sources.json")
    delegates = load_json(DATA / "delegates.json")
    optional = load_json(DATA / "optional-engines.json")["engines"]
    adapted = load_json(DATA / "adapted-code.json")["items"]
    deps = load_json(publish / "filee-cli.deps.json")

    licenses = stage / "licenses"
    rmtree(licenses)
    licenses.mkdir(parents=True)
    spdx_used = {"MIT", "LGPL-2.1-or-later"}

    present = classify(publish, deps)
    runtime_key = next(k for k in present if k.startswith("runtimepack."))
    nuget_keys = sorted(k for k, v in deps["libraries"].items() if v["type"] == "package" and k in present)
    skipped = sorted(k for k, v in deps["libraries"].items() if v["type"] == "package" and k not in present)
    if skipped:
        print("Packages in deps.json with no file in the output (left out of the notices):", ", ".join(skipped))

    # --- .NET runtime ------------------------------------------------------------------------------------------------
    runtime_name, runtime_version = runtime_key.split("/")
    runtime_folder = nuget_root() / runtime_name.lower().replace("runtimepack.", "") / runtime_version
    runtime_nuspec = read_nuspec(runtime_name.replace("runtimepack.", ""), runtime_version)
    runtime_target = licenses / "dotnet-runtime"
    runtime_target.mkdir()
    for filename in ("LICENSE.TXT", "THIRD-PARTY-NOTICES.TXT"):
        shutil.copy2(runtime_folder / filename, runtime_target / filename)
    dotnet_commit = runtime_nuspec["repo_commit"]
    if not dotnet_commit:
        raise BuildError("the .NET runtime pack nuspec names no source commit")

    # --- NuGet packages ----------------------------------------------------------------------------------------------
    components = [nuget_component(key, sources) for key in nuget_keys]
    nuget_rows = []
    for component in components:
        folder = licenses / "nuget" / f"{component['name']}-{component['version']}"
        shipped = nupkg_license_files(component)
        if shipped:
            folder.mkdir(parents=True, exist_ok=True)
            for filename, data in shipped:
                (folder / filename).write_bytes(data)
        spdx_used.update(spdx_ids(component["license"]))
        files = ", ".join(f"`licenses/nuget/{folder.name}/{f}`" for f, _ in shipped) or "SPDX text in `licenses/spdx/`"
        ref_label = component["ref"][:12] if re.fullmatch(r"[0-9a-f]{40}", component["ref"]) else component["ref"]
        source = link(tree_url(component["repo"], component["ref"]), f"{component['repo'].split('://')[1]}@{ref_label}")
        if component["ref_note"]:
            source += f" ({component['ref_note']})"
        nuget_rows.append([component["name"], component["version"], component["license"], component["copyright"], source, files])

    # --- Magick.NET delegates ----------------------------------------------------------------------------------------
    magick = next(c for c in components if c["name"] == "Magick.NET-Q8-AnyCPU")
    with zipfile.ZipFile(magick["nupkg"]) as archive:
        notice_text = archive.read("Notice.txt").decode("utf-8-sig")
    delegate_rows = [[d["name"], d["version"], d["license"], link(tree_url(d["repository"], d["tag"]), f"{d['tag']}"), d["commit"][:12]]
                     for d in delegate_table(notice_text, delegates)]

    # --- pdfium ------------------------------------------------------------------------------------------------------
    pdfium = pdfium_license_files(rid, pins, publish, licenses / "pdfium")

    # --- Rust crates -------------------------------------------------------------------------------------------------
    crate_sections = {}
    for key in ("unhwp", "rhwp"):
        crate_sections[key] = write_crate_licenses(read_crates(key), licenses / "rust" / key)
        for (_, _, expression, _), _note in crate_sections[key]:
            spdx_used.update(spdx_ids(expression))

    # --- optional engines --------------------------------------------------------------------------------------------
    manifest = load_json(Path(engines["engines_json"]))
    downloadable = set(manifest["components"])
    for platform_entry in manifest["platforms"].values():
        downloadable.update(platform_entry["components"])
    unknown = sorted((downloadable - {"rhwp", "7zip"}) - set(optional))
    if unknown:
        raise BuildError(f"Filee can download engines this repository does not describe in data/optional-engines.json: {unknown}")
    # Every optional engine is listed whatever the platform: Filee also finds them on PATH, and a user may install any.
    optional_rows = [[v["name"], v["license"], link(v["url"]), link(v["source"]) if v["source"].startswith("http") else v["source"]]
                     for _, v in sorted(optional.items())]

    spdx_written = copy_spdx(spdx_used, licenses / "spdx")

    out = []
    w = out.append
    w(f"# Third-party notices: filee-cli {up['version']} ({rid})\n")
    w("This package is an **unofficial redistribution** of the command line of [Filee](https://github.com/KnifeLemon/Filee) "
      f"{up['tag']} (MIT, Copyright (c) 2026 Myeonggu Kim (KnifeLemon) and Filee contributors; see `LICENSE`). It was "
      "built from the upstream source" + (" plus the patches listed in `UPSTREAM.md`" if any((ROOT / "patches").glob("*.patch")) else ", unmodified") +
      " (see `UPSTREAM.md` for the exact commit). Every component below keeps its own license.\n")
    w("This file was generated by `scripts/generate_notices.py` from the files of this package: the dotnet publish output "
      "and its `filee-cli.deps.json`, the NuGet packages' own metadata, and the pins in `data/`. Components that are not "
      "in this package (the GUI, tray, Explorer menu, installers, fonts, icons, optional engines) are not listed as "
      "contents. The complete license texts and notice files are in `licenses/`.\n")

    w("## Summary of copyleft components\n")
    w("* **No GPL-only, AGPL or other strong-copyleft component is distributed in this package.** FFmpeg, Pandoc, calibre, "
      "Ghostscript, H2Orestart and Eclipse Temurin are *not* included (see \"Optional engines, not distributed here\").\n"
      "* **LGPL**: 7-Zip (`engines/7zip`) and the ImageMagick delegate libraries statically linked inside "
      f"`Magick.Native-Q8-*` (libde265, libheif, glib, gdk-pixbuf, pango, librsvg, libcroco, libraw, fribidi, liblqr, cairo "
      "and others, table below). Source locations are given; each LGPL library sits inside a separate, replaceable library "
      "file (`Magick.Native-Q8-*` or `7zz`/`7z.dll`) that you can rebuild from the sources named below and drop in.\n"
      "* **MPL-2.0 / MPL-1.1**: OpenMcdf (MPL-2.0) and UTF.Unknown (MPL-1.1, alternatively GPL-2.0-or-later or "
      "LGPL-2.1-or-later; used here under MPL-1.1) are unmodified binaries; their source is at the URLs in the NuGet table. "
      "Cairo (inside Magick.Native) is LGPL-2.1 or MPL-1.1; used under the LGPL.\n")

    w("## Filee\n")
    w(table(["Component", "License", "Source"], [
        [f"Filee {up['upstream_version']} (`filee-cli`, Filee.Core, Filee.Engines)", "MIT", link(tree_url(up["repository"], up["commit"]), f"{up['tag']} ({up['commit'][:12]})")]]))
    w("Third-party work adapted into Filee's own source files (found in the upstream tree at the pinned tag):\n")
    w(table(["Project", "What", "Where in Filee", "License", "Source"], [
        [a["name"], a["what"], a["where"], a["license"], link(a["repository"] if a["ref"] == "main" else tree_url(a["repository"], a["ref"]))] for a in adapted]))
    spdx_used.update(a["license"] for a in adapted)

    w("## .NET runtime (self-contained)\n")
    w(table(["Component", "Version", "License", "Source", "Notice files"], [
        [runtime_name.replace("runtimepack.", ""), runtime_version, "MIT", link(tree_url("https://github.com/dotnet/dotnet", dotnet_commit), f"dotnet/dotnet@{dotnet_commit[:12]}"),
         "`licenses/dotnet-runtime/LICENSE.TXT`, `licenses/dotnet-runtime/THIRD-PARTY-NOTICES.TXT`"]]))

    w("## NuGet libraries (from `filee-cli.deps.json`, restricted to files present in this package)\n")
    w(table(["Package", "Version", "License", "Copyright", "Source (commit or tag)", "License files"], nuget_rows))

    w("## Native code inside NuGet packages\n")
    w("### Magick.Native (ImageMagick) delegate libraries\n")
    w(f"`Magick.Native-Q8-*` is built by [dlemstra/Magick.Native]({pins['magick_native']['repository']}) "
      f"([{pins['magick_native']['tag']}]({tree_url(pins['magick_native']['repository'], pins['magick_native']['tag'])}), "
      f"commit {pins['magick_native']['commit'][:12]}) with the libraries below linked in statically. The list is the one in "
      "the `Notice.txt` of the Magick.NET-Q8-AnyCPU package (`licenses/nuget/Magick.NET-Q8-AnyCPU-*/Notice.txt`, which "
      "holds every license text); the generator fails when it differs from `data/delegates.json`. Versions and tags are the "
      "ones that file names; the license column is what each project declares.\n")
    w(table(["Library", "Version", "License", "Source tag", "Commit"], delegate_rows))
    w("### PDFium\n")
    w(f"`{os.path.basename(PDFIUM_LIBRARY[rid])}` is byte-identical to the one in [{pdfium['file']}]({pdfium['url']}) "
      f"(SHA-256 `{pdfium['sha256']}`), build {pdfium['version']} of [bblanchon/pdfium-binaries]({pins['pdfium']['repository']}) "
      f"(Apache-2.0; PDFium is BSD-3-Clause/Apache-2.0). Its own and its dependencies' licenses are in `licenses/pdfium/`.\n")
    w("### libunhwp (Unhwp)\n")
    w(f"`libunhwp` / `unhwp.dll` comes from the Unhwp NuGet package (MIT), built from "
      f"{link(tree_url(pins['unhwp']['repository'], pins['unhwp']['commit']), 'iyulab/unhwp@' + pins['unhwp']['commit'][:12])} "
      f"(version {pins['unhwp']['version']}, `cargo build --release` with the `ffi` feature). Rust crates in it "
      f"({len(crate_sections['unhwp'])}, superset over all platforms; license files in `licenses/rust/unhwp/`):\n")
    w(table(["Crate", "Version", "License", "crates.io SHA-256 (= Cargo.lock)", "Note"],
            [[r[0], r[1], r[2], r[3][:16] + "…", n] for r, n in crate_sections["unhwp"]]))
    w("### SkiaSharp and HarfBuzzSharp natives\n")
    w("`libSkiaSharp` and `libHarfBuzzSharp` come from the SkiaSharp / HarfBuzzSharp NuGet packages (MIT). Their own "
      "THIRD-PARTY-NOTICES (FreeType, libpng, zlib, libjpeg-turbo, ICU, HarfBuzz, Expat, WebP, …) are in "
      "`licenses/nuget/SkiaSharp.NativeAssets.*` and `licenses/nuget/HarfBuzzSharp.NativeAssets.*`.\n")

    w("## Bundled engines (`engines/`)\n")
    rhwp_source = ("built from source on Ubuntu 22.04 by this repository's CI: tag `v{v}`, commit `{c}`, "
                   "`cargo +{t} build --release --locked --bin rhwp`" if rid.startswith("linux-") else
                   "rhwp's own release build of tag `v{v}` ({asset})")
    rhwp_source = rhwp_source.format(v=pins["rhwp"]["version"], c=pins["rhwp"]["commit"], t=pins["rhwp"]["rust_toolchain"], asset=engines["rhwp"]["url"])
    w(table(["Engine", "Version", "License", "Source"], [
        ["rhwp (`engines/rhwp`)", pins["rhwp"]["version"], "MIT", link(tree_url(pins["rhwp"]["repository"], pins["rhwp"]["commit"]), f"edwardkim/rhwp@{pins['rhwp']['commit'][:12]}") + "; " + rhwp_source],
        ["7-Zip (`engines/7zip`)", pins["7zip"]["version"], pins["7zip"]["license"], link(tree_url(pins["7zip"]["repository"], pins["7zip"]["tag"]), f"ip7z/7zip tag {pins['7zip']['tag']}") + f" (commit {pins['7zip']['commit'][:12]}); binary from {engines['7zip']['url']}"]]))
    w("7-Zip is Copyright (C) 1999-2026 Igor Pavlov; its `License.txt` is in `engines/7zip`. The RAR decompression code in "
      "7-Zip is under the unRAR license restriction: it may be used to unpack RAR archives, but not to develop a RAR "
      "(WinRAR) compatible archiver or to re-create the RAR compression algorithm. Filee only reads RAR files.\n")
    w("rhwp's `LICENSE` is in `engines/rhwp`. Rust crates in rhwp "
      f"({len(crate_sections['rhwp'])}, superset over all platforms; license files in `licenses/rust/rhwp/`):\n")
    w(table(["Crate", "Version", "License", "crates.io SHA-256 (= Cargo.lock)", "Note"],
            [[r[0], r[1], r[2], r[3][:16] + "…", n] for r, n in crate_sections["rhwp"]]))
    w("rhwp does not embed font files in its release binary; it uses fonts installed on the computer (the fonts in the "
      "rhwp repository are web/test assets that are not part of the program).\n")

    w("## Optional engines, not distributed here\n")
    w("Filee can use these programs when they are installed on the computer (found on `PATH` or in the usual locations; "
      "`filee-cli` has no command to download them). They are separate programs under their own licenses, which are "
      "mostly GPL/AGPL; **none of them is in this package**. Obtain them from the official sources below.\n")
    w(table(["Engine", "License", "Official download", "Source code"], optional_rows))

    w("## License texts\n")
    w("Full texts: `LICENSE` (Filee, MIT), `licenses/spdx/` (" + ", ".join(spdx_written) + "), `licenses/nuget/`, "
      "`licenses/pdfium/`, `licenses/dotnet-runtime/`, `licenses/rust/`, `engines/7zip/License.txt`, `engines/rhwp/LICENSE`.\n")
    (stage / "THIRD-PARTY-NOTICES.md").write_text("\n".join(out), encoding="utf-8", newline="\n")
    copy_spdx(spdx_used, licenses / "spdx")
    print(f"THIRD-PARTY-NOTICES.md: {len(components)} NuGet packages, {len(delegate_rows)} delegate libraries, "
          f"{len(crate_sections['unhwp'])} + {len(crate_sections['rhwp'])} crates, {len(optional_rows)} optional engines listed")


def main() -> int:
    import argparse
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--stage", required=True, type=Path)
    parser.add_argument("--publish", required=True, type=Path)
    parser.add_argument("--rid", required=True)
    parser.add_argument("--engines-info", required=True, type=Path, help="JSON written by build_package.py")
    options = parser.parse_args()
    try:
        generate(options.stage, options.publish, options.rid, load_json(options.engines_info))
        return 0
    except BuildError as error:
        return fail(error)


if __name__ == "__main__":
    sys.exit(main())
