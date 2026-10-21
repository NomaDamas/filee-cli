package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Crate struct{ Name, Version, License, Checksum string }

type pinsFile struct {
	Rhwp struct {
		Version, Repository, Tag, Commit, License string
		RustToolchain                             string `json:"rust_toolchain"`
	} `json:"rhwp"`
	Unhwp struct {
		Version, Repository, Commit, License string
	} `json:"unhwp"`
	SevenZip struct {
		Version, Repository, Tag, Commit, License string
	} `json:"7zip"`
	MagickNative struct {
		Version, Repository, Tag, Commit string
	} `json:"magick_native"`
	Pdfium struct {
		Release, Repository string
		Assets              map[string]struct{ File, Sha256 string }
	} `json:"pdfium"`
}

func readPins() (pinsFile, error) {
	var p pinsFile
	err := readJSON(filepath.Join(dataDir(), "pins.json"), &p)
	return p, err
}

var (
	lockName     = regexp.MustCompile(`^name = "(.*)"`)
	lockVersion  = regexp.MustCompile(`^version = "(.*)"`)
	lockChecksum = regexp.MustCompile(`^checksum = "(.*)"`)
	treePackage  = regexp.MustCompile(`^(\S+) v(\S+)`)
	pathCrate    = regexp.MustCompile(`\((/|[A-Za-z]:\\)`)
)

func lockChecksums(lock string) (map[[2]string]string, error) {
	f, err := os.Open(lock)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sums := map[[2]string]string{}
	var name, version string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "[[package]]":
			name, version = "", ""
		case lockName.MatchString(line):
			name = lockName.FindStringSubmatch(line)[1]
		case lockVersion.MatchString(line):
			version = lockVersion.FindStringSubmatch(line)[1]
		case lockChecksum.MatchString(line):
			sums[[2]string{name, version}] = lockChecksum.FindStringSubmatch(line)[1]
		}
	}
	return sums, sc.Err()
}

// crateList is the transitive normal (non-dev, non-build) dependency set of one package for all targets, a superset
// of what one platform links.
func crateList(source, pkg string, extra []string, label string) ([]Crate, error) {
	args := append([]string{"tree", "--locked", "--target", "all", "-e", "normal", "--prefix", "none", "-p", pkg}, extra...)
	args = append(args, "--format", "{p}|{l}")
	out, err := run(runOpts{Dir: source, Capture: true}, "cargo", args...)
	if err != nil {
		return nil, err
	}
	sums, err := lockChecksums(filepath.Join(source, "Cargo.lock"))
	if err != nil {
		return nil, err
	}
	seen := map[Crate]bool{}
	for _, line := range strings.Split(out, "\n") {
		packagePart, licensePart, ok := strings.Cut(line, "|")
		if !ok {
			continue
		}
		m := treePackage.FindStringSubmatch(packagePart)
		if m == nil || m[1] == pkg {
			continue
		}
		if pathCrate.MatchString(packagePart) {
			continue // a workspace member of the component itself: covered by the component's own license
		}
		expr := strings.TrimSpace(strings.TrimSuffix(licensePart, " (*)"))
		expr = strings.ReplaceAll(strings.ReplaceAll(expr, " / ", " OR "), "/", " OR ")
		if expr == "" {
			return nil, fmt.Errorf("%s: crate %s %s declares no license", label, m[1], m[2])
		}
		sum := sums[[2]string{m[1], m[2]}]
		if sum == "" {
			return nil, fmt.Errorf("%s: no Cargo.lock checksum for %s %s (path or git dependency?)", label, m[1], m[2])
		}
		seen[Crate{m[1], m[2], expr, sum}] = true
	}
	rows := make([]Crate, 0, len(seen))
	for c := range seen {
		rows = append(rows, c)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Version < b.Version
	})
	return rows, nil
}

// cmdCrates regenerates data/{rhwp,unhwp}-crates.tsv from the pinned sources (needs git and cargo).
func cmdCrates(args []string) error {
	pins, err := readPins()
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "filee-crates-")
	if err != nil {
		return err
	}
	defer rmtree(tmp)
	type job struct {
		key, repo, commit, version, pkg string
		extra                           []string
	}
	jobs := []job{
		{"rhwp", pins.Rhwp.Repository, pins.Rhwp.Commit, pins.Rhwp.Version, "rhwp", nil},
		{"unhwp", pins.Unhwp.Repository, pins.Unhwp.Commit, pins.Unhwp.Version, "unhwp", []string{"--features", "ffi"}},
	}
	for _, j := range jobs {
		src := filepath.Join(tmp, j.key)
		if err := os.MkdirAll(src, 0o755); err != nil {
			return err
		}
		if err := gitCheckout(src, j.repo, j.commit); err != nil {
			return err
		}
		rows, err := crateList(src, j.pkg, j.extra, j.key)
		if err != nil {
			return err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "# %s %s at %s: name<TAB>version<TAB>license<TAB>Cargo.lock sha256\n", j.key, j.version, j.commit)
		for _, c := range rows {
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", c.Name, c.Version, c.License, c.Checksum)
		}
		target := filepath.Join(dataDir(), j.key+"-crates.tsv")
		if err := os.WriteFile(target, []byte(b.String()), 0o644); err != nil {
			return err
		}
		fmt.Printf("%s: %d crates\n", target, len(rows))
	}
	return nil
}

func readCrates(name string) ([]Crate, error) {
	data, err := os.ReadFile(filepath.Join(dataDir(), name+"-crates.tsv"))
	if err != nil {
		return nil, err
	}
	var rows []Crate
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 4 {
			return nil, fmt.Errorf("%s-crates.tsv: bad line %q", name, line)
		}
		rows = append(rows, Crate{f[0], f[1], f[2], f[3]})
	}
	return rows, nil
}
