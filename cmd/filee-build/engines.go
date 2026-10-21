package main

import (
	"archive/tar"
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// engineSets lists, per RID, the third-party conversion engines bundled into engines/<id>. They are exactly the ones
// Filee itself offers for that platform (upstream engines.json), minus rhwp and 7-Zip, which addEngines handles.
var engineSets = map[string][]string{
	"win-x64":     {"pandoc", "ffmpeg", "calibre", "ghostscript", "vcruntime", "libreoffice", "h2orestart", "jre"},
	"linux-x64":   {"pandoc", "ffmpeg", "calibre"},
	"linux-arm64": {"pandoc", "ffmpeg", "calibre"},
	"osx-x64":     {"pandoc", "calibre", "libreoffice"},
	"osx-arm64":   {"pandoc", "calibre", "libreoffice"},
}

// binaryOverrides are the only places where the bundled binary is not the one upstream's engines.json pins: the
// replacement must come with a complete source pin (data/engine-sources/<id>.json).
var binaryOverrides = map[string]string{
	"ffmpeg/win-x64": "upstream pins a Gyan.dev build whose build scripts and dependency pins are not published; the BtbN build of the same FFmpeg release is bundled instead",
}

// requiredPrograms are the programs a folder must have to count as an engine (mirrors upstream's EngineEnvironment).
func requiredPrograms(id string, windows bool) []string {
	exe := func(n string) string {
		if windows {
			return n + ".exe"
		}
		return n
	}
	switch id {
	case "pandoc":
		return []string{exe("pandoc")}
	case "ffmpeg":
		return []string{exe("ffmpeg"), exe("ffprobe")}
	case "calibre":
		return []string{exe("ebook-convert")}
	case "ghostscript":
		if windows {
			return []string{"gswin64c.exe"}
		}
		return []string{"gs"}
	case "libreoffice":
		return []string{exe("soffice")}
	}
	return nil
}

// BundledEngine describes one engine bundled into a package.
type BundledEngine struct {
	ID, Version, URL, Sha256, Kind string
	Size                           int64
	Override                       string // why the binary differs from upstream's engines.json, if it does
}

type bundleBinary struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	Sha256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

// resolveBundled returns the pinned binary of engine id for rid. It must be listed in data/engine-sources/<id>.json
// (so no binary is shipped without a source pin) and equal upstream's engines.json unless binaryOverrides allows it.
func resolveBundled(id, rid string, m enginesManifest) (BundledEngine, error) {
	var out BundledEngine
	upstream, ok := m.forRID(rid)[id]
	if !ok {
		return out, fmt.Errorf("upstream engines.json has no %s for %s", id, rid)
	}
	var f struct {
		Binaries map[string]bundleBinary `json:"binaries"`
	}
	file := filepath.Join(dataDir(), "engine-sources", id+".json")
	if err := readJSON(file, &f); err != nil {
		return out, fmt.Errorf("no source pin for %s: %w", id, err)
	}
	pin, ok := f.Binaries[rid]
	if !ok && strings.HasPrefix(rid, "win-") {
		pin, ok = f.Binaries["win"]
	}
	if !ok {
		return out, fmt.Errorf("%s has no binary pin for %s", file, rid)
	}
	override := binaryOverrides[id+"/"+rid]
	same := strings.EqualFold(pin.Sha256, upstream.Sha256)
	switch {
	case !same && override == "":
		return out, fmt.Errorf("%s pins %s for %s with sha256 %s but upstream engines.json pins %s: re-pin the sources (docs/updating.md)", file, id, rid, pin.Sha256, upstream.Sha256)
	case same && override != "":
		return out, fmt.Errorf("binaryOverrides[%s/%s] is obsolete: upstream pins the same file now", id, rid)
	}
	if pin.Size == 0 && same {
		pin.Size = upstream.Size // pins that omit the size take upstream's, which the same hash vouches for
	}
	if pin.URL == "" || len(pin.Sha256) != 64 || pin.Size <= 0 {
		return out, fmt.Errorf("%s: incomplete binary pin for %s", file, rid)
	}
	return BundledEngine{ID: id, Version: pin.Version, URL: pin.URL, Sha256: strings.ToLower(pin.Sha256), Kind: upstream.Kind, Size: pin.Size, Override: override}, nil
}

// installEngines downloads (SHA-256 checked) and unpacks every engine of the RID's set into engines/<id>.
func installEngines(rid, enginesDir, scratch string) ([]BundledEngine, error) {
	set, ok := engineSets[rid]
	if !ok {
		return nil, fmt.Errorf("no engine set for %s", rid)
	}
	m, err := readEnginesManifest()
	if err != nil {
		return nil, err
	}
	windows := strings.HasPrefix(rid, "win-")
	var out []BundledEngine
	vcruntime := ""
	for _, id := range set {
		e, err := resolveBundled(id, rid, m)
		if err != nil {
			return nil, err
		}
		file, err := download(e.URL, e.Sha256, filepath.Join(cacheDir(), "downloads"))
		if err != nil {
			return nil, err
		}
		if st, err := os.Stat(file); err != nil || st.Size() != e.Size {
			return nil, fmt.Errorf("%s: downloaded file is not the pinned %d bytes", id, e.Size)
		}
		dest := filepath.Join(enginesDir, id)
		fmt.Printf("engine %s %s (%s)\n", id, e.Version, e.Kind)
		switch e.Kind {
		case "zip", "tar.gz", "tar.xz":
			stage := filepath.Join(scratch, id+"-x")
			if err := rmtree(stage); err != nil {
				return nil, err
			}
			if err := extractArchive(file, e.Kind, stage); err != nil {
				return nil, fmt.Errorf("%s: %w", id, err)
			}
			err = place(singleRoot(stage), dest)
		case "dmg":
			err = installDMG(id, file, dest, scratch)
		case "msi":
			err = installMSI(id, file, dest, scratch)
		case "conda":
			err = installConda(id, file, dest)
			if id == "vcruntime" {
				vcruntime = dest
			}
		case "oxt":
			ext := filepath.Join(enginesDir, "libreoffice", "share", "extensions")
			if _, err := os.Stat(ext); err != nil {
				return nil, fmt.Errorf("%s needs LibreOffice, which is not part of this package", id)
			}
			dest = filepath.Join(ext, "H2Orestart")
			err = extractArchive(file, "zip", dest)
		default:
			err = fmt.Errorf("unknown engine package kind %q", e.Kind)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", id, err)
		}
		if err := checkPrograms(id, dest, windows); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	// Like upstream's installer: the Microsoft C++ runtime goes next to the programs of the conda packages (Windows
	// looks in a program's own folder before System32) and is not a folder of its own.
	if vcruntime != "" {
		dlls, _ := filepath.Glob(filepath.Join(vcruntime, "*.dll"))
		for _, id := range set {
			if id == "vcruntime" {
				continue
			}
			var e BundledEngine
			for _, b := range out {
				if b.ID == id {
					e = b
				}
			}
			if e.Kind != "conda" {
				continue
			}
			for _, dll := range dlls {
				if err := copyFile(dll, filepath.Join(enginesDir, id, filepath.Base(dll))); err != nil {
					return nil, err
				}
			}
			if err := copyTree(filepath.Join(vcruntime, "licenses"), filepath.Join(enginesDir, id, "licenses", "vc14_runtime")); err != nil {
				return nil, err
			}
		}
		if err := rmtree(vcruntime); err != nil {
			return nil, err
		}
	}
	if !windows {
		for _, e := range out {
			if err := makeExecutable(e.ID, filepath.Join(enginesDir, e.ID)); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// singleRoot collapses an archive with one top-level folder.
func singleRoot(dir string) string {
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) == 1 && entries[0].IsDir() {
		return filepath.Join(dir, entries[0].Name())
	}
	return dir
}

// place makes src the folder dest (moving it, or copying across volumes).
func place(src, dest string) error {
	if err := rmtree(dest); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dest); err == nil {
		return nil
	}
	if err := copyTree(src, dest); err != nil {
		return err
	}
	return rmtree(src)
}

// programFolder is the folder holding all required programs of engine id, searched like upstream does.
func programFolder(id, root string, windows bool) string {
	programs := requiredPrograms(id, windows)
	if len(programs) == 0 {
		return ""
	}
	candidates := []string{root, filepath.Join(root, "bin"), filepath.Join(root, "program"), filepath.Join(root, "Contents", "MacOS")}
	for _, app := range []string{"LibreOffice.app", "calibre.app"} {
		candidates = append(candidates, filepath.Join(root, app, "Contents", "MacOS"))
	}
	for _, c := range candidates {
		all := true
		for _, p := range programs {
			if st, err := os.Stat(filepath.Join(c, p)); err != nil || st.IsDir() {
				all = false
			}
		}
		if all {
			return c
		}
	}
	return ""
}

func checkPrograms(id, root string, windows bool) error {
	if requiredPrograms(id, windows) == nil {
		if _, err := os.Stat(root); err != nil {
			return fmt.Errorf("%s was not unpacked", id)
		}
		return nil
	}
	if programFolder(id, root, windows) == "" {
		return fmt.Errorf("%s: required programs %v not found in the unpacked package", id, requiredPrograms(id, windows))
	}
	return nil
}

func makeExecutable(id, root string) error {
	folder := programFolder(id, root, false)
	for _, p := range requiredPrograms(id, false) {
		full := filepath.Join(folder, p)
		st, err := os.Stat(full)
		if err != nil {
			return err
		}
		if err := os.Chmod(full, st.Mode().Perm()|0o111); err != nil {
			return err
		}
	}
	return nil
}

// installDMG copies the application bundle out of a macOS disk image with ditto (symlinks, permissions and the
// vendor's code signature stay intact).
func installDMG(id, file, dest, scratch string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("disk images can only be unpacked on macOS")
	}
	app := map[string]string{"libreoffice": "LibreOffice.app", "calibre": "calibre.app"}[id]
	if app == "" {
		return fmt.Errorf("no application bundle is specified for %s", id)
	}
	mount, err := os.MkdirTemp("", "filee-mount-") // hdiutil may not mount below an external volume
	if err != nil {
		return err
	}
	if _, err := run(runOpts{}, "hdiutil", "attach", "-readonly", "-nobrowse", "-noverify", "-mountpoint", mount, file); err != nil {
		return err
	}
	detach := func() error {
		if _, err := run(runOpts{Quiet: true}, "hdiutil", "detach", mount); err != nil {
			_, err = run(runOpts{Quiet: true}, "hdiutil", "detach", "-force", mount)
			return err
		}
		return nil
	}
	src := filepath.Join(mount, app)
	if st, err := os.Lstat(src); err != nil || !st.IsDir() {
		detach()
		return fmt.Errorf("%s is missing from the disk image", app)
	}
	if err := rmtree(dest); err != nil {
		detach()
		return err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		detach()
		return err
	}
	if _, err := run(runOpts{Quiet: true}, "ditto", src, filepath.Join(dest, app)); err != nil {
		detach()
		return err
	}
	if err := detach(); err != nil {
		return err
	}
	return os.Remove(mount)
}

// installMSI unpacks an installer with an administrative install (files only: no registry entries, no admin rights)
// into a short folder (msiexec fails beyond MAX_PATH), then keeps the program folder. LibreOffice is trimmed like
// upstream trims it: help, readmes, gallery, and spelling dictionaries other than English and Korean.
func installMSI(id, file, dest, scratch string) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("%s is a Windows installer: build win-x64 on Windows", id)
	}
	short, err := os.MkdirTemp(filepath.VolumeName(scratch)+`\`, "fe")
	if err != nil {
		return err
	}
	defer rmtree(short)
	named := filepath.Join(short, id+".msi")
	if err := copyFile(file, named); err != nil {
		return err
	}
	target := filepath.Join(short, "t")
	if _, err := run(runOpts{}, "msiexec.exe", "/a", named, "/qn", "TARGETDIR="+target); err != nil {
		return err
	}
	var root string
	switch id {
	case "calibre":
		found := findFile(target, "ebook-convert.exe")
		if found == "" {
			return fmt.Errorf("ebook-convert.exe not found in the calibre installer")
		}
		root = filepath.Dir(found)
	case "libreoffice":
		found := findFile(target, "soffice.exe")
		if found == "" {
			return fmt.Errorf("soffice.exe not found in the LibreOffice installer")
		}
		root = filepath.Dir(filepath.Dir(found))
		if err := trimLibreOffice(root); err != nil {
			return err
		}
	default:
		return fmt.Errorf("no layout is specified for the %s installer", id)
	}
	return place(root, dest)
}

func findFile(root, name string) string {
	found := ""
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && found == "" && !d.IsDir() && strings.EqualFold(d.Name(), name) {
			found = p
			return fs.SkipAll
		}
		return nil
	})
	return found
}

func trimLibreOffice(root string) error {
	for _, rel := range []string{"help", "readmes", filepath.Join("share", "gallery")} {
		if err := rmtree(filepath.Join(root, rel)); err != nil {
			return err
		}
	}
	msis, _ := filepath.Glob(filepath.Join(root, "*.msi"))
	for _, m := range msis {
		os.Remove(m)
	}
	dicts, _ := filepath.Glob(filepath.Join(root, "share", "extensions", "dict-*"))
	for _, d := range dicts {
		if b := filepath.Base(d); b != "dict-en" && b != "dict-ko" {
			if err := rmtree(d); err != nil {
				return err
			}
		}
	}
	return nil
}

// installConda unpacks a conda-forge package for Windows (a zip holding zstd-compressed tarballs): the programs and
// DLLs in Library/bin land directly in dest, the license files of the package in dest/licenses.
func installConda(id, file, dest string) error {
	if err := rmtree(dest); err != nil {
		return err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	zr, err := zip.OpenReader(file)
	if err != nil {
		return err
	}
	defer zr.Close()
	bins := 0
	for _, f := range zr.File {
		isPkg := strings.HasPrefix(f.Name, "pkg-") && strings.HasSuffix(f.Name, ".tar.zst")
		isInfo := strings.HasPrefix(f.Name, "info-") && strings.HasSuffix(f.Name, ".tar.zst")
		if !isPkg && !isInfo {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		zd, err := zstd.NewReader(rc)
		if err != nil {
			rc.Close()
			return err
		}
		tr := tar.NewReader(zd)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				zd.Close()
				rc.Close()
				return err
			}
			if h.Typeflag != tar.TypeReg {
				continue
			}
			name := path.Clean(h.Name)
			var rel string
			switch {
			case isPkg && strings.HasPrefix(name, "Library/bin/"):
				rel, bins = strings.TrimPrefix(name, "Library/bin/"), bins+1
			case isInfo && strings.HasPrefix(name, "info/licenses/"):
				rel = "licenses/" + strings.TrimPrefix(name, "info/licenses/")
			case isInfo && name == "info/about.json":
				rel = "licenses/about.json"
			default:
				continue
			}
			target, err := safeJoin(dest, rel)
			if err == nil {
				err = os.MkdirAll(filepath.Dir(target), 0o755)
			}
			if err == nil {
				err = writeFile(target, tr, 0o755)
			}
			if err != nil {
				zd.Close()
				rc.Close()
				return err
			}
		}
		zd.Close()
		rc.Close()
	}
	if bins == 0 {
		return fmt.Errorf("the conda package contains no Windows binaries (Library/bin)")
	}
	return nil
}

// bundledNames returns the sorted IDs.
func bundledNames(list []BundledEngine) []string {
	var ids []string
	for _, e := range list {
		ids = append(ids, e.ID)
	}
	sort.Strings(ids)
	return ids
}
