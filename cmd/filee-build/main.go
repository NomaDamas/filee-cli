// Command filee-build builds, checks, tests and releases the filee-cli packages. It is a single static Go binary:
// the only things it calls are git, the .NET SDK (dotnet publish), and on macOS codesign.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

const usage = `filee-build: build and release filee-cli packages from a pinned upstream Filee tag

  fetch                              check out the pinned upstream commit into .upstream/Filee, apply patches/
  rhwp    --rid R --output DIR       build rhwp from source for Linux (Ubuntu 22.04, glibc 2.35)
  package --rid R [--rhwp-dir DIR]   publish Filee.Cli, add engines, write notices, archive into dist/
  smoke   ARCHIVE                    unpack an archive and run it (version, conversions, exit codes)
  glibc   [--max 2.35] PATH...       fail when an ELF file needs a newer glibc
  crates                             regenerate data/*-crates.tsv (needs cargo)
  release DIR [--tag T]              write SHA256SUMS and RELEASE_NOTES.md for the five archives in DIR
  local   [flags]                    build and test packages on this computer (docker for Linux, ssh for Windows)

Run from the repository (or set FILEE_BUILD_ROOT). RIDs: win-x64 linux-x64 linux-arm64 osx-x64 osx-arm64.
`

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func main() { os.Exit(realMain(os.Args[1:])) }

func realMain(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(usage)
		return 0
	}
	commands := map[string]func([]string) error{
		"fetch": cmdFetch, "rhwp": cmdRhwp, "package": cmdPackage, "smoke": cmdSmoke, "glibc": cmdGlibc,
		"crates": cmdCrates, "release": cmdRelease, "local": cmdLocal,
	}
	fn, ok := commands[args[0]]
	if !ok {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	// smoke and glibc work on files you hand them, anywhere
	if args[0] != "smoke" && args[0] != "glibc" {
		if err := findRoot(); err != nil {
			return fail(err)
		}
	} else if err := findRoot(); err != nil {
		rootDir = "" // not inside the repository: only the explicit arguments are needed
	}
	if err := fn(args[1:]); err != nil {
		return fail(err)
	}
	return 0
}
