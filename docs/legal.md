# Licensing and trademark notes

## What this repository redistributes

Binary packages of `Filee.Cli` (MIT) built from the upstream source at a pinned commit plus the patches in `patches/` (they add on-demand engine
downloads to the command line), with
[rhwp](https://github.com/edwardkim/rhwp) (MIT) and [7-Zip](https://github.com/ip7z/7zip) (LGPL-2.1+) next to it.
Every package carries `LICENSE` (upstream's MIT text with its copyright line intact; one more line covers this
repository's own scripts), `THIRD-PARTY-NOTICES.md`, `licenses/` and `UPSTREAM.md`.

* **Corresponding source** for LGPL/MPL parts is named with tag or commit URLs in `THIRD-PARTY-NOTICES.md`
  (7-Zip, the ImageMagick delegate libraries inside Magick.Native, OpenMcdf, UTF.Unknown).
* **GPL, LGPL and AGPL engines are not redistributed.** Pandoc, FFmpeg, calibre, Ghostscript, H2Orestart, Temurin and
  LibreOffice (MPL-2.0) are neither in the packages nor in the release assets. `filee-cli` downloads the vendor's own
  unmodified file from the vendor's site (the URLs Filee pins; on macOS also Martin Riedl's FFmpeg and conda-forge's
  Ghostscript) when the user's conversion needs it, checks its SHA-256, and starts it as a separate process, never
  linked into `filee-cli`. The user's computer receives the engine from its vendor, with the vendor's license and
  source offer; this repository hosts no engine source. `THIRD-PARTY-NOTICES.md` lists each engine with license,
  official download and source location.
* **7-Zip's unRAR restriction** is repeated in the notices and `engines/7zip/License.txt` ships unchanged.

## Trademark: unresolved, handled conservatively

Upstream states no trademark or branding policy: no `TRADEMARK`/brand file, no such section in its README, LICENSE
or THIRD-PARTY-NOTICES (checked in the v2.0.0 tree). MIT does not grant trademark rights. So:

- every README and package file calls this an **unofficial redistribution**;
- the executable keeps the name `filee-cli` (what programs that look for it expect) and the strings inside the
  program are upstream's; the Filee logo and icons are not used by this repository, and the GUI is not shipped;
- release titles say "filee-cli"; nothing says "Filee <version>" as if it were an official release.

To remove the remaining risk, ask the author: the issue draft is in
[docs/upstream-issue-draft.md](upstream-issue-draft.md) (not posted).

The embedded application icon of `filee-cli.exe` is upstream's `filee.ico` (set by `Filee.Cli.csproj`); it was left
unchanged to avoid a patch. If the author objects, remove it with a patch that clears `ApplicationIcon`.

## Licence of this repository's own files

Scripts, CI, patches and documents here are **MIT** (the default; change `LICENSE` if the owners decide otherwise).
