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

## Install

| Platform | Command |
|---|---|
| macOS (Apple silicon, Intel), Linux (x64, arm64) | `brew install NomaDamas/filee-cli/filee-cli` ([tap](https://github.com/NomaDamas/homebrew-filee-cli)) |
| Windows x64 | `scoop bucket add filee-cli https://github.com/NomaDamas/scoop-filee-cli` then `scoop install filee-cli` ([bucket](https://github.com/NomaDamas/scoop-filee-cli)) |
| anywhere | the archive from the [latest release](https://github.com/NomaDamas/filee-cli/releases/latest), see below |

```sh
filee-cli formats                          # 255 formats, 368 extensions
filee-cli convert photo.heic --to jpg
filee-cli convert *.png --to webp -q 80 -o out
```

The download is 77 to 87 MB per platform. The conversion engines (FFmpeg, Pandoc, calibre, Ghostscript, LibreOffice) are **not in the package**: the first
conversion that needs one downloads the vendor's copy, checked against a pinned SHA-256 (see "Engines" below).
Both package managers check the package's SHA-256.

## Download and verify

Releases have five archives and one `SHA256SUMS`:

| Platform | Asset |
|---|---|
| Windows x64 | `filee-cli-<version>-win-x64.zip` |
| Linux x64 (glibc ≥ 2.35, Ubuntu 22.04+) | `filee-cli-<version>-linux-x64.tar.gz` |
| Linux arm64 (glibc ≥ 2.35) | `filee-cli-<version>-linux-arm64.tar.gz` |
| macOS Intel | `filee-cli-<version>-osx-x64.tar.gz` |
| macOS Apple silicon | `filee-cli-<version>-osx-arm64.tar.gz` |

`<version>` is the upstream version plus this repository's packaging revision, e.g. `2.0.0-2`.

```sh
# Download the asset and SHA256SUMS from the release page (or use the Homebrew tap / Scoop bucket below):
gh release download v2.0.0-2 --repo NomaDamas/filee-cli --pattern 'filee-cli-2.0.0-2-linux-x64.tar.gz' --pattern SHA256SUMS

sha256sum --check --ignore-missing SHA256SUMS       # Linux
shasum -a 256 --check --ignore-missing SHA256SUMS   # macOS
```

```powershell
# Windows: the hash must equal the line for the zip in SHA256SUMS
(Get-FileHash .\filee-cli-2.0.0-2-win-x64.zip -Algorithm SHA256).Hash.ToLower()
Select-String 'win-x64' .\SHA256SUMS
```

Releases built by `release.yml` also carry a signed build-provenance attestation:
`gh attestation verify <asset> --repo NomaDamas/filee-cli`.

macOS packages are ad-hoc signed like upstream's, **not notarized**. Browsers add a quarantine flag to downloads;
remove it with `xattr -dr com.apple.quarantine filee-cli-<version>-osx-arm64` (files fetched by `curl`/`gh` have none).

## Use

```sh
tar -xzf filee-cli-2.0.0-2-osx-arm64.tar.gz
cd filee-cli-2.0.0-2-osx-arm64
./filee-cli version
./filee-cli convert photo.png --to webp --json
./filee-cli formats
```

`--json` prints one JSON object (`preset`, `target`, `converted`, `skipped`, `failed`, `files[]` with `state`,
`outputs`, `error`). Exit codes: 0 all converted, 1 some failed, 2 wrong arguments or a missing input, 3 nothing to
convert. Put the folder on `PATH`, or link `filee-cli` into a directory that is.

Callers should run it as a **separate process** (it is a self-contained .NET program with its own native libraries).
It does not need a display. The `engines/` folder must stay next to the program. Linux needs the libraries .NET itself
needs; see the package's `README.md`. Engines are downloaded to the user's `filee` data folder (`FILEE_DATA_DIR`
moves it); progress and the "installing ..." lines go to **stderr**, so `--json` output on stdout stays clean.

Several `filee-cli` processes at the same time share one LibreOffice profile folder, which LibreOffice does not allow
([#2](https://github.com/NomaDamas/filee-cli/issues/2)): give each process its own `FILEE_DATA_DIR`, or pass all files
to one `filee-cli convert` (that parallelises safely).

### Inside the package

| Path | |
|---|---|
| `filee-cli`, `*.dll`/`*.so`/`*.dylib`, `filee-cli.deps.json` | the program, the .NET runtime and native libraries |
| `engines/rhwp/` | rhwp (MIT), HWP/HWPX → PDF. Linux builds are compiled from source on Ubuntu 22.04 |
| `engines/7zip/` | 7-Zip (LGPL-2.1+, unRAR restriction on the RAR code), archives |
| `LICENSE`, `THIRD-PARTY-NOTICES.md`, `licenses/` | licenses, generated from the package contents |
| `UPSTREAM.md` | upstream URL, tag, commit, patches, build date, SDK |

### Engines (GPL, LGPL, AGPL and others), downloaded on demand

Filee itself is MIT and calls these engines as **separate programs** (`filee-cli` starts them with command-line
arguments and links none of their code). The packages carry only rhwp and 7-Zip. For everything else `filee-cli`
does what the Filee app does in *Settings → Engines*: before a conversion it works out which engine the files need,
downloads it from the vendor's own location, checks the **SHA-256 pinned in Filee's `engines.json`**, and goes on.
A download that does not match is discarded and nothing is installed.

```sh
filee-cli engines list                   # what each engine is for, its size, and whether it is installed
filee-cli engines install ffmpeg         # get one ahead of time (before going offline); "all" gets every one
filee-cli engines remove calibre         # delete the copy filee-cli downloaded
filee-cli convert clip.mp4 --to mp3      # downloads FFmpeg first (on stderr), then converts
filee-cli convert a.md --to pdf --no-install   # never download: a missing engine fails the file and names it
```

| Engine | Used for | Download (macOS / Windows / Linux) | License |
|---|---|---|---|
| FFmpeg 9.0.2 | video, audio, Live Photo, HEIC writing | 54 to 68 MB / 100 MB / 127 to 151 MB | GPL-3.0-or-later |
| Pandoc 3.12 | Markdown, LaTeX, Org, AsciiDoc, Markdown → slides | 26 to 42 MB / 42 MB / 35 to 38 MB | GPL-2.0-or-later |
| Ghostscript 10.08 | EPS, PostScript | 60 MB / 20 MB / none (use your package manager) | AGPL-3.0 |
| calibre 9.15 | Kindle (MOBI, AZW3) and rare e-book formats | 350 MB / 227 MB / 182 to 195 MB | GPL-3.0 |
| LibreOffice 26.2 | DOC, PPT, RTF, ODF and other older or rare Office formats | 298 to 308 MB / 423 MB (with the HWP filter H2Orestart and a JRE) / none (use your package manager) | MPL-2.0 (+ LGPL/GPL parts) |

Sizes are the pinned downloads (`filee-cli engines list` shows the ones for your computer); a first LibreOffice or calibre
conversion on Windows took about 15 minutes to download and unpack on the test machine, later ones take seconds. The
download size on disk after unpacking is larger (calibre about 660 MB, LibreOffice 1.3 GB).

- Where the PC already has an engine (on the `PATH`, e.g. from Homebrew, or an installed LibreOffice), that one is used
  and nothing is downloaded.
- macOS: upstream Filee has no FFmpeg or Ghostscript download there. This repository adds one for each in a
  [patch](patches/0001-on-demand-engines.patch): [Martin Riedl's](https://ffmpeg.martin-riedl.de) GPL build of FFmpeg 9.0.2
  (his signature is kept) and conda-forge's `gs` 10.08.0 (same release Filee downloads for Windows). Both are checked
  against SHA-256 values computed by this repository. The bundled Ghostscript needs macOS 14.5 or later
  ([#5](https://github.com/NomaDamas/filee-cli/issues/5)).
- Linux has no Ghostscript or LibreOffice download (Filee offers none): install them with your package manager and
  `filee-cli` finds them. `filee-cli engines list` says so.
- Concurrent `filee-cli` processes take turns installing (a lock file in the engines folder), so two of them never
  unpack into the same folder.
- On Linux calibre is a Qt program: it needs `libxcb1 libxkbcommon0 libfontconfig1 libfreetype6 libx11-6 libegl1
  libopengl0 libglx0` (apt names) even without a display. Without them only calibre's conversions (for example EPUB →
  MOBI) fail, with an error naming the missing library.

Each engine keeps its own license, listed with the official download and source location in the package's
`THIRD-PARTY-NOTICES.md` ("Engines downloaded on demand"). They are the vendors' unmodified files, fetched from the
vendors' sites; **this repository does not redistribute them**, so it hosts no engine source code. `filee-cli formats`
lists every format, and `filee-cli formats <format>` what it converts to.

## How the packages are made

```
UPSTREAM (tag + commit) ──► filee-build fetch ──► dotnet publish src/Filee.Cli (self-contained, per RID)
                                                          + rhwp + 7-Zip
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
