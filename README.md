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

`<version>` is the upstream version plus this repository's packaging revision, e.g. `1.12.0-1`.

```sh
# Download the asset and SHA256SUMS from the release page (a private repository needs `gh release download`):
gh release download v1.12.0-1 --repo NomaDamas/filee-cli --pattern 'filee-cli-1.12.0-1-linux-x64.tar.gz' --pattern SHA256SUMS

sha256sum --check --ignore-missing SHA256SUMS       # Linux
shasum -a 256 --check --ignore-missing SHA256SUMS   # macOS
```

```powershell
# Windows: the hash must equal the line for the zip in SHA256SUMS
(Get-FileHash .\filee-cli-1.12.0-1-win-x64.zip -Algorithm SHA256).Hash.ToLower()
Select-String 'win-x64' .\SHA256SUMS
```

Once the repository is public, releases also carry a signed build-provenance attestation:
`gh attestation verify <asset> --repo NomaDamas/filee-cli`.

macOS packages are ad-hoc signed like upstream's, **not notarized**. Browsers add a quarantine flag to downloads;
remove it with `xattr -dr com.apple.quarantine filee-cli-<version>-osx-arm64` (files fetched by `curl`/`gh` have none).

## Use

```sh
tar -xzf filee-cli-1.12.0-1-osx-arm64.tar.gz
cd filee-cli-1.12.0-1-osx-arm64
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

### Inside the package

| Path | |
|---|---|
| `filee-cli`, `*.dll`/`*.so`/`*.dylib`, `filee-cli.deps.json` | the program, the .NET runtime and native libraries |
| `engines/rhwp/` | rhwp (MIT), HWP/HWPX → PDF. Linux builds are compiled from source on Ubuntu 22.04 |
| `engines/7zip/` | 7-Zip (LGPL-2.1+, unRAR restriction on the RAR code), archives |
| `LICENSE`, `THIRD-PARTY-NOTICES.md`, `licenses/` | licenses, generated from the package contents |
| `UPSTREAM.md` | upstream URL, tag, commit, patches, build date, SDK |

### Optional engines (not included)

FFmpeg, Pandoc, calibre, Ghostscript, LibreOffice, H2Orestart and a Java runtime are **not distributed** here: most
are GPL/AGPL. `filee-cli` has no command to download them (the app's *Settings → Engines* does that in the GUI). If you
install one yourself so that its program is on `PATH` (or in the usual folder, e.g. Homebrew's), `filee-cli` finds it
and the conversions that need it start working; `filee-cli formats` shows what is available. Licenses and official
download links are in the "Optional engines, not distributed here" section of `THIRD-PARTY-NOTICES.md`.

## How the packages are made

```
UPSTREAM (tag + commit) ──► scripts/fetch_upstream.py ──► dotnet publish src/Filee.Cli (self-contained, per RID)
                                                          + rhwp + 7-Zip (pinned, SHA-256 checked)
                                                          + generated notices, UPSTREAM.md ──► archive ──► smoke test
```

CI (`.github/workflows`) builds all five platforms on every push, runs `filee-cli version` and real conversions
(PNG→WebP, MD→DOCX, MD→HWPX→PDF with Korean text, ZIP→7Z) on the unpacked archive, and checks glibc ≤ 2.35 for Linux.
Pushing a tag `v<version>` publishes a release. Details: [docs/releasing.md](docs/releasing.md); keeping up with
upstream: [docs/updating.md](docs/updating.md); trademark and licensing notes: [docs/legal.md](docs/legal.md).

The scripts, patches and documents of this repository are MIT-licensed (see `LICENSE`).
