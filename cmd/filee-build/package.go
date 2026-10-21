package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Words that mean a GUI, tray, shell-integration or installer file. The package must contain none of them.
var forbidden = []string{"avalonia", "sharphook", "uiohook", "communitytoolkit", "filee.app", "filee.exe", "filee.dll",
	"filee.platform", "fileeexplorermenu", "setup", "installer", "install.sh", "uninstall", ".app/", ".msi",
	"inno", "materialdesign", "simpleicons", "notosans"}

var allowedFilee = map[string]bool{"filee-cli": true, "filee-cli.exe": true, "filee-cli.dll": true, "filee-cli.deps.json": true,
	"filee-cli.runtimeconfig.json": true, "filee.core.dll": true, "filee.engines.dll": true}

func cmdPackage(args []string) error {
	fl := newFlags("package")
	rid := fl.String("rid", "", "target runtime identifier")
	rhwpDir := fl.String("rhwp-dir", "", "folder with rhwp built for this RID (required for linux-*)")
	dist := fl.String("dist", filepath.Join(rootDir, "dist"), "output folder")
	identity := fl.String("codesign-identity", "-", "macOS signing identity ('-' = ad-hoc)")
	if err := fl.Parse(args); err != nil {
		return err
	}
	valid := false
	for _, r := range rids {
		valid = valid || r == *rid
	}
	if !valid {
		return fmt.Errorf("--rid must be one of %s", strings.Join(rids, ", "))
	}
	if strings.HasPrefix(*rid, "osx-") && runtime.GOOS != "darwin" {
		return fmt.Errorf("build macOS packages on macOS (codesign)")
	}
	up, err := readUpstream()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(upstreamDir(), "Filee.slnx")); err != nil {
		return fmt.Errorf("run `filee-build fetch` first")
	}
	epoch := time.Now().Unix()
	if v := os.Getenv("SOURCE_DATE_EPOCH"); v != "" {
		if epoch, err = strconv.ParseInt(v, 10, 64); err != nil {
			return fmt.Errorf("SOURCE_DATE_EPOCH: %w", err)
		}
	} else {
		out, err := run(runOpts{Capture: true, Quiet: true}, "git", "-C", upstreamDir(), "log", "-1", "--format=%ct")
		if err != nil {
			return err
		}
		if epoch, err = strconv.ParseInt(strings.TrimSpace(out), 10, 64); err != nil {
			return err
		}
	}
	built := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	name := packageName(up, *rid)
	stageRoot := filepath.Join(workDir(), *rid)
	if err := rmtree(stageRoot); err != nil {
		return err
	}
	scratch := filepath.Join(stageRoot, "scratch")
	pkg := filepath.Join(stageRoot, name)
	publishDir := filepath.Join(stageRoot, "publish")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		return err
	}

	if err := publish(*rid, up.UpstreamVersion, publishDir); err != nil {
		return err
	}
	if err := copyTree(publishDir, pkg); err != nil {
		return err
	}
	info, err := addEngines(*rid, pkg, *rhwpDir, scratch)
	if err != nil {
		return err
	}
	if err := copyFile(filepath.Join(rootDir, "LICENSE"), filepath.Join(pkg, "LICENSE")); err != nil {
		return err
	}
	if err := generateNotices(pkg, publishDir, *rid, info); err != nil {
		return err
	}
	exe := "filee-cli"
	if strings.HasPrefix(*rid, "win-") {
		exe = "filee-cli.exe"
	}
	linux := ""
	if strings.HasPrefix(*rid, "linux-") {
		linux = linuxNote
	}
	readme := fmt.Sprintf(packageReadme, up.Version, *rid, exe, exe, exe, exe, linux)
	if err := os.WriteFile(filepath.Join(pkg, "README.md"), []byte(readme), 0o644); err != nil {
		return err
	}
	pins, err := readPins()
	if err != nil {
		return err
	}
	rhwpNote := fmt.Sprintf("release build %s, sha256 %s", info.Rhwp.URL, info.Rhwp.Sha256)
	if *rhwpDir != "" {
		rhwpNote = fmt.Sprintf("built from source, tag %s commit %s, cargo +%s build --release --locked --bin rhwp", pins.Rhwp.Tag, pins.Rhwp.Commit, pins.Rhwp.RustToolchain)
	}
	sdk, err := run(runOpts{Dir: upstreamDir(), Capture: true, Quiet: true}, "dotnet", "--version")
	if err != nil {
		return err
	}
	if err := writeUpstreamMD(pkg, *rid, up, info, strings.TrimSpace(sdk), built, rhwpNote); err != nil {
		return err
	}
	if strings.HasPrefix(*rid, "osx-") {
		if err := codesignMacOS(pkg, *identity); err != nil {
			return err
		}
	}
	listing, err := checkNoGUI(pkg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*dist, 0o755); err != nil {
		return err
	}
	archive := filepath.Join(*dist, archiveName(up, *rid))
	os.Remove(archive)
	if err := writeArchive(pkg, archive, *rid, epoch); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*dist, name+".files.txt"), []byte(strings.Join(listing, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	st, _ := os.Stat(archive)
	sum, _ := sha256File(archive)
	fmt.Printf("%s (%.1f MB), %d files, sha256 %s\n", archive, float64(st.Size())/1e6, len(listing), sum)
	return nil
}

func publish(rid, version, dest string) error {
	if err := rmtree(dest); err != nil {
		return err
	}
	_, err := run(runOpts{Dir: upstreamDir()}, "dotnet", "publish", filepath.Join(upstreamDir(), "src", "Filee.Cli", "Filee.Cli.csproj"),
		"-c", "Release", "-r", rid, "--self-contained", "true", "-p:Version="+version, "-p:FileeRelease=true",
		"-p:PublishReadyToRun=false", "-o", dest) // cwd: upstream's global.json selects the SDK
	if err != nil {
		return err
	}
	// Debug symbols and XML documentation are not needed to run (libSkiaSharp.pdb alone is 84 MB on Windows).
	return filepath.WalkDir(dest, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && (strings.HasSuffix(p, ".pdb") || strings.HasSuffix(p, ".xml")) {
			return os.Remove(p)
		}
		return err
	})
}

// fetchEngine downloads one pinned engine (SHA-256 checked), unpacks it, and returns the folder with its files.
func fetchEngine(name string, c engineComponent, scratch string) (string, error) {
	archive, err := download(c.URL, c.Sha256, filepath.Join(cacheDir(), "downloads"))
	if err != nil {
		return "", err
	}
	target := filepath.Join(scratch, name+"-x")
	if c.Kind == "msi" {
		if runtime.GOOS != "windows" {
			return "", fmt.Errorf("the Windows 7-Zip MSI is unpacked with msiexec: build win-x64 on Windows")
		}
		named := filepath.Join(scratch, name+".msi")
		if err := copyFile(archive, named); err != nil {
			return "", err
		}
		if _, err := run(runOpts{}, "msiexec.exe", "/a", named, "/qn", "TARGETDIR="+target); err != nil {
			return "", err
		}
		return target, nil
	}
	if err := extractArchive(archive, c.Kind, target); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return "", err
	}
	if len(entries) == 1 && entries[0].IsDir() {
		return filepath.Join(target, entries[0].Name()), nil
	}
	return target, nil
}

func addEngines(rid, pkg, rhwpDir, scratch string) (EnginesInfo, error) {
	var info EnginesInfo
	manifest, err := readEnginesManifest()
	if err != nil {
		return info, err
	}
	pins, err := readPins()
	if err != nil {
		return info, err
	}
	if err := checkEngineVersions(pins); err != nil {
		return info, err
	}
	components := manifest.forRID(rid)
	windows := strings.HasPrefix(rid, "win-")
	info.EnginesJSON = filepath.Join(upstreamDir(), "src", "Filee.Engines", "Infrastructure", "engines.json")
	engines := filepath.Join(pkg, "engines")
	if err := os.MkdirAll(engines, 0o755); err != nil {
		return info, err
	}

	rhwpTarget := filepath.Join(engines, "rhwp")
	switch {
	case rhwpDir != "":
		if _, err := os.Stat(filepath.Join(rhwpDir, "rhwp")); err != nil {
			return info, fmt.Errorf("%s has no rhwp program", rhwpDir)
		}
		if err := copyTree(rhwpDir, rhwpTarget); err != nil {
			return info, err
		}
		info.Rhwp.URL = "built from source (filee-build rhwp)"
	case strings.HasPrefix(rid, "linux-"):
		return info, fmt.Errorf("linux-* packages need --rhwp-dir (rhwp's Linux release needs a newer glibc than Ubuntu 22.04)")
	default:
		src, err := fetchEngine("rhwp", components["rhwp"], scratch)
		if err != nil {
			return info, err
		}
		if err := copyTree(src, rhwpTarget); err != nil {
			return info, err
		}
		info.Rhwp.URL, info.Rhwp.Sha256 = components["rhwp"].URL, components["rhwp"].Sha256
	}
	program := filepath.Join(rhwpTarget, "rhwp")
	if windows {
		program += ".exe"
	}
	if _, err := os.Stat(program); err != nil {
		return info, fmt.Errorf("%s is missing", program)
	}

	z := components["7zip"]
	src, err := fetchEngine("7zip", z, scratch)
	if err != nil {
		return info, err
	}
	zipTarget := filepath.Join(engines, "7zip")
	if err := os.MkdirAll(zipTarget, 0o755); err != nil {
		return info, err
	}
	keep := []string{"7zz", "License.txt", "readme.txt"}
	if windows {
		keep = []string{"7z.exe", "7z.dll", "License.txt", "readme.txt"}
	}
	found := map[string]string{}
	filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			for _, k := range keep {
				if d.Name() == k {
					if _, dup := found[k]; !dup {
						found[k] = p
					}
				}
			}
		}
		return nil
	})
	for _, k := range keep {
		if found[k] == "" {
			return info, fmt.Errorf("7-Zip archive lacks %s", k)
		}
		if err := copyFile(found[k], filepath.Join(zipTarget, k)); err != nil {
			return info, err
		}
	}
	info.SevenZip.URL, info.SevenZip.Sha256 = z.URL, z.Sha256

	info.Bundled, err = installEngines(rid, engines, scratch)
	if err != nil {
		return info, err
	}

	if !windows {
		for _, p := range []string{program, filepath.Join(zipTarget, "7zz"), filepath.Join(pkg, "filee-cli")} {
			if err := os.Chmod(p, 0o755); err != nil {
				return info, err
			}
		}
	}
	return info, nil
}

func isMachO(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	m := make([]byte, 4)
	if n, _ := f.Read(m); n != 4 {
		return false
	}
	switch {
	case bytes.Equal(m, []byte{0xcf, 0xfa, 0xed, 0xfe}), bytes.Equal(m, []byte{0xce, 0xfa, 0xed, 0xfe}), bytes.Equal(m, []byte{0xca, 0xfe, 0xba, 0xbe}):
		return true
	}
	return false
}

// vendorSigned: the application bundles of calibre and LibreOffice keep the vendor's own signature, which signing a
// single file inside would break.
func vendorSigned(rel string) bool {
	return strings.HasPrefix(rel, "engines/calibre/") || strings.HasPrefix(rel, "engines/libreoffice/")
}

// codesignMacOS signs every Mach-O file (the main program last) and verifies the program.
func codesignMacOS(pkg, identity string) error {
	options := []string{"--force", "--sign", identity}
	if identity != "-" {
		options = append(options, "--timestamp", "--options", "runtime")
	}
	entitlements := filepath.Join(dataDir(), "entitlements.plist")
	files, err := listFiles(pkg)
	if err != nil {
		return err
	}
	sort.Slice(files, func(i, j int) bool { return len(files[i]) > len(files[j]) })
	main := filepath.Join(pkg, "filee-cli")
	for _, rel := range files {
		p := filepath.Join(pkg, filepath.FromSlash(rel))
		if p == main || !isMachO(p) || vendorSigned(rel) {
			continue
		}
		if _, err := run(runOpts{Quiet: true}, "codesign", append(append([]string{}, options...), "--entitlements", entitlements, p)...); err != nil {
			return err
		}
	}
	if _, err := run(runOpts{}, "codesign", append(append([]string{}, options...), "--entitlements", entitlements, main)...); err != nil {
		return err
	}
	_, err = run(runOpts{}, "codesign", "--verify", "--strict", main)
	return err
}

// checkNoGUI fails when a GUI, tray, installer or shell-integration file is in the package; returns the file list.
func checkNoGUI(pkg string) ([]string, error) {
	files, err := listFiles(pkg)
	if err != nil {
		return nil, err
	}
	var offenders []string
	for _, rel := range files {
		lower := strings.ToLower(rel)
		if strings.HasPrefix(lower, "licenses/") || lower == "third-party-notices.md" || lower == "upstream.md" || lower == "readme.md" {
			continue // texts that talk about those components
		}
		if strings.HasPrefix(lower, "engines/") {
			continue // third-party programs with their own files (LibreOffice has a fileexporteddialog.ui)
		}
		base := path.Base(lower)
		if strings.HasPrefix(base, "filee") && !allowedFilee[base] {
			offenders = append(offenders, rel)
			continue
		}
		for _, w := range forbidden {
			if strings.Contains(lower, w) {
				offenders = append(offenders, rel)
				break
			}
		}
	}
	if len(offenders) > 0 {
		return nil, fmt.Errorf("GUI, tray, installer or shell-integration files in the package:\n  %s", strings.Join(offenders, "\n  "))
	}
	return files, nil
}

func sha256Hex(path string) string { s, _ := sha256File(path); return s }

func writeUpstreamMD(pkg, rid string, up Upstream, info EnginesInfo, sdk, built, rhwpNote string) error {
	patches, _ := filepath.Glob(filepath.Join(rootDir, "patches", "*.patch"))
	patchText := "- none (built from the unmodified upstream source)"
	if len(patches) > 0 {
		var lines []string
		for _, p := range patches {
			lines = append(lines, fmt.Sprintf("- `%s` (sha256 %s)", filepath.Base(p), sha256Hex(p)))
		}
		patchText = strings.Join(lines, "\n")
	}
	ci := ""
	if os.Getenv("GITHUB_ACTIONS") != "" {
		ci = fmt.Sprintf("\n## Build\n\n- Workflow run: %s/%s/actions/runs/%s\n- This repository at: `%s`\n- Runner: %s %s\n",
			os.Getenv("GITHUB_SERVER_URL"), os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_RUN_ID"), os.Getenv("GITHUB_SHA"), os.Getenv("RUNNER_OS"), os.Getenv("RUNNER_ARCH"))
	}
	text := fmt.Sprintf(`# filee-cli %[1]s (%[2]s)

**Unofficial redistribution** of the command line of Filee. Not made, reviewed or endorsed by the Filee author.

## Corresponding source

- Upstream: %[3]s
- Tag: `+"`%[4]s`"+`
- Commit: `+"`%[5]s`"+` (%[3]s/tree/%[5]s)
- Projects built: `+"`src/Filee.Cli`"+` with `+"`Filee.Core`"+` and `+"`Filee.Engines`"+`. Nothing else of upstream is built or included.
- Patches applied to the upstream source:
%[6]s
- Packaging revision: %[7]s (package version %[1]s = upstream %[8]s + packaging revision)
- Build and packaging program: this repository (`+"`cmd/filee-build`"+`, see its README.md)

## Build

- Built (UTC): %[9]s
- .NET SDK: %[10]s
- Command: `+"`dotnet publish src/Filee.Cli/Filee.Cli.csproj -c Release -r %[2]s --self-contained true -p:Version=%[8]s -p:FileeRelease=true -p:PublishReadyToRun=false`"+`
- After publishing, `+"`*.pdb`"+` and `+"`*.xml`"+` documentation files were deleted; nothing else was changed.
- Engines: rhwp (%[11]s), 7-Zip (%[12]s, sha256 %[13]s).
%[14]s`, up.Version, rid, up.Repository, up.Tag, up.Commit, patchText, up.Revision, up.UpstreamVersion, built, sdk, rhwpNote, info.SevenZip.URL, info.SevenZip.Sha256, ci)
	return os.WriteFile(filepath.Join(pkg, "UPSTREAM.md"), []byte(text), 0o644)
}

const packageReadme = "# filee-cli %s (%s)\n\n" +
	"Unofficial redistribution of the command line of [Filee](https://github.com/KnifeLemon/Filee) (MIT). It contains no\n" +
	"GUI, tray, Explorer menu or installer. See `UPSTREAM.md` for the exact upstream commit, `LICENSE` and\n" +
	"`THIRD-PARTY-NOTICES.md` for licenses.\n\n" +
	"Run `%s version`, `%s formats`, `%s convert photo.png --to webp --json`. Put this folder (or a link to\n" +
	"`%s`) on your `PATH` to use it from other programs. The `engines/` folder must stay next to the program.\n\n" +
	"Larger engines (FFmpeg, Pandoc, LibreOffice, Ghostscript, calibre) are not included; if you install them yourself and\n" +
	"they are on your `PATH`, the program finds them.\n%s"

const linuxNote = "\nLinux needs the system libraries .NET itself needs, e.g. on Ubuntu 22.04:\n" +
	"`sudo apt-get install -y ca-certificates libc6 libgcc-s1 libicu70 libssl3 libstdc++6 tzdata zlib1g`\n\n" +
	"The bundled calibre (`engines/calibre`, e-book conversion) is a Qt program and needs these libraries even though it runs\n" +
	"without a display: `sudo apt-get install -y libxcb1 libxkbcommon0 libfontconfig1 libfreetype6 libx11-6 libegl1\n" +
	"libopengl0 libglx0`. Without them only the calibre conversions fail (the error names the missing library).\n\n" +
	"HWP/HWPX to PDF (rhwp) draws text with fonts installed on the computer; it embeds none. Install a Korean font\n" +
	"(for example `fonts-noto-cjk`) or Hangul will not render. The release tests ran with `fonts-noto-cjk`.\n"

// writeArchive creates a reproducible archive (fixed times, owners and modes) with one top-level folder.
func writeArchive(pkg, dest, rid string, epoch int64) error {
	top := filepath.Base(pkg)
	mtime := time.Unix(epoch, 0).UTC()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	type entry struct {
		rel    string
		isDir  bool
		exec   bool
		linkTo string // symlink target (engine bundles such as LibreOffice.app contain symlinks)
	}
	var entries []entry
	err = filepath.WalkDir(pkg, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == pkg {
			return err
		}
		rel, _ := filepath.Rel(pkg, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			if strings.HasPrefix(rid, "win-") {
				return fmt.Errorf("%s is a symlink, which a zip package cannot carry portably", p)
			}
			to, err := os.Readlink(p)
			if err != nil {
				return err
			}
			entries = append(entries, entry{rel: filepath.ToSlash(rel), linkTo: to})
			return nil
		}
		if !d.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", p)
		}
		entries = append(entries, entry{rel: filepath.ToSlash(rel), isDir: d.IsDir(), exec: info.Mode()&0o111 != 0})
		return nil
	})
	if err != nil {
		return err
	}

	if strings.HasPrefix(rid, "win-") {
		zw := zip.NewWriter(out)
		zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) { return flate.NewWriter(w, flate.BestCompression) })
		for _, e := range entries {
			if e.isDir {
				continue
			}
			mode := fs.FileMode(0o644)
			if e.exec {
				mode = 0o755
			}
			h := &zip.FileHeader{Name: top + "/" + e.rel, Method: zip.Deflate, Modified: mtime}
			h.SetMode(mode)
			w, err := zw.CreateHeader(h)
			if err != nil {
				return err
			}
			if err := copyInto(w, filepath.Join(pkg, filepath.FromSlash(e.rel))); err != nil {
				return err
			}
		}
		if err := zw.Close(); err != nil {
			return err
		}
		return out.Close()
	}

	gz, err := gzip.NewWriterLevel(out, gzip.BestCompression)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: top + "/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: mtime, Format: tar.FormatPAX}); err != nil {
		return err
	}
	for _, e := range entries {
		h := &tar.Header{Name: top + "/" + e.rel, ModTime: mtime, Format: tar.FormatPAX}
		if e.linkTo != "" {
			h.Typeflag, h.Mode, h.Linkname = tar.TypeSymlink, 0o777, e.linkTo
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			continue
		}
		if e.isDir {
			h.Typeflag, h.Mode, h.Name = tar.TypeDir, 0o755, h.Name+"/"
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			continue
		}
		p := filepath.Join(pkg, filepath.FromSlash(e.rel))
		st, err := os.Stat(p)
		if err != nil {
			return err
		}
		h.Typeflag, h.Size, h.Mode = tar.TypeReg, st.Size(), 0o644
		if e.exec {
			h.Mode = 0o755
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if err := copyInto(tw, p); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return out.Close()
}

func copyInto(w io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}
