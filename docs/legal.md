# Licensing and trademark notes

## What this repository redistributes

Binary packages of `Filee.Cli` (MIT) built from the unmodified upstream source at a pinned commit, with
[rhwp](https://github.com/edwardkim/rhwp) (MIT) and [7-Zip](https://github.com/ip7z/7zip) (LGPL-2.1+) next to it.
Every package carries `LICENSE` (upstream's MIT text with its copyright line intact; one more line covers this
repository's own scripts), `THIRD-PARTY-NOTICES.md`, `licenses/` and `UPSTREAM.md`.

* **Corresponding source** for LGPL/MPL parts is named with tag or commit URLs in `THIRD-PARTY-NOTICES.md`
  (7-Zip, the ImageMagick delegate libraries inside Magick.Native, OpenMcdf, UTF.Unknown).
* **GPL, LGPL and AGPL engines are bundled** (Pandoc, FFmpeg, calibre, Ghostscript, H2Orestart, Temurin; LibreOffice is
  MPL-2.0). They are unmodified vendor binaries started as separate processes, never linked into `filee-cli`. Their
  Corresponding Source is hosted in the release (`filee-cli-<version>-source-<engine>.tar.gz`) from the pins in
  `data/engine-sources/`; LibreOffice's source is named by URL and SHA-256 (MPL source availability), and the Microsoft
  VC++ runtime has no source. Anything that could not be pinned is stated in the `unresolved` lists, never hidden.
* **7-Zip's unRAR restriction** is repeated in the notices and `engines/7zip/License.txt` ships unchanged.

## Trademark: unresolved, handled conservatively

Upstream states no trademark or branding policy: no `TRADEMARK`/brand file, no such section in its README, LICENSE
or THIRD-PARTY-NOTICES (checked in the v1.12.0 tree). MIT does not grant trademark rights. So:

- every README and package file calls this an **unofficial redistribution**;
- the executable keeps the name `filee-cli` (what librarian and Dazzi search for) and the strings inside the
  program are upstream's; the Filee logo and icons are not used by this repository, and the GUI is not shipped;
- release titles say "filee-cli"; nothing says "Filee <version>" as if it were an official release.

To remove the remaining risk, ask the author: the issue draft is in
[docs/upstream-issue-draft.md](upstream-issue-draft.md) (not posted).

The embedded application icon of `filee-cli.exe` is upstream's `filee.ico` (set by `Filee.Cli.csproj`); it was left
unchanged to avoid a patch. If the author objects, remove it with a patch that clears `ApplicationIcon`.

## Licence of this repository's own files

Scripts, CI, patches and documents here are **MIT** (the default; change `LICENSE` if the owners decide otherwise).
