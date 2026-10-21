package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Everything in THIRD-PARTY-NOTICES.md is derived from the package itself: the dotnet publish folder and its
// filee-cli.deps.json, the NuGet packages' own metadata, Magick.NET's Notice.txt, the pinned pdfium archive, the crates.io
// files of the Rust components, and the pins in data/. Anything that cannot be attributed to a license and a source
// fails the build.

var licenseFilePattern = regexp.MustCompile(`(?i)^(licen[cs]e|copying|unlicense|notice|copyright|third[-_ ]party[-_ ]notices?)([-_.].*)?$`)

var fileeFiles = map[string]bool{"filee-cli": true, "filee-cli.exe": true, "filee-cli.dll": true, "filee-cli.deps.json": true, "filee-cli.runtimeconfig.json": true}

var spdxWords = map[string]bool{"AND": true, "OR": true, "WITH": true}

var spdxToken = regexp.MustCompile(`[A-Za-z0-9.+-]+`)
var spdxWith = regexp.MustCompile(`\bWITH\s+[A-Za-z0-9.+-]+`)

func nugetRoot() string {
	if v := os.Getenv("NUGET_PACKAGES"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".nuget", "packages")
}

// ---- what is in the publish folder ---------------------------------------------------------------------------------

type depsJSON struct {
	Targets   map[string]map[string]depsTarget `json:"targets"`
	Libraries map[string]struct {
		Type string `json:"type"`
	} `json:"libraries"`
}

type depsTarget struct {
	Runtime   map[string]json.RawMessage `json:"runtime"`
	Native    map[string]json.RawMessage `json:"native"`
	Resources map[string]json.RawMessage `json:"resources"`
}

// fileOwners maps each asset's base name to the library that ships it (the RID-specific target of deps.json).
func fileOwners(deps depsJSON) map[string]string {
	var target map[string]depsTarget
	for _, name := range sortedKeys(deps.Targets) {
		if strings.Contains(name, "/") {
			target = deps.Targets[name]
			break
		}
	}
	owners := map[string]string{}
	for _, lib := range sortedKeys(target) {
		t := target[lib]
		for _, section := range []map[string]json.RawMessage{t.Runtime, t.Native, t.Resources} {
			for _, p := range sortedKeys(section) {
				base := path.Base(p)
				if _, ok := owners[base]; !ok {
					owners[base] = lib
				}
			}
		}
	}
	return owners
}

// classify maps every file of the publish folder to a library; native code that no library accounts for is an error.
func classify(publish string, deps depsJSON) (map[string][]string, error) {
	owners := fileOwners(deps)
	files, err := listFiles(publish)
	if err != nil {
		return nil, err
	}
	present := map[string][]string{}
	var unmapped, nativeUnmapped []string
	for _, rel := range files {
		name := path.Base(rel)
		if fileeFiles[name] || strings.HasSuffix(name, ".pdb") {
			continue
		}
		lib := owners[name]
		if lib == "" {
			if strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".txt") || strings.HasSuffix(name, ".md") {
				continue
			}
			if name == "createdump" || name == "createdump.exe" { // part of the .NET runtime pack
				for _, k := range sortedKeys(deps.Libraries) {
					if strings.HasPrefix(k, "runtimepack.") {
						lib = k
						break
					}
				}
			}
			if lib == "" {
				unmapped = append(unmapped, rel)
				if strings.HasSuffix(name, ".dll") || strings.HasSuffix(name, ".so") || strings.HasSuffix(name, ".dylib") || !strings.Contains(name, ".") {
					nativeUnmapped = append(nativeUnmapped, rel)
				}
				continue
			}
		}
		present[lib] = append(present[lib], rel)
	}
	if len(unmapped) > 0 {
		fmt.Println("Files not attributed to a library by deps.json:\n  " + strings.Join(unmapped, "\n  "))
	}
	if len(nativeUnmapped) > 0 {
		return nil, fmt.Errorf("executable code in the package that deps.json does not account for: %s", strings.Join(nativeUnmapped, ", "))
	}
	return present, nil
}

// ---- NuGet metadata ------------------------------------------------------------------------------------------------

type nuspec struct {
	Metadata struct {
		License struct {
			Type string `xml:"type,attr"`
			Text string `xml:",chardata"`
		} `xml:"license"`
		Copyright  string `xml:"copyright"`
		Authors    string `xml:"authors"`
		ProjectURL string `xml:"projectUrl"`
		Repository struct {
			URL    string `xml:"url,attr"`
			Commit string `xml:"commit,attr"`
		} `xml:"repository"`
	} `xml:"metadata"`
}

type nugetMeta struct {
	License, Copyright, ProjectURL, RepoURL, RepoCommit, Nupkg string
}

func readNuspec(name, version string) (nugetMeta, error) {
	folder := filepath.Join(nugetRoot(), strings.ToLower(name), version)
	specs, _ := filepath.Glob(filepath.Join(folder, "*.nuspec"))
	if len(specs) == 0 {
		return nugetMeta{}, fmt.Errorf("%s %s: no nuspec in %s (restore first)", name, version, folder)
	}
	data, err := os.ReadFile(specs[0])
	if err != nil {
		return nugetMeta{}, err
	}
	var s nuspec
	if err := xml.Unmarshal([]byte(strings.TrimPrefix(string(data), "\ufeff")), &s); err != nil {
		return nugetMeta{}, fmt.Errorf("%s: %w", specs[0], err)
	}
	m := s.Metadata
	meta := nugetMeta{Copyright: strings.TrimSpace(m.Copyright), ProjectURL: strings.TrimSpace(m.ProjectURL),
		RepoURL: strings.TrimSpace(m.Repository.URL), RepoCommit: strings.TrimSpace(m.Repository.Commit)}
	if m.License.Type == "expression" {
		meta.License = strings.TrimSpace(m.License.Text)
	}
	if meta.Copyright == "" {
		meta.Copyright = strings.TrimSpace(m.Authors)
	}
	nupkgs, _ := filepath.Glob(filepath.Join(folder, "*.nupkg"))
	if len(nupkgs) > 0 {
		meta.Nupkg = nupkgs[0]
	}
	return meta, nil
}

type nugetSources struct {
	RepositoryOverride map[string]string                     `json:"repository_override"`
	RefOverride        map[string]struct{ Ref, Note string } `json:"ref_override"`
	LicenseOverride    map[string]string                     `json:"license_override"`
}

type nugetComponent struct {
	Name, Version, License, Copyright, Repo, Ref, RefNote, Nupkg string
}

func normalizeRepo(u string) string {
	u = strings.TrimSpace(u)
	u = strings.Replace(u, "git://", "https://", 1)
	u = strings.TrimSuffix(u, ".git")
	return strings.TrimRight(u, "/")
}

func buildNugetComponent(key string, src nugetSources) (nugetComponent, error) {
	name, version, _ := strings.Cut(key, "/")
	meta, err := readNuspec(name, version)
	if err != nil {
		return nugetComponent{}, err
	}
	repo := meta.RepoURL
	for _, pattern := range sortedKeys(src.RepositoryOverride) {
		if ok, _ := path.Match(pattern, name); ok {
			repo = src.RepositoryOverride[pattern]
			break
		}
	}
	if repo == "" {
		repo = meta.ProjectURL
	}
	ref, note := meta.RepoCommit, ""
	if o, ok := src.RefOverride[name]; ok && ref == "" {
		ref, note = o.Ref, o.Note
	}
	if !strings.Contains(repo, "github.com") && !strings.Contains(repo, "gitlab") && !strings.Contains(repo, "googlesource") {
		return nugetComponent{}, fmt.Errorf("%s: no source repository URL (nuspec repository/projectUrl: %q, %q)", key, meta.RepoURL, meta.ProjectURL)
	}
	if ref == "" {
		return nugetComponent{}, fmt.Errorf("%s: no source commit in the nuspec; add it to data/nuget-sources.json ref_override", key)
	}
	license := src.LicenseOverride[name]
	if license == "" {
		license = meta.License
	}
	if license == "" {
		return nugetComponent{}, fmt.Errorf("%s: no license expression in the nuspec; add it to data/nuget-sources.json license_override", key)
	}
	return nugetComponent{Name: name, Version: version, License: license, Copyright: meta.Copyright,
		Repo: normalizeRepo(repo), Ref: ref, RefNote: note, Nupkg: meta.Nupkg}, nil
}

type namedData struct {
	Name string
	Data []byte
}

func isLicenseFile(base string) bool {
	return licenseFilePattern.MatchString(strings.TrimSuffix(base, filepath.Ext(base))) || licenseFilePattern.MatchString(base)
}

// nupkgLicenseFiles returns the license and notice files at the top of a package (or in its docs/ folder).
func nupkgLicenseFiles(nupkg string) ([]namedData, error) {
	r, err := zip.OpenReader(nupkg)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var out []namedData
	for _, f := range r.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		parent := path.Dir(f.Name)
		if parent != "." && parent != "docs" {
			continue
		}
		if !isLicenseFile(path.Base(f.Name)) {
			continue
		}
		data, err := readZipFile(f)
		if err != nil {
			return nil, err
		}
		out = append(out, namedData{path.Base(f.Name), data})
	}
	return out, nil
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func readZipMember(nupkg, member string) ([]byte, error) {
	r, err := zip.OpenReader(nupkg)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	for _, f := range r.File {
		if f.Name == member {
			return readZipFile(f)
		}
	}
	return nil, fmt.Errorf("%s has no %s", nupkg, member)
}

// ---- Magick.NET delegate libraries ---------------------------------------------------------------------------------

type delegate struct {
	Name, Version, Repository, Tag, Commit, License string
}

var magickEntry = regexp.MustCompile(`(?m)^\[ (\S+) (\S+) \(\d{4}-\d\d-\d\d\) \]$`)

func delegateTable(notice string, known map[string]delegate) ([]delegate, error) {
	notice = strings.ReplaceAll(strings.TrimPrefix(notice, "\ufeff"), "\r\n", "\n")
	var problems []string
	listed := map[string]bool{}
	var rows []delegate
	for _, m := range magickEntry.FindAllStringSubmatch(notice, -1) {
		name, version := m[1], m[2]
		listed[name] = true
		d, ok := known[name]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("Notice.txt lists %s %s, which is not in data/delegates.json", name, version))
		case d.Version != version:
			problems = append(problems, fmt.Sprintf("%s: Notice.txt says %s, data/delegates.json says %s", name, version, d.Version))
		default:
			d.Name = name
			rows = append(rows, d)
		}
	}
	for _, name := range sortedKeys(known) {
		if !listed[name] {
			problems = append(problems, fmt.Sprintf("data/delegates.json lists %s, which Notice.txt no longer contains", name))
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("Magick.NET delegate libraries changed:\n  %s", strings.Join(problems, "\n  "))
	}
	return rows, nil
}

// ---- pdfium ----------------------------------------------------------------------------------------------------------

var pdfiumLibrary = map[string]string{"win-x64": "bin/pdfium.dll", "linux-x64": "lib/libpdfium.so", "linux-arm64": "lib/libpdfium.so",
	"osx-x64": "lib/libpdfium.dylib", "osx-arm64": "lib/libpdfium.dylib"}

type pdfiumInfo struct{ File, URL, Sha256, Version string }

// pdfiumLicenses downloads the pinned pdfium-binaries archive, proves the package's library is byte-identical to the
// one inside, and copies the archive's license files.
func pdfiumLicenses(rid string, pins pinsFile, publish, dest string) (pdfiumInfo, error) {
	asset, ok := pins.Pdfium.Assets[rid]
	if !ok {
		return pdfiumInfo{}, fmt.Errorf("data/pins.json has no pdfium asset for %s", rid)
	}
	url := fmt.Sprintf("%s/releases/download/%s/%s", pins.Pdfium.Repository, pins.Pdfium.Release, asset.File)
	archive, err := download(url, asset.Sha256, filepath.Join(cacheDir(), "downloads"))
	if err != nil {
		return pdfiumInfo{}, err
	}
	f, err := os.Open(archive)
	if err != nil {
		return pdfiumInfo{}, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return pdfiumInfo{}, err
	}
	tr := tar.NewReader(gz)
	want := pdfiumLibrary[rid]
	var libHash, version string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return pdfiumInfo{}, err
		}
		name := strings.TrimPrefix(h.Name, "./")
		if h.Typeflag != tar.TypeReg {
			continue
		}
		switch {
		case name == want:
			hash := sha256.New()
			if _, err := io.Copy(hash, tr); err != nil {
				return pdfiumInfo{}, err
			}
			libHash = hex.EncodeToString(hash.Sum(nil))
		case name == "VERSION":
			data, _ := io.ReadAll(tr)
			var parts []string
			for _, field := range strings.Fields(string(data)) {
				_, v, _ := strings.Cut(field, "=")
				parts = append(parts, v)
			}
			version = strings.Join(parts, ".")
		case name == "LICENSE" || strings.HasPrefix(name, "licenses/"):
			target := filepath.Join(dest, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return pdfiumInfo{}, err
			}
			if err := writeFile(target, tr, 0o644); err != nil {
				return pdfiumInfo{}, err
			}
		}
	}
	if libHash == "" {
		return pdfiumInfo{}, fmt.Errorf("%s has no %s", asset.File, want)
	}
	have, err := sha256File(filepath.Join(publish, path.Base(want)))
	if err != nil {
		return pdfiumInfo{}, err
	}
	if have != libHash {
		return pdfiumInfo{}, fmt.Errorf("%s in the package is not the pdfium build pinned in data/pins.json (%s)", path.Base(want), asset.File)
	}
	return pdfiumInfo{asset.File, url, asset.Sha256, version}, nil
}

// ---- Rust crates ---------------------------------------------------------------------------------------------------

func spdxIDs(expression string) []string {
	var ids []string
	for _, id := range spdxToken.FindAllString(spdxWith.ReplaceAllString(expression, ""), -1) {
		if !spdxWords[id] {
			ids = append(ids, id)
		}
	}
	return ids
}

// deprecatedSPDX maps the deprecated ids some upstream metadata still uses to the ids whose texts SPDX publishes.
var deprecatedSPDX = map[string]string{"GPL-2.0": "GPL-2.0-only", "GPL-3.0": "GPL-3.0-only", "LGPL-2.1": "LGPL-2.1-only", "LGPL-3.0": "LGPL-3.0-only", "AGPL-3.0": "AGPL-3.0-only"}

func spdxText(id string) []byte {
	if to, ok := deprecatedSPDX[id]; ok {
		id = to
	}
	data, err := os.ReadFile(filepath.Join(dataDir(), "license-texts", id+".txt"))
	if err != nil {
		return nil
	}
	return data
}

func crateLicenseFiles(c Crate) ([]namedData, error) {
	url := fmt.Sprintf("https://static.crates.io/crates/%s/%s-%s.crate", c.Name, c.Name, c.Version)
	crate, err := download(url, c.Checksum, filepath.Join(cacheDir(), "crates"))
	if err != nil {
		return nil, err
	}
	f, err := os.Open(crate)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	prefix := c.Name + "-" + c.Version + "/"
	var files []namedData
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return files, nil
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg || !strings.HasPrefix(h.Name, prefix) {
			continue
		}
		rel := h.Name[len(prefix):]
		if strings.Contains(rel, "/") || !isLicenseFile(rel) {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		files = append(files, namedData{rel, data})
	}
}

type crateRow struct {
	Crate
	Note string
}

func writeCrateLicenses(crates []Crate, dest string) ([]crateRow, error) {
	rows := make([]crateRow, len(crates))
	errs := make([]error, len(crates))
	files := make([][]namedData, len(crates))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, c := range crates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			files[i], errs[i] = crateLicenseFiles(c)
		}()
	}
	wg.Wait()
	for i, c := range crates {
		if errs[i] != nil {
			return nil, errs[i]
		}
		folder := filepath.Join(dest, c.Name+"-"+c.Version)
		if err := os.MkdirAll(folder, 0o755); err != nil {
			return nil, err
		}
		note := ""
		if len(files[i]) > 0 {
			for _, f := range files[i] {
				if err := os.WriteFile(filepath.Join(folder, f.Name), f.Data, 0o644); err != nil {
					return nil, err
				}
			}
		} else {
			chosen := ""
			for _, id := range spdxIDs(c.License) {
				if spdxText(id) != nil {
					chosen = id
					break
				}
			}
			if chosen == "" {
				return nil, fmt.Errorf("crate %s %s (%s) has no license file and no SPDX text is in data/license-texts", c.Name, c.Version, c.License)
			}
			if err := os.WriteFile(filepath.Join(folder, chosen+".txt"), spdxText(chosen), 0o644); err != nil {
				return nil, err
			}
			note = "crate has no license file; SPDX text of " + chosen + " supplied"
		}
		rows[i] = crateRow{c, note}
	}
	return rows, nil
}

// ---- rendering -----------------------------------------------------------------------------------------------------

func escapeCell(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "|", `\|`), "\n", " "))
}

func table(header []string, rows [][]string) string {
	var b strings.Builder
	b.WriteString("| " + strings.Join(header, " | ") + " |\n|")
	for range header {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, row := range rows {
		cells := make([]string, len(row))
		for i, c := range row {
			cells[i] = escapeCell(c)
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
	return b.String()
}

func link(url, label string) string {
	if label == "" {
		label = url
	}
	return "[" + label + "](" + url + ")"
}

var hex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)

// treeURL is the web address of a tag or commit in the repository's own host.
func treeURL(repo, ref string) string {
	switch {
	case strings.Contains(repo, "googlesource.com"):
		if hex40.MatchString(ref) {
			return repo + "/+/" + ref
		}
		return repo + "/+/refs/tags/" + ref
	case strings.Contains(repo, "gitlab"):
		return repo + "/-/tree/" + ref
	case strings.Contains(repo, "sourceware.org/git"):
		return repo + "/?p=" + path.Base(repo) + ";a=tree;hb=" + ref
	}
	return repo + "/tree/" + ref
}

func shortRef(ref string) string {
	if hex40.MatchString(ref) {
		return ref[:12]
	}
	return ref
}

func copySpdx(ids map[string]bool, dest string) (written, missing []string, err error) {
	if err = os.MkdirAll(dest, 0o755); err != nil {
		return
	}
	for _, id := range sortedKeys(ids) {
		text := spdxText(id)
		if text == nil {
			missing = append(missing, id)
			continue
		}
		if err = os.WriteFile(filepath.Join(dest, id+".txt"), text, 0o644); err != nil {
			return
		}
		written = append(written, id)
	}
	return
}

type EnginesInfo struct {
	EnginesJSON string
	Rhwp        struct{ URL, Sha256 string }
	SevenZip    struct{ URL, Sha256 string }
	Bundled     []BundledEngine
}

type optionalEngine struct{ Name, License, URL, Source string }

type adaptedItem struct{ Name, What, Where, License, Repository, Ref, Note string }

func generateNotices(stage, publish, rid string, engines EnginesInfo) error {
	up, err := readUpstream()
	if err != nil {
		return err
	}
	pins, err := readPins()
	if err != nil {
		return err
	}
	var sources nugetSources
	var delegates struct{ Components map[string]delegate }
	var optional struct{ Engines map[string]optionalEngine }
	var adapted struct{ Items []adaptedItem }
	var deps depsJSON
	for file, target := range map[string]any{"nuget-sources.json": &sources, "delegates.json": &delegates, "optional-engines.json": &optional, "adapted-code.json": &adapted} {
		if err := readJSON(filepath.Join(dataDir(), file), target); err != nil {
			return err
		}
	}
	if err := readJSON(filepath.Join(publish, "filee-cli.deps.json"), &deps); err != nil {
		return err
	}

	licenses := filepath.Join(stage, "licenses")
	if err := rmtree(licenses); err != nil {
		return err
	}
	if err := os.MkdirAll(licenses, 0o755); err != nil {
		return err
	}
	spdxUsed := map[string]bool{"MIT": true, "LGPL-2.1-or-later": true}
	for _, a := range adapted.Items {
		for _, id := range spdxIDs(a.License) {
			spdxUsed[id] = true
		}
	}

	present, err := classify(publish, deps)
	if err != nil {
		return err
	}
	runtimeKey := ""
	for _, k := range sortedKeys(present) {
		if strings.HasPrefix(k, "runtimepack.") {
			runtimeKey = k
			break
		}
	}
	if runtimeKey == "" {
		return fmt.Errorf("no .NET runtime pack among the package's files")
	}
	var nugetKeys, skipped []string
	for _, k := range sortedKeys(deps.Libraries) {
		if deps.Libraries[k].Type != "package" {
			continue
		}
		if _, ok := present[k]; ok {
			nugetKeys = append(nugetKeys, k)
		} else {
			skipped = append(skipped, k)
		}
	}
	if len(skipped) > 0 {
		fmt.Println("Packages in deps.json with no file in the output (left out of the notices): " + strings.Join(skipped, ", "))
	}

	// .NET runtime
	runtimeName, runtimeVersion, _ := strings.Cut(runtimeKey, "/")
	runtimeName = strings.TrimPrefix(runtimeName, "runtimepack.")
	runtimeMeta, err := readNuspec(runtimeName, runtimeVersion)
	if err != nil {
		return err
	}
	if runtimeMeta.RepoCommit == "" {
		return fmt.Errorf("the .NET runtime pack nuspec names no source commit")
	}
	runtimeFolder := filepath.Join(nugetRoot(), strings.ToLower(runtimeName), runtimeVersion)
	for _, f := range []string{"LICENSE.TXT", "THIRD-PARTY-NOTICES.TXT"} {
		if err := copyFile(filepath.Join(runtimeFolder, f), filepath.Join(licenses, "dotnet-runtime", f)); err != nil {
			return err
		}
	}

	// NuGet packages
	var components []nugetComponent
	var nugetRows [][]string
	for _, key := range nugetKeys {
		c, err := buildNugetComponent(key, sources)
		if err != nil {
			return err
		}
		components = append(components, c)
		folderName := c.Name + "-" + c.Version
		shipped, err := nupkgLicenseFiles(c.Nupkg)
		if err != nil {
			return err
		}
		var names []string
		for _, f := range shipped {
			target := filepath.Join(licenses, "nuget", folderName, f.Name)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(target, f.Data, 0o644); err != nil {
				return err
			}
			names = append(names, "`licenses/nuget/"+folderName+"/"+f.Name+"`")
		}
		filesCell := strings.Join(names, ", ")
		if filesCell == "" {
			filesCell = "SPDX text in `licenses/spdx/`"
		}
		for _, id := range spdxIDs(c.License) {
			spdxUsed[id] = true
		}
		source := link(treeURL(c.Repo, c.Ref), strings.SplitN(c.Repo, "://", 2)[1]+"@"+shortRef(c.Ref))
		if c.RefNote != "" {
			source += " (" + c.RefNote + ")"
		}
		nugetRows = append(nugetRows, []string{c.Name, c.Version, c.License, c.Copyright, source, filesCell})
	}

	// Magick.NET delegates
	var magick *nugetComponent
	for i := range components {
		if components[i].Name == "Magick.NET-Q8-AnyCPU" {
			magick = &components[i]
		}
	}
	if magick == nil {
		return fmt.Errorf("Magick.NET-Q8-AnyCPU is not in the package")
	}
	noticeBytes, err := readZipMember(magick.Nupkg, "Notice.txt")
	if err != nil {
		return err
	}
	delegateRows, err := delegateTable(string(noticeBytes), delegates.Components)
	if err != nil {
		return err
	}
	var delegateCells [][]string
	for _, d := range delegateRows {
		delegateCells = append(delegateCells, []string{d.Name, d.Version, d.License, link(treeURL(d.Repository, d.Tag), d.Tag), d.Commit[:12]})
	}

	pdfium, err := pdfiumLicenses(rid, pins, publish, filepath.Join(licenses, "pdfium"))
	if err != nil {
		return err
	}

	crateSections := map[string][]crateRow{}
	for _, key := range []string{"unhwp", "rhwp"} {
		crates, err := readCrates(key)
		if err != nil {
			return err
		}
		rows, err := writeCrateLicenses(crates, filepath.Join(licenses, "rust", key))
		if err != nil {
			return err
		}
		crateSections[key] = rows
		for _, r := range rows {
			for _, id := range spdxIDs(r.License) {
				spdxUsed[id] = true
			}
		}
	}

	// optional engines: everything Filee can download, whatever the platform (it also finds them on PATH)
	manifest, err := readEnginesManifest()
	if err != nil {
		return err
	}
	downloadable := map[string]bool{}
	for k := range manifest.Components {
		downloadable[k] = true
	}
	for _, p := range manifest.Platforms {
		for k := range p.Components {
			downloadable[k] = true
		}
	}
	var unknown []string
	for _, k := range sortedKeys(downloadable) {
		if _, ok := optional.Engines[k]; !ok && k != "rhwp" && k != "7zip" {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("Filee can download engines this repository does not describe in data/optional-engines.json: %v", unknown)
	}
	var optionalRows [][]string
	bundledIDs := map[string]bool{}
	for _, e := range engines.Bundled {
		bundledIDs[e.ID] = true
	}
	for _, k := range sortedKeys(optional.Engines) {
		if bundledIDs[k] {
			continue
		}
		e := optional.Engines[k]
		source := e.Source
		if strings.HasPrefix(source, "http") {
			source = link(source, "")
		}
		optionalRows = append(optionalRows, []string{e.Name, e.License, link(e.URL, ""), source})
	}

	bundledText, bundledSpdx, err := bundledEngineNotices(rid, up, repoSlug, engines.Bundled)
	if err != nil {
		return err
	}
	for _, id := range bundledSpdx {
		spdxUsed[id] = true
	}
	spdxWritten, spdxMissing, err := copySpdx(spdxUsed, filepath.Join(licenses, "spdx"))
	if err != nil {
		return err
	}
	if len(spdxMissing) > 0 {
		fmt.Println("No SPDX text in data/license-texts for: " + strings.Join(spdxMissing, ", ") + " (their license files come from the packages)")
	}

	var b strings.Builder
	w := func(s string) { b.WriteString(s + "\n") }
	patched := ", unmodified"
	if matches, _ := filepath.Glob(filepath.Join(rootDir, "patches", "*.patch")); len(matches) > 0 {
		patched = " plus the patches listed in `UPSTREAM.md`"
	}
	w(fmt.Sprintf("# Third-party notices: filee-cli %s (%s)\n", up.Version, rid))
	w(fmt.Sprintf("This package is an **unofficial redistribution** of the command line of [Filee](https://github.com/KnifeLemon/Filee) %s (MIT, Copyright (c) 2026 Myeonggu Kim (KnifeLemon) and Filee contributors; see `LICENSE`). It was built from the upstream source%s (see `UPSTREAM.md` for the exact commit). Every component below keeps its own license.\n", up.Tag, patched))
	w("This file was generated by `filee-build` (`cmd/filee-build/notices.go`) from the files of this package: the dotnet publish output and its `filee-cli.deps.json`, the NuGet packages' own metadata, and the pins in `data/`. Components that are not in this package (the GUI, tray, Explorer menu, installers, fonts, icons, optional engines) are not listed as contents. The complete license texts and notice files are in `licenses/`.\n")

	w("## Summary of copyleft components\n")
	if len(engines.Bundled) == 0 {
		w("* **No GPL-only, AGPL or other strong-copyleft component is distributed in this package.** FFmpeg, Pandoc, calibre, Ghostscript, H2Orestart and Eclipse Temurin are *not* included (see \"Optional engines, not distributed here\").\n" +
			"* **LGPL**: 7-Zip (`engines/7zip`) and the ImageMagick delegate libraries statically linked inside `Magick.Native-Q8-*` (libde265, libheif, glib, gdk-pixbuf, pango, librsvg, libcroco, libraw, fribidi, liblqr, cairo and others, table below). Source locations are given; each LGPL library sits inside a separate, replaceable library file (`Magick.Native-Q8-*` or `7zz`/`7z.dll`) that you can rebuild from the sources named below and drop in.\n" +
			"* **MPL-2.0 / MPL-1.1**: OpenMcdf (MPL-2.0) and UTF.Unknown (MPL-1.1, alternatively GPL-2.0-or-later or LGPL-2.1-or-later; used here under MPL-1.1) are unmodified binaries; their source is at the URLs in the NuGet table. Cairo (inside Magick.Native) is LGPL-2.1 or MPL-1.1; used under the LGPL.\n")
	} else {
		var gpl, agpl, lgpl []string
		all, err := readEngineSources()
		if err != nil {
			return err
		}
		for _, e := range engines.Bundled {
			es := all[e.ID]
			ids := strings.Join(spdxIDs(es.License), " ")
			switch {
			case strings.Contains(ids, "AGPL-"):
				agpl = append(agpl, es.Name)
			case strings.Contains(ids, "GPL-") && !strings.Contains(ids, "LGPL-"):
				gpl = append(gpl, es.Name)
			}
		}
		for _, e := range engines.Bundled {
			if e.ID == "libreoffice" {
				lgpl = append(lgpl, all[e.ID].Name+" (MPL-2.0 core with LGPL, GPL and other components)")
			}
		}
		line := func(label string, names []string) string {
			if len(names) == 0 {
				return "* **" + label + "**: none in this package.\n"
			}
			return "* **" + label + "**: " + strings.Join(names, ", ") + " (see \"Bundled third-party engines\": separate programs, source offered below).\n"
		}
		w(line("AGPL", agpl) + line("GPL", gpl) + line("MPL / mixed", lgpl) +
			"* **LGPL**: 7-Zip (`engines/7zip`) and the ImageMagick delegate libraries statically linked inside `Magick.Native-Q8-*` (table below), each in a separate, replaceable library file or program. Source locations are given.\n" +
			"* **MPL-2.0 / MPL-1.1**: OpenMcdf (MPL-2.0) and UTF.Unknown (MPL-1.1; used here under MPL-1.1) are unmodified binaries; their source is at the URLs in the NuGet table. Cairo (inside Magick.Native) is LGPL-2.1 or MPL-1.1; used under the LGPL.\n" +
			"* GPL/AGPL programs are **not linked** into `filee-cli`: it starts them as separate processes with command-line arguments, as Filee does.\n")
	}

	w("## Filee\n")
	w(table([]string{"Component", "License", "Source"}, [][]string{
		{fmt.Sprintf("Filee %s (`filee-cli`, Filee.Core, Filee.Engines)", up.UpstreamVersion), "MIT", link(treeURL(up.Repository, up.Commit), fmt.Sprintf("%s (%s)", up.Tag, up.Commit[:12]))}}))
	w("Third-party work adapted into Filee's own source files (found in the upstream tree at the pinned tag):\n")
	var adaptedRows [][]string
	for _, a := range adapted.Items {
		u := a.Repository
		if a.Ref != "main" {
			u = treeURL(a.Repository, a.Ref)
		}
		adaptedRows = append(adaptedRows, []string{a.Name, a.What, a.Where, a.License, link(u, "")})
	}
	w(table([]string{"Project", "What", "Where in Filee", "License", "Source"}, adaptedRows))

	w("## .NET runtime (self-contained)\n")
	w(table([]string{"Component", "Version", "License", "Source", "Notice files"}, [][]string{
		{runtimeName, runtimeVersion, "MIT", link(treeURL("https://github.com/dotnet/dotnet", runtimeMeta.RepoCommit), "dotnet/dotnet@"+runtimeMeta.RepoCommit[:12]),
			"`licenses/dotnet-runtime/LICENSE.TXT`, `licenses/dotnet-runtime/THIRD-PARTY-NOTICES.TXT`"}}))

	w("## NuGet libraries (from `filee-cli.deps.json`, restricted to files present in this package)\n")
	w(table([]string{"Package", "Version", "License", "Copyright", "Source (commit or tag)", "License files"}, nugetRows))

	w("## Native code inside NuGet packages\n")
	w("### Magick.Native (ImageMagick) delegate libraries\n")
	mn := pins.MagickNative
	w(fmt.Sprintf("`Magick.Native-Q8-*` is built by [dlemstra/Magick.Native](%s) ([%s](%s), commit %s) with the libraries below linked in statically. The list is the one in the `Notice.txt` of the Magick.NET-Q8-AnyCPU package (`licenses/nuget/Magick.NET-Q8-AnyCPU-*/Notice.txt`, which holds every license text); the build fails when it differs from `data/delegates.json`. Versions and tags are the ones that file names; the license column is what each project declares.\n",
		mn.Repository, mn.Tag, treeURL(mn.Repository, mn.Tag), mn.Commit[:12]))
	w(table([]string{"Library", "Version", "License", "Source tag", "Commit"}, delegateCells))
	w("### PDFium\n")
	w(fmt.Sprintf("`%s` is byte-identical to the one in [%s](%s) (SHA-256 `%s`), build %s of [bblanchon/pdfium-binaries](%s) (Apache-2.0; PDFium is BSD-3-Clause/Apache-2.0). Its own and its dependencies' licenses are in `licenses/pdfium/`.\n",
		path.Base(pdfiumLibrary[rid]), pdfium.File, pdfium.URL, pdfium.Sha256, pdfium.Version, pins.Pdfium.Repository))
	w("### libunhwp (Unhwp)\n")
	w(fmt.Sprintf("`libunhwp` / `unhwp.dll` comes from the Unhwp NuGet package (MIT), built from %s (version %s, `cargo build --release` with the `ffi` feature). Rust crates in it (%d, superset over all platforms; license files in `licenses/rust/unhwp/`):\n",
		link(treeURL(pins.Unhwp.Repository, pins.Unhwp.Commit), "iyulab/unhwp@"+pins.Unhwp.Commit[:12]), pins.Unhwp.Version, len(crateSections["unhwp"])))
	w(crateTable(crateSections["unhwp"]))
	w("### SkiaSharp and HarfBuzzSharp natives\n")
	w("`libSkiaSharp` and `libHarfBuzzSharp` come from the SkiaSharp / HarfBuzzSharp NuGet packages (MIT). Their own THIRD-PARTY-NOTICES (FreeType, libpng, zlib, libjpeg-turbo, ICU, HarfBuzz, Expat, WebP, ...) are in `licenses/nuget/SkiaSharp.NativeAssets.*` and `licenses/nuget/HarfBuzzSharp.NativeAssets.*`.\n")

	w("## Bundled engines (`engines/`)\n")
	var rhwpSource string
	if strings.HasPrefix(rid, "linux-") {
		rhwpSource = fmt.Sprintf("built from source on Ubuntu 22.04 by this repository's build: tag `%s`, commit `%s`, `cargo +%s build --release --locked --bin rhwp`", pins.Rhwp.Tag, pins.Rhwp.Commit, pins.Rhwp.RustToolchain)
	} else {
		rhwpSource = fmt.Sprintf("rhwp's own release build of tag `%s` (%s)", pins.Rhwp.Tag, engines.Rhwp.URL)
	}
	w(table([]string{"Engine", "Version", "License", "Source"}, [][]string{
		{"rhwp (`engines/rhwp`)", pins.Rhwp.Version, "MIT", link(treeURL(pins.Rhwp.Repository, pins.Rhwp.Commit), "edwardkim/rhwp@"+pins.Rhwp.Commit[:12]) + "; " + rhwpSource},
		{"7-Zip (`engines/7zip`)", pins.SevenZip.Version, pins.SevenZip.License, link(treeURL(pins.SevenZip.Repository, pins.SevenZip.Tag), "ip7z/7zip tag "+pins.SevenZip.Tag) + fmt.Sprintf(" (commit %s); binary from %s", pins.SevenZip.Commit[:12], engines.SevenZip.URL)}}))
	w("7-Zip is Copyright (C) 1999-2026 Igor Pavlov; its `License.txt` is in `engines/7zip`. The RAR decompression code in 7-Zip is under the unRAR license restriction: it may be used to unpack RAR archives, but not to develop a RAR (WinRAR) compatible archiver or to re-create the RAR compression algorithm. Filee only reads RAR files.\n")
	w(fmt.Sprintf("rhwp's `LICENSE` is in `engines/rhwp`. Rust crates in rhwp (%d, superset over all platforms; license files in `licenses/rust/rhwp/`):\n", len(crateSections["rhwp"])))
	w(crateTable(crateSections["rhwp"]))
	w("rhwp does not embed font files in its release binary; it uses fonts installed on the computer (the fonts in the rhwp repository are web/test assets that are not part of the program).\n")

	if bundledText != "" {
		w(bundledText)
	}
	w("## Optional engines, not distributed here\n")
	if len(optionalRows) == 0 {
		w("Every engine Filee can use on this platform is bundled in this package (see above).\n")
	} else {
		w("Filee can also use these programs when they are installed on the computer (found on `PATH` or in the usual locations; `filee-cli` has no command to download them). They are separate programs under their own licenses, which are mostly GPL/AGPL; **none of the engines in this table is in this package**. Obtain them from the official sources below.\n")
		w(table([]string{"Engine", "License", "Official download", "Source code"}, optionalRows))
	}

	w("## License texts\n")
	w("Full texts: `LICENSE` (Filee, MIT), `licenses/spdx/` (" + strings.Join(spdxWritten, ", ") + "), `licenses/nuget/`, `licenses/pdfium/`, `licenses/dotnet-runtime/`, `licenses/rust/`, `engines/7zip/License.txt`, `engines/rhwp/LICENSE`, and the license files each bundled engine ships in its own folder under `engines/`.\n")

	if err := os.WriteFile(filepath.Join(stage, "THIRD-PARTY-NOTICES.md"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	fmt.Printf("THIRD-PARTY-NOTICES.md: %d NuGet packages, %d delegate libraries, %d + %d crates, %d optional engines listed\n",
		len(components), len(delegateRows), len(crateSections["unhwp"]), len(crateSections["rhwp"]), len(optionalRows))
	return nil
}

func crateTable(rows []crateRow) string {
	cells := make([][]string, len(rows))
	for i, r := range rows {
		cells[i] = []string{r.Name, r.Version, r.License, r.Checksum[:16] + "…", r.Note}
	}
	return table([]string{"Crate", "Version", "License", "crates.io SHA-256 (= Cargo.lock)", "Note"}, cells)
}
