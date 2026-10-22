# filee-cli

**Unofficial redistribution of the command line of [KnifeLemon/Filee](https://github.com/KnifeLemon/Filee).**
This repository is not the Filee project, is not made, reviewed or endorsed by its author, and its packages are
**not the same as Filee's official downloads**: they contain only `filee-cli` (no GUI, tray, Explorer or Finder menu,
or installer), and are built from a pinned upstream tag by the scripts here.

- Filee is MIT-licensed (Copyright (c) 2026 Myeonggu Kim (KnifeLemon) and Filee contributors). `LICENSE` keeps that
  notice and adds one line for the scripts, CI and docs of this repository.
- Source of what you download: upstream tag + commit in [`UPSTREAM`](UPSTREAM) (nothing from upstream is stored in
  this repository), plus the patches in [`patches/`](patches) (currently none). Every package carries `UPSTREAM.md`
  with the exact commit, SDK version and engine hashes.
- **Issues**: problems with these packages (missing files, archive layout, checksums, a platform that does not start,
  notices) go to this repository. Problems with a conversion itself (output quality, unsupported file) belong to
  [Filee](https://github.com/KnifeLemon/Filee/issues) or to the engine it used; reproduce with the official `filee`
  first if you can.
- The name "Filee" belongs to its author. The executable stays `filee-cli` so programs that look for it keep working;
  nothing here claims to be the official release.

## Download and verify

Releases have five archives and one `SHA256SUMS`:

| Platform | Asset |
|---|---|
| Windows x64 | `filee-cli-<version>-win-x64.zip` |
| Linux x64 (glibc ≥ 2.35, Ubuntu 22.04+) | `filee-cli-<version>-linux-x64.tar.gz` |
| Linux arm64 (glibc ≥ 2.35) | `filee-cli-<version>-linux-arm64.tar.gz` |
| macOS Intel | `filee-cli-<version>-osx-x64.tar.gz` |
| macOS Apple silicon | `filee-cli-<version>-osx-arm64.tar.gz` |

`<version>` is the upstream version plus this repository's packaging revision, e.g. `2.0.0-1`.

```sh
# Download the asset and SHA256SUMS from the release page (or use the Homebrew tap / Scoop bucket below):
gh release download v2.0.0-1 --repo NomaDamas/filee-cli --pattern 'filee-cli-2.0.0-1-linux-x64.tar.gz' --pattern SHA256SUMS

sha256sum --check --ignore-missing SHA256SUMS       # Linux
shasum -a 256 --check --ignore-missing SHA256SUMS   # macOS
```

```powershell
# Windows: the hash must equal the line for the zip in SHA256SUMS
(Get-FileHash .\filee-cli-2.0.0-1-win-x64.zip -Algorithm SHA256).Hash.ToLower()
Select-String 'win-x64' .\SHA256SUMS
```

Once the repository is public, releases also carry a signed build-provenance attestation:
`gh attestation verify <asset> --repo NomaDamas/filee-cli`.

macOS packages are ad-hoc signed like upstream's, **not notarized**. Browsers add a quarantine flag to downloads;
remove it with `xattr -dr com.apple.quarantine filee-cli-<version>-osx-arm64` (files fetched by `curl`/`gh` have none).

## Use

```sh
tar -xzf filee-cli-2.0.0-1-osx-arm64.tar.gz
cd filee-cli-2.0.0-1-osx-arm64
./filee-cli version
./filee-cli convert photo.png --to webp --json
./filee-cli formats
```

`--json` prints one JSON object (`preset`, `target`, `converted`, `skipped`, `failed`, `files[]` with `state`,
`outputs`, `error`). Exit codes: 0 all converted, 1 some failed, 2 wrong arguments or a missing input, 3 nothing to
convert. Put the folder on `PATH`, or link `filee-cli` into a directory that is.

Callers should run it as a **separate process** (it is a self-contained .NET program with its own native libraries).
It does not need a display. The `engines/` folder must stay next to the program. Linux needs the libraries .NET itself
needs; see the package's `README.md`.

Several `filee-cli` processes at the same time share one LibreOffice profile folder, which LibreOffice does not allow
([#2](https://github.com/NomaDamas/filee-cli/issues/2)): give each process its own `FILEE_DATA_DIR`, or pass all files
to one `filee-cli convert` (that parallelises safely).

### Inside the package

| Path | |
|---|---|
| `filee-cli`, `*.dll`/`*.so`/`*.dylib`, `filee-cli.deps.json` | the program, the .NET runtime and native libraries |
| `engines/rhwp/` | rhwp (MIT), HWP/HWPX → PDF. Linux builds are compiled from source on Ubuntu 22.04 |
| `engines/7zip/` | 7-Zip (LGPL-2.1+, unRAR restriction on the RAR code), archives |
| `engines/pandoc/`, `engines/calibre/`, `engines/libreoffice/` (macOS, Windows), `engines/ffmpeg/`, `engines/ghostscript/` (macOS, Windows), `engines/h2orestart` + `engines/jre/` (Windows) | the vendors' own unmodified binaries, see below |
| `LICENSE`, `THIRD-PARTY-NOTICES.md`, `licenses/` | licenses, generated from the package contents |
| `UPSTREAM.md` | upstream URL, tag, commit, patches, build date, SDK |

### Bundled engines (GPL, LGPL, AGPL and others) and their source

Filee itself is MIT and calls these engines as **separate programs** (`filee-cli` starts them with command-line
arguments and links none of their code). Each package carries the engines Filee offers on that platform, exactly the
files upstream's `engines.json` pins (same URL and SHA-256), with three exceptions where this repository ships another
build of the **same release** because the pinned one does not exist or has no published build scripts:

- FFmpeg on Windows: upstream pins a Gyan.dev build whose scripts are not published; the BtbN GPL build is shipped.
- FFmpeg on macOS: Filee has no macOS download (it looks for one on the `PATH`); the packages carry
  [Martin Riedl's](https://ffmpeg.martin-riedl.de) GPL build 9.0.2 (Developer-ID signed by him, signature kept), whose
  build scripts and all 31 dependencies are pinned.
- Ghostscript on macOS: likewise no download upstream; the packages carry conda-forge's `gs` 10.08.0, built from the
  same feedstock commit as the Windows package upstream pins.

Each engine keeps its license; the full list with versions, SHA-256 and licenses is in the package's
`THIRD-PARTY-NOTICES.md`.

| Engine | Where | License | Source |
|---|---|---|---|
| Pandoc 3.12 | all | GPL-2.0-or-later | hosted: `filee-cli-<version>-source-pandoc.tar.gz` |
| FFmpeg 9.0.2 (BtbN build; Martin Riedl build on macOS) | all | GPL-3.0-or-later | hosted: `…-source-ffmpeg.tar.gz` |
| calibre 9.15.0 | all | GPL-3.0 | hosted: `…-source-calibre.tar.gz` |
| Ghostscript 10.08 | macOS, Windows | AGPL-3.0 | hosted: `…-source-ghostscript.tar.gz` |
| H2Orestart 0.7.14, Eclipse Temurin JRE 21 | Windows | GPL-3.0; GPL-2.0 + Classpath exception | hosted: `…-source-h2orestart.tar.gz`, `…-source-jre.tar.gz` |
| LibreOffice 26.2.6 | macOS, Windows | MPL-2.0 (+ LGPL/GPL parts) | TDF's own source release, URL and SHA-256 in the notices |
| Microsoft VC++ runtime | Windows | proprietary redistributable | none exists |

The **Corresponding Source** of the hosted engines is published in the same release as the packages (assets
`filee-cli-<version>-source-<engine>.tar.gz`, also listed in `SHA256SUMS`). Each holds a `SOURCES.md` (written offer,
every component with version, license, URL, commit or SHA-256, how the binary was built, and anything that could not be
pinned) and the pinned sources themselves; the machine-readable pins are in
[`data/engine-sources/`](data/engine-sources). To drop an engine you do not want, delete its folder under `engines/`:
only the conversions it provided go away. If you install an engine yourself and put it on `PATH`, `filee-cli` uses
the bundled copy first (Filee's lookup order), then the one on `PATH`.

Engines Filee can use but that this package does not carry (Ghostscript and LibreOffice on Linux) are listed in the
"Optional engines, not distributed here" section of `THIRD-PARTY-NOTICES.md` with license and download link; install
one on `PATH` and `filee-cli` finds it. `filee-cli` has no command to download engines.

On Linux the bundled calibre is a Qt program: it needs `libxcb1 libxkbcommon0 libfontconfig1 libfreetype6 libx11-6
libegl1 libopengl0 libglx0` (apt names) even without a display. Without them only calibre's conversions (for example
EPUB → MOBI) fail, with an error naming the missing library.

The packages are large because of this (about 600 MB to 900 MB each). `filee-cli formats` lists what each package can do.

## How the packages are made

```
UPSTREAM (tag + commit) ──► filee-build fetch ──► dotnet publish src/Filee.Cli (self-contained, per RID)
                                                          + rhwp + 7-Zip + the platform's engines (pinned, SHA-256 checked)
                                                          + generated notices, UPSTREAM.md ──► archive ──► smoke test
```

The build program is `cmd/filee-build`, a single static **Go** binary (standard library plus one small xz reader; no
Python, no virtualenv, no npm). The only external tools are `git`, the **.NET SDK** (`dotnet publish` is what
compiles Filee, so it is needed to *build*, never to *run* the packages: they are self-contained) and, on macOS,
`codesign`. Rust (`rustup`) is needed only to compile rhwp for Linux.

```sh
go build -o filee-build ./cmd/filee-build
./filee-build local                 # build + test all five packages from this computer (see docs/releasing.md)
./filee-build local --rids osx-arm64
./filee-build local --publish       # after all five pass: tag, push the tag, create the GitHub release
```

Without GitHub Actions, `local` builds macOS directly, Linux in Docker
(`ubuntu:22.04`, then a second clean container for the smoke test) and Windows over SSH. The workflows in
`.github/workflows` do the same on GitHub's runners (docs/releasing.md).
Each package is smoke-tested after unpacking: `filee-cli version`, exit codes, PNG→WebP, MD→DOCX, MD→HWPX→PDF with
Korean text, ZIP→7Z, and Linux files are checked for glibc ≤ 2.35. Details: [docs/releasing.md](docs/releasing.md);
keeping up with upstream: [docs/updating.md](docs/updating.md); trademark and licensing notes:
[docs/legal.md](docs/legal.md).

The Go program, patches and documents of this repository are MIT-licensed (see `LICENSE`).
