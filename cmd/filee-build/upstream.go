package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// cmdFetch checks out the pinned upstream commit into .upstream/Filee and applies patches/*.patch. The tag must still
// point at the pinned commit; a moved tag stops the build. Nothing from upstream is committed to this repository.
func cmdFetch(args []string) error {
	up, err := readUpstream()
	if err != nil {
		return err
	}
	out, err := run(runOpts{Capture: true}, "git", "ls-remote", up.Repository, "refs/tags/"+up.Tag, "refs/tags/"+up.Tag+"^{}")
	if err != nil {
		return err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		sha, ref, ok := strings.Cut(line, "\t")
		if ok {
			refs[ref] = sha
		}
	}
	tagged := refs["refs/tags/"+up.Tag+"^{}"]
	if tagged == "" {
		tagged = refs["refs/tags/"+up.Tag]
	}
	if tagged != up.Commit {
		return fmt.Errorf("%s tag %s points at %q, UPSTREAM pins %s", up.Repository, up.Tag, tagged, up.Commit)
	}
	dir := upstreamDir()
	if err := rmtree(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := gitCheckout(dir, up.Repository, up.Commit); err != nil {
		return err
	}
	patches, _ := filepath.Glob(filepath.Join(rootDir, "patches", "*.patch"))
	for _, p := range patches {
		if _, err := run(runOpts{}, "git", "-C", dir, "apply", "--check", p); err != nil {
			return err
		}
		if _, err := run(runOpts{}, "git", "-C", dir, "apply", p); err != nil {
			return err
		}
	}
	names := "none"
	if len(patches) > 0 {
		var n []string
		for _, p := range patches {
			n = append(n, filepath.Base(p))
		}
		names = strings.Join(n, ", ")
	}
	fmt.Printf("Upstream %s (%s) in %s; patches applied: %s\n", up.Tag, up.Commit, dir, names)
	return nil
}

// gitCheckout makes dir a shallow checkout of exactly one commit (GitHub serves any reachable commit by hash).
func gitCheckout(dir, repository, commit string) error {
	if _, err := run(runOpts{}, "git", "init", "-q", dir); err != nil {
		return err
	}
	if _, err := run(runOpts{}, "git", "-C", dir, "fetch", "-q", "--depth", "1", repository, commit); err != nil {
		return err
	}
	if _, err := run(runOpts{}, "git", "-C", dir, "checkout", "-q", "--detach", "FETCH_HEAD"); err != nil {
		return err
	}
	head, err := run(runOpts{Capture: true, Quiet: true}, "git", "-C", dir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(head) != commit {
		return fmt.Errorf("%s: checked out %s, expected %s", repository, strings.TrimSpace(head), commit)
	}
	return nil
}
