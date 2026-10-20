#!/usr/bin/env python3
"""Collects the five release archives, writes SHA256SUMS and the release notes.

Usage: prepare_release.py DIR [--tag TAG]
DIR holds exactly the five archives named by UPSTREAM (filee-cli-<version>-<rid>.tar.gz / .zip). Fails when one is
missing, when there is an unexpected file, or when --tag is not v<version>.
"""

import argparse
import sys
from pathlib import Path

from common import RIDS, BuildError, archive_name, fail, read_upstream, sha256_file

NOTES = """\
# filee-cli {version}

**Unofficial redistribution** of the command line of [Filee]({repository}) {tag} (MIT). Not made or endorsed by the
Filee author. Built from the unmodified upstream source at commit `{commit}`; the scripts that build these packages are
in this repository. No GUI, tray, Explorer menu or installer is included.

| Asset | SHA-256 |
|---|---|
{rows}

Verify a download (`SHA256SUMS` is in this release):

```sh
sha256sum --check --ignore-missing SHA256SUMS      # Linux
shasum -a 256 --check --ignore-missing SHA256SUMS  # macOS
```

```powershell
(Get-FileHash .\\filee-cli-{version}-win-x64.zip -Algorithm SHA256).Hash.ToLower()   # compare with the table
```

Each archive has one top-level folder `filee-cli-{version}-<rid>/` with `filee-cli` (`filee-cli.exe` on Windows),
`engines/rhwp`, `engines/7zip`, `LICENSE`, `THIRD-PARTY-NOTICES.md` and `UPSTREAM.md`.
{attestation}"""

ATTESTATION = """
Build provenance (public repositories only): `gh attestation verify <asset> --repo {repository_slug}`
"""


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("directory", type=Path)
    parser.add_argument("--tag")
    parser.add_argument("--slug", default="", help="owner/repo, to print the attestation verification command")
    options = parser.parse_args()
    try:
        up = read_upstream()
        expected = [archive_name(rid) for rid in RIDS]
        if options.tag and options.tag != f"v{up['version']}":
            raise BuildError(f"tag {options.tag} does not match UPSTREAM (expected v{up['version']})")
        present = sorted(p.name for p in options.directory.iterdir() if p.is_file() and p.name != "SHA256SUMS" and not p.name.startswith("RELEASE_NOTES"))
        missing = sorted(set(expected) - set(present))
        extra = sorted(set(present) - set(expected))
        if missing or extra:
            raise BuildError(f"release assets differ from the expected five. missing: {missing}; unexpected: {extra}")
        sums = [(sha256_file(options.directory / name), name) for name in expected]
        (options.directory / "SHA256SUMS").write_text("".join(f"{h}  {n}\n" for h, n in sums), encoding="ascii", newline="\n")
        rows = "\n".join(f"| `{n}` | `{h}` |" for h, n in sums)
        notes = NOTES.format(version=up["version"], tag=up["tag"], repository=up["repository"], commit=up["commit"], rows=rows,
                             attestation=ATTESTATION.format(repository_slug=options.slug) if options.slug else "")
        (options.directory / "RELEASE_NOTES.md").write_text(notes, encoding="utf-8", newline="\n")
        print("\n".join(f"{h}  {n}" for h, n in sums))
        return 0
    except BuildError as error:
        return fail(error)


if __name__ == "__main__":
    sys.exit(main())
