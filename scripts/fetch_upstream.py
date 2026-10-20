#!/usr/bin/env python3
"""Checks out the pinned upstream commit into .upstream/Filee and applies patches/*.patch.

The tag in UPSTREAM must still point at the pinned commit; otherwise the build stops (a moved tag means the pin
needs a human look). Nothing from upstream is committed to this repository.
"""

import subprocess
import sys

from common import ROOT, UPSTREAM_DIR, BuildError, fail, read_upstream, rmtree, run


def main() -> int:
    up = read_upstream()
    try:
        refs = run(["git", "ls-remote", up["repository"], f"refs/tags/{up['tag']}", f"refs/tags/{up['tag']}^{{}}"],
                   capture=True).splitlines()
        tags = {}
        for line in refs:
            sha, _, ref = line.partition("\t")
            tags[ref] = sha
        tagged = tags.get(f"refs/tags/{up['tag']}^{{}}") or tags.get(f"refs/tags/{up['tag']}")
        if tagged != up["commit"]:
            raise BuildError(f"{up['repository']} tag {up['tag']} points at {tagged}, UPSTREAM pins {up['commit']}")

        rmtree(UPSTREAM_DIR)
        UPSTREAM_DIR.mkdir(parents=True)
        run(["git", "init", "-q", UPSTREAM_DIR])
        run(["git", "-C", UPSTREAM_DIR, "fetch", "-q", "--depth", "1", up["repository"], up["commit"]])
        run(["git", "-C", UPSTREAM_DIR, "checkout", "-q", "--detach", "FETCH_HEAD"])
        head = run(["git", "-C", UPSTREAM_DIR, "rev-parse", "HEAD"], capture=True).strip()
        if head != up["commit"]:
            raise BuildError(f"checked out {head}, expected {up['commit']}")

        patches = sorted((ROOT / "patches").glob("*.patch"))
        for patch in patches:
            run(["git", "-C", UPSTREAM_DIR, "apply", "--check", patch])
            run(["git", "-C", UPSTREAM_DIR, "apply", patch])
        print(f"Upstream {up['tag']} ({head}) in {UPSTREAM_DIR}; patches applied: {[p.name for p in patches] or 'none'}")
        return 0
    except (BuildError, subprocess.SubprocessError) as error:
        return fail(error)


if __name__ == "__main__":
    sys.exit(main())
