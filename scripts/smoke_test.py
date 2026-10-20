#!/usr/bin/env python3
"""Unpacks a release archive and runs it: `filee-cli version`, exit codes, PNG -> WebP, MD -> DOCX, MD -> HWPX -> PDF
(rhwp, Korean text), archive conversion (7-Zip), and the package layout. Exits non-zero on the first failure.

Usage: smoke_test.py ARCHIVE [--rid RID]
"""

import argparse
import json
import os
import shutil
import struct
import subprocess
import sys
import tarfile
import tempfile
import zipfile
import zlib
from pathlib import Path

from common import BuildError, extract_tar, fail, read_upstream

REQUIRED = ("LICENSE", "THIRD-PARTY-NOTICES.md", "UPSTREAM.md", "README.md", "engines/rhwp/LICENSE", "engines/7zip/License.txt")


def png_bytes(width=64, height=48) -> bytes:
    """A small gradient PNG, written without any imaging library."""
    def chunk(kind, data):
        body = kind + data
        return struct.pack(">I", len(data)) + body + struct.pack(">I", zlib.crc32(body) & 0xFFFFFFFF)
    rows = b"".join(b"\x00" + b"".join(bytes((x * 4 % 256, y * 5 % 256, 128)) for x in range(width)) for y in range(height))
    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(rows)) + chunk(b"IEND", b""))


def extract(archive: Path, destination: Path) -> Path:
    if archive.name.endswith(".zip"):
        with zipfile.ZipFile(archive) as z:
            for info in z.infolist():
                z.extract(info, destination)
                mode = info.external_attr >> 16
                if mode:
                    os.chmod(destination / info.filename, mode)
    else:
        extract_tar(archive, destination)
    folders = [p for p in destination.iterdir()]
    if len(folders) != 1 or not folders[0].is_dir():
        raise BuildError(f"{archive.name} must contain exactly one top-level folder, found {[p.name for p in folders]}")
    return folders[0]


class Smoke:
    def __init__(self, exe: Path, work: Path):
        self.exe, self.work = exe, work

    def run(self, *args, expect=0):
        result = subprocess.run([str(self.exe), *args], cwd=self.work, capture_output=True, text=True, encoding="utf-8", timeout=300)
        if result.returncode != expect:
            raise BuildError(f"filee-cli {' '.join(args)}: exit {result.returncode}, expected {expect}\nstdout: {result.stdout}\nstderr: {result.stderr}")
        return result

    def convert(self, source: str, target: str, expected: Path):
        result = self.run("convert", source, "--to", target, "--json", "--overwrite")
        data = json.loads(result.stdout)
        if not (data["converted"] == 1 and data["failed"] == 0 and data["target"] == target and data["files"][0]["state"] == "done"):
            raise BuildError(f"unexpected JSON for {source} -> {target}: {result.stdout}")
        outputs = data["files"][0]["outputs"]
        if len(outputs) != 1 or Path(outputs[0]).resolve() != expected.resolve() or expected.stat().st_size == 0:
            raise BuildError(f"{source} -> {target}: output {outputs} is not the expected non-empty {expected}")
        return expected.read_bytes()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("archive", type=Path)
    options = parser.parse_args()
    try:
        archive = options.archive.resolve()
        up = read_upstream()
        with tempfile.TemporaryDirectory(prefix="filee-smoke-") as tmp:
            tmp = Path(tmp)
            package = extract(archive, tmp / "x")
            name = archive.name.removesuffix(".tar.gz").removesuffix(".zip")
            if package.name != name:
                raise BuildError(f"top-level folder is {package.name}, expected {name}")
            exe = package / ("filee-cli.exe" if archive.suffix == ".zip" else "filee-cli")
            if not exe.is_file():
                raise BuildError(f"{exe.name} is missing")
            for required in REQUIRED:
                if not (package / required).is_file():
                    raise BuildError(f"{required} is missing from the package")
            if not any(package.glob("engines/rhwp/rhwp*")) or not any(package.glob("engines/7zip/7z*")):
                raise BuildError("engine programs are missing")

            work = tmp / "work"
            work.mkdir()
            smoke = Smoke(exe, work)

            version = smoke.run("version").stdout.strip()
            if version != f"filee {up['upstream_version']}":
                raise BuildError(f"`version` printed {version!r}, expected 'filee {up['upstream_version']}'")
            print("version:", version)

            if "png" not in smoke.run("formats").stdout:
                raise BuildError("`formats` does not list png")
            smoke.run("convert", "missing.png", "--to", "webp", expect=2)
            smoke.run("convert", "missing.png", "--to", "nosuchformat", expect=2)

            (work / "pic.png").write_bytes(png_bytes())
            webp = smoke.convert("pic.png", "webp", work / "pic.webp")
            if webp[:4] != b"RIFF" or webp[8:12] != b"WEBP":
                raise BuildError("pic.webp is not a WebP file")
            print("PNG -> WebP ok")

            (work / "doc.md").write_text("# 한글 테스트 문서\n\n안녕하세요. **한글** 렌더링 검사입니다. ABC 123.\n\n- 하나\n- 둘\n", encoding="utf-8")
            docx = smoke.convert("doc.md", "docx", work / "doc.docx")
            with zipfile.ZipFile(work / "doc.docx") as z:
                if "word/document.xml" not in z.namelist():
                    raise BuildError("doc.docx has no word/document.xml")
                if "한글".encode() not in z.read("word/document.xml"):
                    raise BuildError("doc.docx lost the Korean text")
            print("MD -> DOCX ok")

            smoke.convert("doc.md", "hwpx", work / "doc.hwpx")
            pdf = smoke.convert("doc.hwpx", "pdf", work / "doc.pdf")
            if not pdf.startswith(b"%PDF-") or b"/Type /Page" not in pdf and b"/Type/Page" not in pdf:
                raise BuildError("doc.pdf (rhwp) is not a PDF with a page")
            print("MD -> HWPX -> PDF (rhwp) ok")

            with zipfile.ZipFile(work / "pack.zip", "w") as z:
                z.writestr("a.txt", "hello " * 50)
            smoke.convert("pack.zip", "7z", work / "pack.7z")
            if (work / "pack.7z").read_bytes()[:6] != b"7z\xbc\xaf\x27\x1c":
                raise BuildError("pack.7z (7-Zip) has no 7z signature")
            print("ZIP -> 7Z (7-Zip) ok")

            result = smoke.run("convert", str(work / "nothing-to-do"), "--to", "pdf", expect=2)
        print(f"smoke test passed: {archive.name}")
        return 0
    except (BuildError, subprocess.SubprocessError, json.JSONDecodeError, KeyError) as error:
        return fail(error)


if __name__ == "__main__":
    sys.exit(main())
