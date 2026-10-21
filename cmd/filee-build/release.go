package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const notesTemplate = "# filee-cli %[1]s\n\n" +
	"**Unofficial redistribution** of the command line of [Filee](%[2]s) %[3]s (MIT). Not made or endorsed by the\n" +
	"Filee author. Built from the unmodified upstream source at commit `%[4]s`; the program that builds these packages is\n" +
	"in this repository. No GUI, tray, Explorer menu or installer is included.\n\n" +
	"| Asset | SHA-256 |\n|---|---|\n%[5]s\n\n" +
	"Verify a download (`SHA256SUMS` is in this release):\n\n" +
	"```sh\nsha256sum --check --ignore-missing SHA256SUMS      # Linux\nshasum -a 256 --check --ignore-missing SHA256SUMS  # macOS\n```\n\n" +
	"```powershell\n(Get-FileHash .\\filee-cli-%[1]s-win-x64.zip -Algorithm SHA256).Hash.ToLower()   # compare with the table\n```\n\n" +
	"Each archive has one top-level folder `filee-cli-%[1]s-<rid>/` with `filee-cli` (`filee-cli.exe` on Windows),\n" +
	"`engines/rhwp`, `engines/7zip`, `LICENSE`, `THIRD-PARTY-NOTICES.md` and `UPSTREAM.md`.\n%[6]s"

// cmdRelease writes SHA256SUMS and RELEASE_NOTES.md for exactly the five archives, failing on a missing or extra file or a
// tag that is not v<upstream version>-<revision>.
func cmdRelease(args []string) error {
	fl := newFlags("release")
	tag := fl.String("tag", "", "release tag, must equal v<version> of UPSTREAM")
	slug := fl.String("slug", "", "owner/repo, to print the attestation verification command")
	if err := fl.Parse(reorder(args)); err != nil {
		return err
	}
	if fl.NArg() != 1 {
		return fmt.Errorf("usage: release DIR [--tag T] [--slug owner/repo]")
	}
	dir := fl.Arg(0)
	up, err := readUpstream()
	if err != nil {
		return err
	}
	if *tag != "" && *tag != "v"+up.Version {
		return fmt.Errorf("tag %s does not match UPSTREAM (expected v%s)", *tag, up.Version)
	}
	expected := map[string]bool{}
	for _, rid := range rids {
		expected[archiveName(up, rid)] = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	present := map[string]bool{}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || n == "SHA256SUMS" || strings.HasPrefix(n, "RELEASE_NOTES") {
			continue
		}
		present[n] = true
	}
	var missing, extra []string
	for n := range expected {
		if !present[n] {
			missing = append(missing, n)
		}
	}
	for n := range present {
		if !expected[n] {
			extra = append(extra, n)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		return fmt.Errorf("release assets differ from the expected five. missing: %v; unexpected: %v", missing, extra)
	}
	var sums, rows strings.Builder
	for _, rid := range rids {
		name := archiveName(up, rid)
		h, err := sha256File(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		fmt.Fprintf(&sums, "%s  %s\n", h, name)
		fmt.Fprintf(&rows, "| `%s` | `%s` |\n", name, h)
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()), 0o644); err != nil {
		return err
	}
	attestation := ""
	if *slug != "" {
		attestation = fmt.Sprintf("\nBuild provenance (public repositories only): `gh attestation verify <asset> --repo %s`\n", *slug)
	}
	notes := fmt.Sprintf(notesTemplate, up.Version, up.Repository, up.Tag, up.Commit, strings.TrimRight(rows.String(), "\n"), attestation)
	if err := os.WriteFile(filepath.Join(dir, "RELEASE_NOTES.md"), []byte(notes), 0o644); err != nil {
		return err
	}
	fmt.Print(sums.String())
	return nil
}

// reorder moves flags before positional arguments, so `release DIR --tag T` works with the flag package.
func reorder(args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			if !strings.Contains(args[i], "=") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		} else {
			positional = append(positional, args[i])
		}
	}
	return append(flags, positional...)
}
