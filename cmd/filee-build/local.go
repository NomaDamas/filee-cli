package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// cmdLocal builds, checks and smoke-tests packages on this computer instead of GitHub Actions:
//   - osx-*          run directly (needs the .NET SDK and codesign), osx-x64 on Apple silicon runs under Rosetta
//   - linux-*        in an Ubuntu 22.04 Docker container (glibc 2.35 build), then in a second, clean container that has only
//     the runtime libraries a user installs; linux-arm64 on an x64 host is cross-built and tested under emulation
//   - win-x64        over SSH on a Windows machine that has git and the .NET SDK
//
// With --publish it then creates the git tag and the GitHub release (gh) from the five archives.
func cmdLocal(args []string) error {
	fl := newFlags("local")
	ridList := fl.String("rids", strings.Join(rids, ","), "comma-separated RIDs to build")
	dist := fl.String("dist", filepath.Join(rootDir, "dist"), "where the archives go")
	winHost := fl.String("windows-host", "windows", "ssh host for win-x64")
	winDir := fl.String("windows-dir", `C:\filee-build`, "work folder on the Windows host")
	winDotnet := fl.String("windows-dotnet", `C:\dotnet-sdk`, "folder of the .NET SDK on the Windows host")
	image := fl.String("image", "ubuntu:22.04", "Docker image for Linux builds (must be glibc 2.35)")
	publishRelease := fl.Bool("publish", false, "after all five archives pass: tag, push the tag, and create the GitHub release")
	slug := fl.String("repo", "NomaDamas/filee-cli", "owner/repo for --publish")
	skipBuild := fl.Bool("skip-build", false, "only assemble (and with --publish, release) archives already in --dist")
	if err := fl.Parse(args); err != nil {
		return err
	}
	up, err := readUpstream()
	if err != nil {
		return err
	}
	selected := strings.Split(*ridList, ",")
	distAbs, _ := filepath.Abs(*dist)
	if err := os.MkdirAll(distAbs, 0o755); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}

	if !*skipBuild {
		for _, rid := range selected {
			rid = strings.TrimSpace(rid)
			fmt.Printf("\n=== %s ===\n", rid)
			var err error
			switch {
			case strings.HasPrefix(rid, "osx-"):
				err = localMac(self, rid, distAbs, up)
			case strings.HasPrefix(rid, "linux-"):
				err = localLinux(rid, distAbs, up, *image)
			case rid == "win-x64":
				err = localWindows(rid, distAbs, up, *winHost, *winDir, *winDotnet)
			default:
				err = fmt.Errorf("unknown RID %q", rid)
			}
			if err != nil {
				return fmt.Errorf("%s: %w", rid, err)
			}
		}
	}

	all := true
	for _, rid := range rids {
		if _, err := os.Stat(filepath.Join(distAbs, archiveName(up, rid))); err != nil {
			all = false
		}
	}
	if !all {
		fmt.Println("\nNot all five archives are in", distAbs, "- release files not written.")
		if *publishRelease {
			return fmt.Errorf("--publish needs all five archives")
		}
		return nil
	}
	tag := "v" + up.Version
	// The release folder holds exactly the five archives and the two generated files.
	releaseDir := filepath.Join(workDir(), "release")
	if err := rmtree(releaseDir); err != nil {
		return err
	}
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		return err
	}
	for _, rid := range rids {
		if err := copyFile(filepath.Join(distAbs, archiveName(up, rid)), filepath.Join(releaseDir, archiveName(up, rid))); err != nil {
			return err
		}
	}
	// Corresponding Source of the bundled GPL/LGPL/AGPL engines: built (or reused) next to the packages.
	if err := verifySources(); err != nil {
		return err
	}
	sourcesDir := filepath.Join(distAbs, "sources")
	if err := cmdSources([]string{"--dist", sourcesDir}); err != nil {
		return err
	}
	srcFiles, _ := filepath.Glob(filepath.Join(sourcesDir, "filee-cli-*-source-*"))
	for _, f := range srcFiles {
		if err := copyFile(f, filepath.Join(releaseDir, filepath.Base(f))); err != nil {
			return err
		}
	}
	fmt.Println("\n=== release files ===")
	if err := cmdRelease([]string{releaseDir, "--tag", tag}); err != nil {
		return err
	}
	if !*publishRelease {
		fmt.Printf("\nRelease files are in %s. Run with --publish to tag %s and create the GitHub release.\n", releaseDir, tag)
		return nil
	}
	return publishGitHub(releaseDir, tag, *slug, up)
}

func localMac(self, rid, dist string, up Upstream) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("macOS packages are built on macOS")
	}
	for _, step := range [][]string{{"fetch"}, {"package", "--rid", rid, "--dist", dist}} {
		if _, err := run(runOpts{Dir: rootDir}, self, step...); err != nil {
			return err
		}
	}
	_, err := run(runOpts{Dir: rootDir}, self, "smoke", filepath.Join(dist, archiveName(up, rid)))
	return err
}

// ---- Linux ---------------------------------------------------------------------------------------------------------

const buildScript = `set -euxo pipefail
export DEBIAN_FRONTEND=noninteractive DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_NOLOGO=1 FILEE_BUILD_ROOT=/repo
apt-get update -qq
apt-get install -y -qq --no-install-recommends build-essential pkg-config git curl ca-certificates libicu70 libssl3 tzdata zlib1g unzip xz-utils $EXTRA_PACKAGES >/dev/null
cd /repo
/filee-build fetch
curl -fsSL https://dot.net/v1/dotnet-install.sh -o /tmp/dotnet-install.sh
bash /tmp/dotnet-install.sh --jsonfile .upstream/Filee/global.json --install-dir /opt/dotnet >/dev/null
export PATH=/opt/dotnet:$PATH
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --profile minimal --default-toolchain none >/dev/null
export PATH=$HOME/.cargo/bin:$PATH
/filee-build rhwp --rid "$RID" --output /tmp/rhwp --work /tmp/rhwp-work
/filee-build glibc --max 2.35 --needed /tmp/rhwp
/filee-build package --rid "$RID" --rhwp-dir /tmp/rhwp --dist /out
mkdir -p /tmp/unpacked && tar -xzf /out/filee-cli-*-"$RID".tar.gz -C /tmp/unpacked
/filee-build glibc --max 2.35 /tmp/unpacked | tail -3
`

const smokeScript = `set -euxo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq --no-install-recommends ca-certificates libc6 libgcc-s1 libicu70 libssl3 libstdc++6 tzdata zlib1g fonts-noto-cjk libxcb1 libxkbcommon0 libfontconfig1 libfreetype6 libx11-6 libegl1 libopengl0 libglx0 >/dev/null
/filee-build smoke /out/filee-cli-*-"$RID".tar.gz
`

func goArch(rid string) string {
	if strings.HasSuffix(rid, "arm64") {
		return "arm64"
	}
	return "amd64"
}

func goBuild(goos, goarch, output string) error {
	_, err := run(runOpts{Dir: rootDir, Env: []string{"CGO_ENABLED=0", "GOOS=" + goos, "GOARCH=" + goarch}}, "go", "build", "-trimpath", "-o", output, "./cmd/filee-build")
	return err
}

func localLinux(rid, dist string, up Upstream, image string) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker is needed for Linux builds")
	}
	arch := goArch(rid)
	buildArch := arch
	extra := ""
	// linux-arm64 on an x64 host is cross-built (rhwp with the aarch64 cross toolchain); linux-x64 on an arm64 host runs
	// in an emulated amd64 container.
	if rid == "linux-arm64" && runtime.GOARCH == "amd64" {
		buildArch = "amd64"
		extra = "gcc-aarch64-linux-gnu libc6-dev-arm64-cross"
	}
	tmp, err := os.MkdirTemp("", "filee-linux-")
	if err != nil {
		return err
	}
	defer rmtree(tmp)
	repoCopy, out := filepath.Join(tmp, "repo"), filepath.Join(tmp, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	if err := copyWorkingTree(rootDir, repoCopy); err != nil {
		return err
	}
	cacheHost := filepath.Join(cacheDir(), "linux")
	if err := os.MkdirAll(cacheHost, 0o755); err != nil {
		return err
	}
	binBuild, binSmoke := filepath.Join(tmp, "filee-build-"+buildArch), filepath.Join(tmp, "filee-build-"+arch)
	for _, b := range [][2]string{{buildArch, binBuild}, {arch, binSmoke}} {
		if err := goBuild("linux", b[0], b[1]); err != nil {
			return err
		}
	}
	script := func(name, text string) string {
		p := filepath.Join(tmp, name)
		os.WriteFile(p, []byte(text), 0o755)
		return p
	}
	docker := func(platform, bin, scriptPath string, env ...string) error {
		args := []string{"run", "--rm", "--platform", "linux/" + platform,
			"-v", repoCopy + ":/repo", "-v", cacheHost + ":/repo/.cache", "-v", out + ":/out",
			"-v", bin + ":/filee-build:ro", "-v", scriptPath + ":/script.sh:ro", "-w", "/repo"}
		for _, e := range env {
			args = append(args, "-e", e)
		}
		args = append(args, image, "bash", "/script.sh")
		_, err := run(runOpts{}, "docker", args...)
		return err
	}
	if err := docker(buildArch, binBuild, script("build.sh", buildScript), "RID="+rid, "EXTRA_PACKAGES="+extra); err != nil {
		return err
	}
	// A second, clean container with only what a user installs: proves the package does not lean on build tools.
	if err := docker(arch, binSmoke, script("smoke.sh", smokeScript), "RID="+rid); err != nil {
		return err
	}
	for _, name := range []string{archiveName(up, rid), packageName(up, rid) + ".files.txt"} {
		if err := copyFile(filepath.Join(out, name), filepath.Join(dist, name)); err != nil {
			return err
		}
	}
	return nil
}

// copyWorkingTree copies the repository without generated or local-only folders, so uncommitted edits are built.
func copyWorkingTree(src, dst string) error {
	skip := map[string]bool{".git": true, ".upstream": true, ".cache": true, "work": true, "dist": true}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if skip[strings.Split(filepath.ToSlash(rel), "/")[0]] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		return copyFile(p, filepath.Join(dst, rel))
	})
}

// ---- Windows over SSH ----------------------------------------------------------------------------------------------

func localWindows(rid, dist string, up Upstream, host, dir, dotnet string) error {
	tmp, err := os.MkdirTemp("", "filee-win-")
	if err != nil {
		return err
	}
	defer rmtree(tmp)
	exe := filepath.Join(tmp, "filee-build.exe")
	if err := goBuild("windows", "amd64", exe); err != nil {
		return err
	}
	bundle := filepath.Join(tmp, "repo.tgz")
	if err := tarWorkingTree(rootDir, bundle); err != nil {
		return err
	}
	remote := func(command string) error {
		_, err := run(runOpts{}, "ssh", "-o", "ServerAliveInterval=30", "-o", "ServerAliveCountMax=40", host, command)
		return err
	}
	scpTarget := func(p string) string { return host + ":" + strings.ReplaceAll(p, `\`, "/") }
	// The download cache lives outside the work folder (linked in as .cache) so engine downloads survive between runs;
	// the link is removed first, so clearing the work folder never touches the cache.
	cache := dir + "-cache"
	if err := remote(fmt.Sprintf(`(if exist "%[1]s\.cache" rmdir "%[1]s\.cache") & (if exist "%[1]s" rmdir /s /q "%[1]s") & mkdir "%[1]s" & (if not exist "%[2]s" mkdir "%[2]s") & mklink /J "%[1]s\.cache" "%[2]s"`, dir, cache)); err != nil {
		return err
	}
	for _, f := range []string{bundle, exe} {
		if _, err := run(runOpts{}, "scp", "-q", f, scpTarget(dir+`\`+filepath.Base(f))); err != nil {
			return err
		}
	}
	env := fmt.Sprintf(`cd /d "%s" && set "PATH=%s;%%PATH%%" && set "DOTNET_CLI_TELEMETRY_OPTOUT=1" && set "FILEE_BUILD_ROOT=%s" && `, dir, dotnet, dir)
	steps := []string{
		`tar -xzf repo.tgz`,
		`filee-build.exe fetch`,
		`filee-build.exe package --rid ` + rid,
		`filee-build.exe smoke dist\` + archiveName(up, rid),
	}
	if err := remote(env + strings.Join(steps, " && ")); err != nil {
		return err
	}
	for _, name := range []string{archiveName(up, rid), packageName(up, rid) + ".files.txt"} {
		if _, err := run(runOpts{}, "scp", "-q", scpTarget(dir+`\dist\`+name), filepath.Join(dist, name)); err != nil {
			return err
		}
	}
	return nil
}

func tarWorkingTree(src, dest string) error {
	skip := map[string]bool{".git": true, ".upstream": true, ".cache": true, "work": true, "dist": true}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == src {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if skip[strings.Split(filepath.ToSlash(rel), "/")[0]] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		h, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		return copyInto(tw, p)
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return out.Close()
}

// ---- GitHub release ------------------------------------------------------------------------------------------------

// publishGitHub tags the current commit, pushes the tag, and creates the release from releaseDir. It refuses to run
// with uncommitted changes (the tag must name the code that built the assets) or when the tag already exists.
func publishGitHub(releaseDir, tag, slug string, up Upstream) error {
	status, err := run(runOpts{Dir: rootDir, Capture: true, Quiet: true}, "git", "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("uncommitted changes: commit them first, the tag must name the code that was built")
	}
	if existing, _ := run(runOpts{Dir: rootDir, Capture: true, Quiet: true}, "git", "tag", "--list", tag); strings.TrimSpace(existing) != "" {
		return fmt.Errorf("tag %s already exists", tag)
	}
	ahead, err := run(runOpts{Dir: rootDir, Capture: true, Quiet: true}, "git", "rev-list", "--count", "@{u}..HEAD")
	if err == nil && strings.TrimSpace(ahead) != "0" {
		return fmt.Errorf("%s unpushed commit(s): push main first", strings.TrimSpace(ahead))
	}
	if _, err := run(runOpts{Dir: rootDir}, "git", "tag", "-a", tag, "-m", "filee-cli "+up.Version); err != nil {
		return err
	}
	if _, err := run(runOpts{Dir: rootDir}, "git", "push", "origin", tag); err != nil {
		return err
	}
	files, _ := filepath.Glob(filepath.Join(releaseDir, "filee-cli-*"))
	args := append([]string{"release", "create", tag, "--repo", slug, "--verify-tag", "--title", "filee-cli " + up.Version,
		"--notes-file", filepath.Join(releaseDir, "RELEASE_NOTES.md"), "--draft"}, files...)
	args = append(args, filepath.Join(releaseDir, "SHA256SUMS"))
	if _, err := run(runOpts{Dir: rootDir}, "gh", args...); err != nil {
		return err
	}
	_, err = run(runOpts{Dir: rootDir}, "gh", "release", "edit", tag, "--repo", slug, "--draft=false")
	return err
}
