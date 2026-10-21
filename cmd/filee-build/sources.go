package main

// sources.go builds the Corresponding Source bundles for the GPL/AGPL/LGPL engines that the filee-cli release
// archives bundle. The pins live in data/engine-sources/<engine>.json; for every engine with host_source=true this
// writes one reproducible tar.gz whose single top-level folder filee-cli-<version>-source-<engine>/ holds SOURCES.md
// (the written source offer: shipped binaries, build recipe, every pinned source, unresolved caveats) plus one folder
// per pinned source. http sources are stored as the original file; git and svn sources are exported as a clean tree
// with the VCS metadata removed. Fetched git/svn trees are cached under .cache/sources so reruns are cheap.

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// sourcePartLimit is the largest part size used when an archive has to be split: 1.9 GiB, safely under GitHub's 2 GiB
// per-asset limit.
const sourcePartLimit = int64(2040109465)

// engineBinary is one entry of the "binaries" map of data/engine-sources/<engine>.json, keyed by RID.
type engineBinary struct {
	Version      string   `json:"version"`
	URL          string   `json:"url"`
	SHA256       string   `json:"sha256"`
	Size         int64    `json:"size"`
	Note         string   `json:"note"`
	LicenseFile  string   `json:"license_file"`
	LicenseFiles []string `json:"license_files"`
}

// engineSource is one entry of the "sources" list of data/engine-sources/<engine>.json.
type engineSource struct {
	Name       string   `json:"name"`
	Version    string   `json:"version"`
	License    string   `json:"license"`
	Kind       string   `json:"kind"`
	URL        string   `json:"url"`
	Ref        string   `json:"ref"`
	SHA256     string   `json:"sha256"`
	Size       int64    `json:"size"`
	Submodules bool     `json:"submodules"`
	Platforms  []string `json:"platforms"`
	Note       string   `json:"note"`
	Mirrors    []string `json:"mirrors"` // git: other URLs holding the same commit, tried when url fails
}

// EngineSources is one parsed data/engine-sources/<engine>.json.
type EngineSources struct {
	Engine      string                  `json:"engine"`
	Name        string                  `json:"name"`
	License     string                  `json:"license"`
	LicenseNote string                  `json:"license_note"`
	Binaries    map[string]engineBinary `json:"binaries"`
	HostSource  bool                    `json:"host_source"`
	BuildRecipe string                  `json:"build_recipe"`
	Sources     []engineSource          `json:"sources"`
	Unresolved  []string                `json:"unresolved"`
	Notes       string                  `json:"notes"`
}

var (
	gitRefRe = regexp.MustCompile(`^[0-9a-f]{40}$`)
	svnRefRe = regexp.MustCompile(`^[0-9]+$`)
	sha256Re = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
)

// readEngineSources reads every data/engine-sources/*.json, keyed by the engine id in the file.
func readEngineSources() (map[string]EngineSources, error) {
	dir := filepath.Join(dataDir(), "engine-sources")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string]EngineSources{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var es EngineSources
		if err := readJSON(filepath.Join(dir, e.Name()), &es); err != nil {
			return nil, err
		}
		id := strings.TrimSpace(es.Engine)
		if id == "" {
			id = strings.TrimSuffix(e.Name(), ".json")
		}
		out[id] = es
	}
	return out, nil
}

// hostedSourceEngines returns the sorted ids of the engines whose Corresponding Source has to be hosted.
func hostedSourceEngines() ([]string, error) {
	all, err := readEngineSources()
	if err != nil {
		return nil, err
	}
	var ids []string
	for id, es := range all {
		if es.HostSource {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func sourceArchiveName(up Upstream, engine string) string {
	return "filee-cli-" + up.Version + "-source-" + engine + ".tar.gz"
}

// sourceEntry is one member of the produced tar.gz.
type sourceEntry struct {
	name string // slash-separated path inside the archive; directories end with "/"
	typ  byte   // tar.TypeDir, tar.TypeReg or tar.TypeSymlink
	mode int64
	link string
	size int64
	src  string // file on disk (regular files only)
	data []byte // inline content, instead of src
}

// cmdSources writes the Corresponding Source archives. Flags: --dist DIR (default <root>/dist/sources),
// --engines a,b (default: every engine with host_source=true).
func cmdSources(args []string) error {
	if rootDir == "" {
		if err := findRoot(); err != nil {
			return err
		}
	}
	fl := newFlags("sources")
	dist := fl.String("dist", filepath.Join(rootDir, "dist", "sources"), "directory for the source archives")
	engines := fl.String("engines", "", "comma-separated engine ids (default: every engine with host_source=true)")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if fl.NArg() != 0 {
		return fmt.Errorf("usage: sources [--dist DIR] [--engines a,b] (unexpected argument %q)", fl.Arg(0))
	}
	up, err := readUpstream()
	if err != nil {
		return err
	}
	all, err := readEngineSources()
	if err != nil {
		return err
	}
	var ids []string
	if *engines != "" {
		for _, id := range strings.Split(*engines, ",") {
			if id = strings.TrimSpace(id); id != "" {
				ids = append(ids, id)
			}
		}
	} else {
		if ids, err = hostedSourceEngines(); err != nil {
			return err
		}
	}
	if len(ids) == 0 {
		return errors.New("no engine to build: none has host_source=true and --engines was empty")
	}
	for _, id := range ids {
		if _, ok := all[id]; !ok {
			return fmt.Errorf("unknown engine %q (no data/engine-sources/%s.json)", id, id)
		}
	}
	if err := os.MkdirAll(*dist, 0o755); err != nil {
		return err
	}
	epoch := sourceEpoch(up)
	var failed []string
	for _, id := range ids {
		if err := buildEngineSource(up, id, all[id], *dist, epoch); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", id, err))
		}
	}
	if len(failed) != 0 {
		return fmt.Errorf("sources: %d of %d engines failed:\n%s", len(failed), len(ids), strings.Join(failed, "\n"))
	}
	return nil
}

// sourceEpoch is the fixed mtime stored in every archive member: SOURCE_DATE_EPOCH when set, else the commit time of
// the pinned upstream commit, else the Unix epoch.
func sourceEpoch(up Upstream) int64 {
	if v := os.Getenv("SOURCE_DATE_EPOCH"); v != "" {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return n
		}
	}
	if out, err := run(runOpts{Dir: upstreamDir(), Capture: true, Quiet: true}, "git", "show", "-s", "--format=%ct", up.Commit); err == nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64); err == nil {
			return n
		}
	}
	return 0
}

// buildEngineSource writes one archive for one engine.
func buildEngineSource(up Upstream, engine string, es EngineSources, dist string, epoch int64) error {
	top := "filee-cli-" + up.Version + "-source-" + engine
	entries := []sourceEntry{
		{name: top + "/", typ: tar.TypeDir, mode: 0o755},
		{name: top + "/SOURCES.md", typ: tar.TypeReg, mode: 0o644, data: []byte(renderSourcesMD(up, engine, es, top))},
	}
	for _, s := range es.Sources {
		if strings.TrimSpace(s.Name) == "" || strings.TrimSpace(s.Version) == "" {
			return fmt.Errorf("%s: a source entry has an empty name or version", engine)
		}
		folder := top + "/" + sanitizeName(s.Name) + "-" + s.Version
		var (
			got []sourceEntry
			err error
		)
		switch s.Kind {
		case "http":
			got, err = collectHTTP(s, folder)
		case "git":
			dir, derr := prepareGit(engine, s)
			if derr != nil {
				err = derr
				break
			}
			got, err = collectTree(folder, dir)
		case "svn":
			dir, derr := prepareSVN(engine, s)
			if derr != nil {
				err = derr
				break
			}
			got, err = collectTree(folder, dir)
		default:
			err = fmt.Errorf("source %s: unknown kind %q", s.Name, s.Kind)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", engine, err)
		}
		entries = append(entries, got...)
	}
	dst := filepath.Join(dist, sourceArchiveName(up, engine))
	if parts, err := filepath.Glob(dst + ".part*"); err == nil {
		for _, p := range parts {
			os.Remove(p)
		}
	}
	size, err := writeSourceArchive(dst, entries, epoch)
	if err != nil {
		return fmt.Errorf("%s: %w", engine, err)
	}
	fmt.Printf("sources: %s (%s, %d sources)\n", filepath.Base(dst), formatBytes(size), len(es.Sources))
	if size > sourcePartLimit {
		fmt.Printf("sources: warning: %s is larger than 1.9 GiB and cannot be uploaded to GitHub Releases as one asset\n", filepath.Base(dst))
		parts, err := splitSourceArchive(dst, sourcePartLimit)
		if err != nil {
			return fmt.Errorf("%s: %w", engine, err)
		}
		for _, p := range parts {
			st, err := os.Stat(p)
			if err != nil {
				return fmt.Errorf("%s: %w", engine, err)
			}
			fmt.Printf("sources: part %s (%s) - split of %s at %d bytes; upload the parts and document this split (for example `cat %s.part* > %s`)\n",
				filepath.Base(p), formatBytes(st.Size()), filepath.Base(dst), sourcePartLimit, filepath.Base(dst), filepath.Base(dst))
		}
	}
	return nil
}

// collectHTTP downloads (hash-verified) an http source and stores the original file untouched.
func collectHTTP(s engineSource, folder string) ([]sourceEntry, error) {
	file, err := download(s.URL, s.SHA256, filepath.Join(cacheDir(), "sources", "http"))
	if err != nil {
		return nil, fmt.Errorf("http source %s: %w", s.Name, err)
	}
	st, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	return []sourceEntry{
		{name: folder + "/", typ: tar.TypeDir, mode: 0o755},
		{name: folder + "/" + httpBasename(s.URL), typ: tar.TypeReg, mode: 0o644, size: st.Size(), src: file},
	}, nil
}

// prepareGit fetches exactly one commit into .cache/sources/<engine>/<name>-<ref> and returns that tree.
func prepareGit(engine string, s engineSource) (string, error) {
	if !gitRefRe.MatchString(s.Ref) {
		return "", fmt.Errorf("git source %s: ref %q is not a full lowercase commit", s.Name, s.Ref)
	}
	dir := filepath.Join(cacheDir(), "sources", engine, sanitizeName(s.Name)+"-"+s.Ref)
	if head, err := gitHead(dir); err == nil && head == s.Ref {
		if s.Submodules {
			return dir, gitSubmodules(dir)
		}
		return dir, nil
	}
	if err := rmtree(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// The commit hash is the proof of content: a mirror that serves the commit serves the same tree.
	var fetchErr error
	for _, url := range append([]string{s.URL}, s.Mirrors...) {
		if err := rmtree(dir); err != nil {
			return "", err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		fetchErr = nil
		for _, gitArgs := range [][]string{
			{"init", "-q"},
			{"remote", "add", "origin", url}, // relative submodule URLs (../../GNOME/gvdb.git) resolve against it
			{"fetch", "--depth", "1", "origin", s.Ref},
			{"checkout", "-q", "-f", "--detach", "FETCH_HEAD"},
		} {
			if _, err := run(runOpts{Dir: dir}, "git", gitArgs...); err != nil {
				fetchErr = fmt.Errorf("git %s in %s: %w", strings.Join(gitArgs, " "), dir, err)
				break
			}
		}
		if fetchErr == nil {
			break
		}
	}
	if fetchErr != nil {
		return "", fetchErr
	}
	head, err := gitHead(dir)
	if err != nil {
		return "", err
	}
	if head != s.Ref {
		return "", fmt.Errorf("git source %s: HEAD is %s, want %s", s.Name, head, s.Ref)
	}
	if s.Submodules {
		if err := gitSubmodules(dir); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func gitHead(dir string) (string, error) {
	out, err := run(runOpts{Dir: dir, Capture: true, Quiet: true}, "git", "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func gitSubmodules(dir string) error {
	if _, err := run(runOpts{Dir: dir}, "git", "submodule", "update", "--init", "--recursive", "--depth", "1"); err != nil {
		return fmt.Errorf("git submodule update in %s: %w", dir, err)
	}
	return nil
}

// prepareSVN exports one revision into .cache/sources/<engine>/<name>-<ref> and returns that tree.
func prepareSVN(engine string, s engineSource) (string, error) {
	if !svnRefRe.MatchString(s.Ref) {
		return "", fmt.Errorf("svn source %s: ref %q is not a revision number", s.Name, s.Ref)
	}
	dir := filepath.Join(cacheDir(), "sources", engine, sanitizeName(s.Name)+"-"+s.Ref)
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		return dir, nil
	}
	if err := rmtree(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	if _, err := run(runOpts{Dir: filepath.Dir(dir)}, "svn", "export", "-r", s.Ref, s.URL, dir); err != nil {
		return "", fmt.Errorf("svn export -r %s %s: %w", s.Ref, s.URL, err)
	}
	return dir, nil
}

// collectTree lists a fetched tree, dropping every .git/.svn file and folder, as archive members under prefix.
func collectTree(prefix, dir string) ([]sourceEntry, error) {
	var out []sourceEntry
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		name := strings.TrimSuffix(prefix, "/")
		if rel != "." {
			name += "/" + rel
			for _, part := range strings.Split(rel, "/") {
				if part == ".git" || part == ".svn" {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			out = append(out, sourceEntry{name: name + "/", typ: tar.TypeDir, mode: 0o755})
		case info.Mode()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			out = append(out, sourceEntry{name: name, typ: tar.TypeSymlink, mode: 0o777, link: link})
		case info.Mode().IsRegular():
			out = append(out, sourceEntry{name: name, typ: tar.TypeReg, mode: int64(info.Mode().Perm()), size: info.Size(), src: p})
		default:
			return fmt.Errorf("%s: unsupported file type %v", p, info.Mode())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// writeSourceArchive writes a reproducible tar.gz: members sorted by name, mtime fixed at epoch, uid/gid 0 and no
// user names, symlinks and permission bits kept.
func writeSourceArchive(dst string, entries []sourceEntry, epoch int64) (int64, error) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	for i := 1; i < len(entries); i++ {
		if entries[i].name == entries[i-1].name {
			return 0, fmt.Errorf("duplicate archive member %s", entries[i].name)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	f, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	gz, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		return 0, err
	}
	gz.Name, gz.Comment, gz.ModTime = "", "", time.Time{}
	tw := tar.NewWriter(gz)
	mtime := time.Unix(epoch, 0).UTC()
	for _, e := range entries {
		size := e.size
		if e.data != nil {
			size = int64(len(e.data))
		}
		h := &tar.Header{
			Name:     e.name,
			Mode:     e.mode,
			Size:     size,
			ModTime:  mtime,
			Typeflag: e.typ,
			Linkname: e.link,
			Format:   tar.FormatPAX,
		}
		if e.typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			return 0, err
		}
		switch {
		case e.typ != tar.TypeReg:
		case e.data != nil:
			if _, err := tw.Write(e.data); err != nil {
				return 0, err
			}
		default:
			in, err := os.Open(e.src)
			if err != nil {
				return 0, err
			}
			_, err = io.Copy(tw, in)
			in.Close()
			if err != nil {
				return 0, err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return 0, err
	}
	if err := gz.Close(); err != nil {
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	st, err := os.Stat(dst)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// splitSourceArchive splits the archive into raw byte parts of at most limit bytes and returns their names.
func splitSourceArchive(file string, limit int64) ([]string, error) {
	in, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	var parts []string
	for i := 1; ; i++ {
		name := fmt.Sprintf("%s.part%02d", file, i)
		out, err := os.Create(name)
		if err != nil {
			return nil, err
		}
		n, err := io.Copy(out, io.LimitReader(in, limit))
		cerr := out.Close()
		if err != nil {
			return nil, err
		}
		if cerr != nil {
			return nil, cerr
		}
		if n == 0 {
			os.Remove(name)
			break
		}
		parts = append(parts, name)
		if n < limit {
			break
		}
	}
	return parts, nil
}

// renderSourcesMD is the written source offer of one engine.
func renderSourcesMD(up Upstream, engine string, es EngineSources, top string) string {
	var b strings.Builder
	title := strings.TrimSpace(es.Name)
	if title == "" {
		title = engine
	}
	fmt.Fprintf(&b, "# Corresponding Source — %s (`%s`)\n\n", title, engine)
	fmt.Fprintf(&b, "This archive (`%s.tar.gz`, single top-level folder `%s/`) is the written offer of the Corresponding\n", top, top)
	fmt.Fprintf(&b, "Source for the `%s` engine bundled in the filee-cli %s release archives, built from Filee %s\n", engine, up.Version, up.UpstreamVersion)
	fmt.Fprintf(&b, "(upstream commit `%s`).\n\n", up.Commit)
	if es.License != "" {
		fmt.Fprintf(&b, "**License**: `%s`\n\n", mdCell(es.License))
	}
	if strings.TrimSpace(es.LicenseNote) != "" {
		fmt.Fprintf(&b, "**License note**: %s\n\n", mdCell(es.LicenseNote))
	}

	b.WriteString("## Shipped binaries\n\n")
	if len(es.Binaries) == 0 {
		b.WriteString("No binary of this engine is shipped by filee-cli.\n\n")
	} else {
		b.WriteString("| platform | version | sha256 | bytes | url |\n| --- | --- | --- | --- | --- |\n")
		for _, rid := range sortedKeys(es.Binaries) {
			bin := es.Binaries[rid]
			fmt.Fprintf(&b, "| %s | %s | `%s` | %d | %s |\n", mdCell(rid), mdCell(bin.Version), strings.ToLower(bin.SHA256), bin.Size, mdCell(bin.URL))
		}
		b.WriteString("\n")
		for _, rid := range sortedKeys(es.Binaries) {
			if n := strings.TrimSpace(es.Binaries[rid].Note); n != "" {
				fmt.Fprintf(&b, "- `%s`: %s\n", rid, mdCell(n))
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("## Build recipe\n\n")
	if r := strings.TrimSpace(es.BuildRecipe); r != "" {
		b.WriteString(r)
		b.WriteString("\n\n")
	} else {
		b.WriteString("(not recorded)\n\n")
	}

	b.WriteString("## Sources\n\n")
	if len(es.Sources) == 0 {
		b.WriteString("This engine ships no separate source, or its source is not pinned yet (see Unresolved below).\n\n")
	} else {
		b.WriteString("Every entry below is under its own `<name>-<version>/` folder in this archive.\n\n")
		b.WriteString("| name | version | license | kind | url | ref | sha256 | platforms | note |\n")
		b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
		slash := false
		for _, s := range es.Sources {
			if strings.Contains(s.Name, "/") {
				slash = true
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
				mdCell(s.Name), mdCell(s.Version), mdCell(s.License), mdCell(s.Kind), mdCell(s.URL),
				dash(s.Ref), dash(strings.ToLower(s.SHA256)), dash(strings.Join(s.Platforms, ", ")), dash(mdCell(s.Note)))
		}
		b.WriteString("\n")
		if slash {
			b.WriteString("A `/` inside a source name is replaced by `_` in that source's folder name.\n\n")
		}
	}

	if n := strings.TrimSpace(es.Notes); n != "" {
		b.WriteString("## Notes\n\n")
		b.WriteString(n)
		b.WriteString("\n\n")
	}

	b.WriteString("## Unresolved\n\n")
	if len(es.Unresolved) == 0 {
		b.WriteString("None.\n")
	} else {
		for _, u := range es.Unresolved {
			fmt.Fprintf(&b, "- %s\n", strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(u), "\r\n", "\n"), "\n", "\n  "))
		}
	}
	return b.String()
}

// verifySources validates every data/engine-sources/*.json without touching the network and returns one error
// listing all problems it found.
func verifySources() error {
	if rootDir == "" {
		if err := findRoot(); err != nil {
			return err
		}
	}
	dir := filepath.Join(dataDir(), "engine-sources")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var problems []string
	engines := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		file := e.Name()
		stem := strings.TrimSuffix(file, ".json")
		bad := func(format string, args ...any) {
			problems = append(problems, fmt.Sprintf("%s: %s", file, fmt.Sprintf(format, args...)))
		}
		var es EngineSources
		if err := readJSON(filepath.Join(dir, file), &es); err != nil {
			bad("%v", err)
			continue
		}
		if es.Engine == "" {
			bad("engine id is empty")
		} else {
			if es.Engine != stem {
				bad("engine id %q does not match the file name", es.Engine)
			}
			if prev, ok := engines[es.Engine]; ok {
				bad("engine id %q is already used by %s", es.Engine, prev)
			} else {
				engines[es.Engine] = file
			}
		}
		switch es.License {
		case "":
			bad("license is empty")
		case "LicenseRef-Proprietary":
			if es.HostSource {
				bad("host_source is true for the proprietary license %q", es.License)
			}
		}
		for i, s := range es.Sources {
			where := fmt.Sprintf("source[%d] %q", i, s.Name)
			if strings.TrimSpace(s.Name) == "" {
				bad("source[%d]: name is empty", i)
			}
			if strings.TrimSpace(s.Version) == "" {
				bad("%s: version is empty", where)
			}
			if strings.TrimSpace(s.URL) == "" {
				bad("%s: url is empty", where)
			}
			if strings.TrimSpace(s.License) == "" {
				bad("%s: license is empty", where)
			}
			switch s.Kind {
			case "git":
				if !gitRefRe.MatchString(s.Ref) {
					bad("%s: git ref %q is not 40 lowercase hex digits", where, s.Ref)
				}
				if s.SHA256 != "" || s.Size != 0 {
					bad("%s: git source also has sha256/size", where)
				}
			case "svn":
				if !svnRefRe.MatchString(s.Ref) {
					bad("%s: svn ref %q is not a revision number", where, s.Ref)
				}
			case "http":
				if !sha256Re.MatchString(s.SHA256) {
					bad("%s: http source needs a 64-hex sha256 (got %q)", where, s.SHA256)
				}
				if s.Size <= 0 {
					bad("%s: http source needs a positive size (got %d)", where, s.Size)
				}
				if s.Ref != "" {
					bad("%s: http source also has a ref", where)
				}
				if b := httpBasename(s.URL); b == "" || b == "." || b == "/" {
					bad("%s: url %q has no file name", where, s.URL)
				}
			default:
				bad("%s: unknown kind %q (want git, svn or http)", where, s.Kind)
			}
		}
		for rid, bin := range es.Binaries {
			if bin.URL == "" && bin.SHA256 == "" && bin.Version == "" {
				bad("binary %s has no url, sha256 or version", rid)
			}
			if bin.SHA256 != "" && !sha256Re.MatchString(bin.SHA256) {
				bad("binary %s: sha256 %q is not 64 hex digits", rid, bin.SHA256)
			}
		}
	}
	if len(problems) != 0 {
		sort.Strings(problems)
		return fmt.Errorf("data/engine-sources: %d problem(s):\n%s", len(problems), strings.Join(problems, "\n"))
	}
	return nil
}

func mdCell(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	if s = strings.TrimSpace(s); s == "" {
		return "—"
	}
	return s
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// sanitizeName keeps a source name usable as a single folder name.
func sanitizeName(s string) string {
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	if s = strings.Trim(s, " ."); s == "" {
		s = "source"
	}
	return s
}

func httpBasename(rawURL string) string {
	p := rawURL
	if u, err := url.Parse(rawURL); err == nil && u.Path != "" {
		p = u.Path
	}
	return path.Base(p)
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GiB", float64(n)/float64(int64(1)<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(int64(1)<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	}
	return fmt.Sprintf("%d B", n)
}
