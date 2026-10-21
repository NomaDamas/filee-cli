package main

import (
	"debug/elf"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type version []int

func parseVersion(s string) (version, bool) {
	var v version
	for _, part := range strings.Split(s, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, false
		}
		v = append(v, n)
	}
	return v, true
}

func (a version) less(b version) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

func (a version) String() string {
	parts := make([]string, len(a))
	for i, n := range a {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ".")
}

func isELF(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	magic := make([]byte, 4)
	n, _ := f.Read(magic)
	return n == 4 && string(magic) == "\x7fELF"
}

// cmdGlibc fails when an ELF file under the given paths needs a newer GLIBC symbol version than --max (a Linux package
// runs only on systems whose glibc is at least as new as the newest version any of its files asks for).
func cmdGlibc(args []string) error {
	fl := newFlags("glibc")
	max := fl.String("max", "2.35", "newest GLIBC version allowed (Ubuntu 22.04 has 2.35)")
	needed := fl.Bool("needed", false, "also list each file's DT_NEEDED libraries")
	if err := fl.Parse(args); err != nil {
		return err
	}
	limit, ok := parseVersion(*max)
	if !ok || fl.NArg() == 0 {
		return fmt.Errorf("usage: glibc [--max 2.35] [--needed] PATH...")
	}
	type result struct {
		name   string
		newest version
		libs   []string
	}
	var results []result
	collect := func(path, name string) error {
		f, err := elf.Open(path)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		defer f.Close()
		var newest version
		needs, err := f.DynamicVersionNeeds()
		if err != nil && !strings.Contains(err.Error(), "no") {
			return fmt.Errorf("%s: %w", name, err)
		}
		for _, n := range needs {
			for _, d := range n.Needs {
				if v, ok := strings.CutPrefix(d.Dep, "GLIBC_"); ok {
					if pv, ok := parseVersion(v); ok && newest.less(pv) {
						newest = pv
					}
				}
			}
		}
		libs, _ := f.ImportedLibraries()
		results = append(results, result{name, newest, libs})
		return nil
	}
	for _, root := range fl.Args() {
		info, err := os.Lstat(root)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			if info.Mode().IsRegular() && isELF(root) {
				if err := collect(root, filepath.Base(root)); err != nil {
					return err
				}
			}
			continue
		}
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !d.Type().IsRegular() || !isELF(p) {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			return collect(p, filepath.ToSlash(rel))
		})
		if err != nil {
			return err
		}
	}
	if len(results) == 0 {
		return fmt.Errorf("no ELF files under %s", strings.Join(fl.Args(), ", "))
	}
	sort.Slice(results, func(i, j int) bool { return results[i].name < results[j].name })
	var overall version
	var bad []string
	for _, r := range results {
		label := "-"
		if r.newest != nil {
			label = "GLIBC_" + r.newest.String()
			if overall.less(r.newest) {
				overall = r.newest
			}
			if limit.less(r.newest) {
				bad = append(bad, fmt.Sprintf("%s needs %s", r.name, label))
			}
		}
		line := fmt.Sprintf("%-14s %s", label, r.name)
		if *needed {
			line += "  [" + strings.Join(r.libs, ", ") + "]"
		}
		fmt.Println(line)
	}
	fmt.Printf("%d ELF files; newest requirement GLIBC_%s (limit GLIBC_%s)\n", len(results), overall, limit)
	if len(bad) > 0 {
		return fmt.Errorf("files need a newer glibc than %s:\n  %s", *max, strings.Join(bad, "\n  "))
	}
	return nil
}
