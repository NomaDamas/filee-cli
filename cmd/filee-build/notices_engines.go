package main

import (
	"fmt"
	"sort"
	"strings"
)

// repoSlug is where releases and source assets are published.
const repoSlug = "NomaDamas/filee-cli"

// isCopyleft reports the bundled engines whose license is a (L)GPL/AGPL one, i.e. the ones the written source
// offer is about.
func isCopyleft(license string) bool {
	for _, id := range spdxIDs(license) {
		if strings.HasPrefix(id, "GPL-") || strings.HasPrefix(id, "LGPL-") || strings.HasPrefix(id, "AGPL-") || strings.HasPrefix(id, "MPL-") {
			return true
		}
	}
	return false
}

// bundledEngineNotices renders the "Bundled third-party engines" part of THIRD-PARTY-NOTICES.md for one package and
// returns the SPDX ids whose texts must ship in licenses/spdx. Every engine needs a data/engine-sources pin; a bundled
// engine without one fails the build.
func bundledEngineNotices(rid string, up Upstream, repoSlug string, bundled []BundledEngine) (text string, spdx []string, err error) {
	if len(bundled) == 0 {
		return "", nil, nil
	}
	all, err := readEngineSources()
	if err != nil {
		return "", nil, err
	}
	var b strings.Builder
	var rows, hostedRows [][]string
	used := map[string]bool{}
	for _, e := range bundled {
		es, ok := all[e.ID]
		if !ok {
			return "", nil, fmt.Errorf("bundled engine %s has no data/engine-sources/%s.json", e.ID, e.ID)
		}
		for _, id := range spdxIDs(es.License) {
			used[id] = true
		}
		if strings.Contains(es.License, "Classpath-exception-2.0") {
			used["Classpath-exception-2.0"] = true
		}
		for _, s := range es.Sources {
			for _, id := range spdxIDs(s.License) {
				if strings.HasPrefix(id, "GPL-") || strings.HasPrefix(id, "LGPL-") || strings.HasPrefix(id, "AGPL-") || strings.HasPrefix(id, "MPL-") {
					used[id] = true
				}
			}
		}
		where := "`engines/" + e.ID + "`"
		if e.ID == "vcruntime" {
			where = "DLLs inside the conda engine folders (`engines/ghostscript`)"
		}
		if e.ID == "h2orestart" {
			where = "`engines/libreoffice/share/extensions/H2Orestart`"
		}
		binary := fmt.Sprintf("[%s](%s), SHA-256 `%s`", baseName(e.URL), e.URL, e.Sha256)
		if e.Override != "" {
			binary += "; **not the file upstream Filee pins**: " + e.Override
		}
		var source string
		switch {
		case es.HostSource:
			source = fmt.Sprintf("**Hosted by this project**: release asset `%s` (%d pinned sources, written offer inside; see below)", sourceArchiveName(up, e.ID), len(es.Sources))
			hostedRows = append(hostedRows, []string{es.Name, es.License, "`" + sourceArchiveName(up, e.ID) + "`"})
		case len(es.Sources) == 0:
			source = "none exists (proprietary redistributable)"
		default:
			var parts []string
			for _, s := range es.Sources {
				if s.Kind == "git" {
					parts = append(parts, link(s.URL+"/tree/"+s.Ref, s.Name+"@"+s.Ref[:12]))
				} else {
					parts = append(parts, link(s.URL, s.Name+" "+s.Version))
				}
			}
			source = "Upstream's own source (not mirrored here): " + strings.Join(parts, ", ")
		}
		rows = append(rows, []string{es.Name, e.Version, es.License, where, binary, source})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })
	fmt.Fprintf(&b, "## Bundled third-party engines (%s)\n\n", rid)
	b.WriteString("These conversion engines are included **unmodified** (the vendor's own binaries, checked against the SHA-256 in the table). They are the files upstream Filee pins for this platform, except where the table says **not the file upstream Filee pins** (FFmpeg on Windows, and FFmpeg and Ghostscript on macOS, where Filee has no download and looks for a copy on the `PATH`). They run as separate programs started by `filee-cli`; `filee-cli` links none of their code. Each keeps its own license, and each is optional for Filee: removing its folder under `engines/` disables only the conversions it provided.\n\n")
	b.WriteString(table([]string{"Engine", "Version", "License", "Location", "Binary (download, SHA-256)", "Source code"}, rows))
	b.WriteString("\n")
	if len(hostedRows) > 0 {
		b.WriteString("### Source code and written offer\n\n")
		fmt.Fprintf(&b, "For the engines under GPL, LGPL or AGPL the Corresponding Source is provided in the same GitHub release as this package (%s), as the assets named below. Each archive holds `SOURCES.md` (every component with version, license, URL, commit or SHA-256, and how the binary was built) and the pinned sources themselves: upstream tarballs byte-for-byte, git trees at the exact commit. Machine-readable pins are in `data/engine-sources/` of this repository at the release tag.\n\n", "https://github.com/"+repoSlug+"/releases/tag/v"+up.Version)
		b.WriteString(table([]string{"Engine", "License", "Release asset"}, hostedRows))
		b.WriteString("\n**Written offer.** For at least three years after the release of this package, and for as long as this project distributes it, anyone who has received this package can obtain the Corresponding Source of those engines from the release assets above at no charge beyond the cost of the download, or by writing an issue at https://github.com/" + repoSlug + "/issues. Where the source assets for an engine carry an `unresolved` note in `SOURCES.md`, that note states exactly what could not be pinned and why; it is not hidden.\n\n")
	}
	return b.String(), keys(used), nil
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func baseName(url string) string {
	if i := strings.LastIndex(url, "/"); i >= 0 {
		url = url[i+1:]
	}
	return strings.ReplaceAll(url, "%2B", "+")
}
