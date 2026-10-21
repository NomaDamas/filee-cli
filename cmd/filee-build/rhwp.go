package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

var linuxTriples = map[string]string{"linux-x64": "x86_64-unknown-linux-gnu", "linux-arm64": "aarch64-unknown-linux-gnu"}

// cmdRhwp builds the Linux rhwp program from source on Ubuntu 22.04, so it runs on glibc 2.35 (rhwp's own Linux
// release needs a newer glibc). Pinned commit and Rust toolchain come from data/pins.json, which must agree with
// upstream's engines.json.
//
// Native host (x64 on x64, arm64 on arm64): `cargo build --release --locked --bin rhwp`. Cross build (the arm64 target
// on an x64 host): the same plus --target aarch64-unknown-linux-gnu linked with gcc-aarch64-linux-gnu.
// The output folder has the layout of rhwp's release archive plus SOURCE.txt.
func cmdRhwp(args []string) error {
	fs := newFlags("rhwp")
	rid := fs.String("rid", "", "linux-x64 or linux-arm64")
	output := fs.String("output", "", "folder to create (must not exist)")
	work := fs.String("work", "", "scratch folder (default: a temporary one)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	triple, ok := linuxTriples[*rid]
	if !ok || *output == "" {
		return fmt.Errorf("usage: rhwp --rid linux-x64|linux-arm64 --output DIR [--work DIR]")
	}
	if runtime.GOOS != "linux" {
		return fmt.Errorf("rhwp for Linux is built on Linux")
	}
	out, _ := filepath.Abs(*output)
	if _, err := os.Stat(out); err == nil {
		return fmt.Errorf("%s already exists", out)
	}
	pins, err := readPins()
	if err != nil {
		return err
	}
	if err := checkEngineVersions(pins); err != nil {
		return err
	}
	scratch := *work
	if scratch == "" {
		if scratch, err = os.MkdirTemp("", "filee-rhwp-"); err != nil {
			return err
		}
	}
	scratch, _ = filepath.Abs(scratch)
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		return err
	}
	if _, err := exec.LookPath("rustup"); err != nil {
		return fmt.Errorf("rhwp needs rustup (https://rustup.rs)")
	}
	tags, err := run(runOpts{Capture: true}, "git", "ls-remote", pins.Rhwp.Repository, "refs/tags/"+pins.Rhwp.Tag, "refs/tags/"+pins.Rhwp.Tag+"^{}")
	if err == nil && !strings.Contains(tags, pins.Rhwp.Commit) {
		fmt.Printf("::warning::rhwp tag %s no longer points at the pinned commit %s; the pinned commit is built\n", pins.Rhwp.Tag, pins.Rhwp.Commit)
	}
	src := filepath.Join(scratch, "rhwp")
	if err := os.MkdirAll(src, 0o755); err != nil {
		return err
	}
	if err := gitCheckout(src, pins.Rhwp.Repository, pins.Rhwp.Commit); err != nil {
		return err
	}
	tc := pins.Rhwp.RustToolchain
	native := hostRID() == *rid
	install := []string{"toolchain", "install", tc, "--profile", "minimal", "--no-self-update"}
	if !native {
		install = append(install, "--target", triple)
	}
	if _, err := run(runOpts{}, "rustup", install...); err != nil {
		return err
	}
	targetDir := filepath.Join(scratch, "rhwp-target")
	env := []string{"CARGO_TARGET_DIR=" + targetDir}
	build := []string{"+" + tc, "build", "--release", "--locked", "--bin", "rhwp"}
	program := filepath.Join(targetDir, "release", "rhwp")
	how := fmt.Sprintf("cargo +%s build --release --locked --bin rhwp", tc)
	if !native {
		linker := "aarch64-linux-gnu-gcc"
		env = append(env,
			"CARGO_TARGET_"+strings.ToUpper(strings.ReplaceAll(triple, "-", "_"))+"_LINKER="+linker,
			"CC_"+strings.ReplaceAll(triple, "-", "_")+"="+linker)
		build = append(build, "--target", triple)
		program = filepath.Join(targetDir, triple, "release", "rhwp")
		how += " --target " + triple + " (cross-compiled on " + runtime.GOARCH + ")"
	}
	if _, err := run(runOpts{Dir: src, Env: env}, "cargo", build...); err != nil {
		return err
	}
	if native {
		version, err := run(runOpts{Capture: true}, program, "--version")
		if err != nil {
			return err
		}
		fmt.Println(strings.TrimSpace(version))
		if !strings.Contains(version, pins.Rhwp.Version) {
			return fmt.Errorf("rhwp --version printed %q, expected %s", strings.TrimSpace(version), pins.Rhwp.Version)
		}
	}
	if err := copyFile(program, filepath.Join(out, "rhwp")); err != nil {
		return err
	}
	for _, name := range []string{"LICENSE", "README.md", "README_EN.md"} {
		if _, err := os.Stat(filepath.Join(src, name)); err == nil {
			if err := copyFile(filepath.Join(src, name), filepath.Join(out, name)); err != nil {
				return err
			}
		}
	}
	note := fmt.Sprintf("rhwp built from source by filee-cli's build scripts on %s.\nSource: %s/tree/%s\nv%s; %s\n",
		distribution(), pins.Rhwp.Repository, pins.Rhwp.Commit, pins.Rhwp.Version, how)
	if err := os.WriteFile(filepath.Join(out, "SOURCE.txt"), []byte(note), 0o644); err != nil {
		return err
	}
	names, _ := listFiles(out)
	fmt.Printf("rhwp ready in %s: %s\n", out, strings.Join(names, ", "))
	return nil
}

func distribution() string {
	data, err := os.ReadFile("/etc/os-release")
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
				return strings.Trim(v, `"`)
			}
		}
	}
	return runtime.GOOS + "/" + runtime.GOARCH
}

type enginesManifest struct {
	Components map[string]engineComponent `json:"components"`
	Platforms  map[string]struct {
		Components map[string]engineComponent `json:"components"`
	} `json:"platforms"`
}

type engineComponent struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	Sha256  string `json:"sha256"`
	Kind    string `json:"kind"`
	Size    int64  `json:"size"`
}

func readEnginesManifest() (enginesManifest, error) {
	var m enginesManifest
	err := readJSON(filepath.Join(upstreamDir(), "src", "Filee.Engines", "Infrastructure", "engines.json"), &m)
	return m, err
}

func (m enginesManifest) forRID(rid string) map[string]engineComponent {
	if p, ok := m.Platforms[rid]; ok {
		return p.Components
	}
	return m.Components
}

// checkEngineVersions stops when upstream's engines.json pins other rhwp/7-Zip versions than data/pins.json describes.
func checkEngineVersions(pins pinsFile) error {
	m, err := readEnginesManifest()
	if err != nil {
		return fmt.Errorf("%w (run `filee-build fetch` first)", err)
	}
	var problems []string
	for _, rid := range rids {
		c := m.forRID(rid)
		if c["rhwp"].Version != pins.Rhwp.Version {
			problems = append(problems, fmt.Sprintf("engines.json pins rhwp %s for %s but data/pins.json describes %s", c["rhwp"].Version, rid, pins.Rhwp.Version))
		}
		if c["7zip"].Version != pins.SevenZip.Version {
			problems = append(problems, fmt.Sprintf("engines.json pins 7-Zip %s for %s but data/pins.json describes %s", c["7zip"].Version, rid, pins.SevenZip.Version))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s\nUpdate data/pins.json and the crate lists (docs/updating.md)", strings.Join(problems, "\n"))
	}
	return nil
}
