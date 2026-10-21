# Updating to a new upstream Filee release

Nothing from upstream lives in this repository: `UPSTREAM` pins a tag and the exact commit, and
`filee-build fetch` checks it out (and stops if the tag has moved).

1. **Read what changed.** Compare the tags on GitHub (`KnifeLemon/Filee`). Look at changes to
   `src/Filee.Cli`, `src/Filee.Engines/Infrastructure/engines.json`, `Directory.Packages.props`, `global.json`,
   `build/build-linux-native.py` (its rhwp pin and Rust toolchain), `THIRD-PARTY-NOTICES.md`.
2. **Edit `UPSTREAM`**: `tag`, `commit` (`git ls-remote https://github.com/KnifeLemon/Filee refs/tags/<tag>^{}`),
   `revision=1`. A change that is only in this repository (scripts, docs, patches) keeps the upstream tag and
   raises `revision` (`1.12.0-1` → `1.12.0-2`).
3. **If upstream changed a bundled engine** (`engines.json`, `rhwp` or `7zip` version): update `data/pins.json`
   (version, tag, commit; `rust_toolchain` from the new tag's `rust-toolchain.toml`) and rerun
   `./filee-build crates` (needs `cargo`, network). `filee-build package` stops with a clear message when
   the two disagree, and so does `filee-build rhwp`.
4. **If native dependencies changed** (the build fails in the notices step with a message naming what changed):
   * *Magick.Native delegates changed*: the error lists them. Update `data/delegates.json` (version, repository, tag,
     commit via `git ls-remote --tags <repository>`).
   * *`Unhwp` version changed*: set `unhwp` in `data/pins.json` to the commit its nuspec names (`<repository commit>`),
     rerun `filee-build crates`.
   * *pdfium changed*: set `pdfium` (release, per-RID asset name and SHA-256) in `data/pins.json`; the generator proves
     the library in the package is byte-identical to that asset.
   * *a NuGet package without a commit in its nuspec*: add `ref_override` / `license_override` in
     `data/nuget-sources.json` with how you found it.
   * *a new optional engine in `engines.json`*: describe it in `data/optional-engines.json` (license, download URL).
5. **Build locally** (any one platform is enough to catch most problems):
   ```sh
   go build -o filee-build ./cmd/filee-build
   ./filee-build fetch
   ./filee-build package --rid osx-arm64     # Linux: use `local --rids linux-x64` (Docker) instead
   ./filee-build smoke dist/filee-cli-<version>-osx-arm64.tar.gz
   ```
   or `./filee-build local` for all five.
   Open `work/<rid>/filee-cli-*/THIRD-PARTY-NOTICES.md` and read it. The file `dist/*.files.txt` is the file list
   of each archive; no GUI file (Avalonia, `Filee.exe`, installers, Explorer DLLs, fonts) may be in it
   (`filee-build package` fails otherwise).
6. **Patches**: avoid them. When one is unavoidable, put a `git format-patch`-style file in `patches/NNNN-name.patch`
   (applied in name order by `filee-build fetch`), explain it in the file header, and drop it as soon as upstream has
   the change. `UPSTREAM.md` in the package lists applied patches with their hashes.
7. **Run the librarian check** (`AUTORAG_FILEE_LIVE=1 AUTORAG_FILEE_BIN=<filee-cli> bun run vitest run
   test/live-e2e/filee-convert.test.ts` in `Marker-Inc-Korea/AutoRAG`, branch `feat/file-conversion-filee`).
8. **Commit and push, then release**: `./filee-build local --publish` (see `docs/releasing.md`).
9. Update the pinned version and SHA-256 in Dazzi's `scripts/release/stage-tools.mjs` from the release's `SHA256SUMS`.

## How THIRD-PARTY-NOTICES.md is generated

`cmd/filee-build/notices.go` runs inside `filee-build package` for every RID and writes the file from the package, not
from upstream's notice file:

- Files in the publish folder are mapped to NuGet packages through `filee-cli.deps.json`; a native file or program that
  maps to nothing fails the build. Only packages with files in the output appear (that is how Avalonia,
  CommunityToolkit.Mvvm, SharpHook/libuiohook, Material Design Icons, Noto fonts, Inno Setup, the Explorer menu
  DLL and Simple Icons are absent: none of them is in the CLI's dependency graph or output).
- License, copyright and source commit come from each package's nuspec; license files from the package itself.
- ImageMagick's delegate libraries come from `Notice.txt` inside Magick.NET and must match `data/delegates.json`.
- Rust crates inside `libunhwp` and `rhwp` come from `data/*-crates.tsv` (regenerated from `cargo tree`, with
  `Cargo.lock` checksums); license files are taken from the crates.io `.crate` file with that checksum.
- The optional-engines section is built from `data/optional-engines.json` and checked against upstream's
  `engines.json`.

After a build, `licenses/` holds every license text referenced.
