# Building and releasing

Everything is done by `filee-build` (Go, `cmd/filee-build`). Requirements on the machine that builds:

| Tool | Needed for |
|---|---|
| Go ≥ 1.25 | building `filee-build` itself (one `go build`, no other Go dependency than a small xz reader) |
| git | fetching the pinned upstream commit |
| .NET SDK (version in upstream's `global.json`) | `dotnet publish` of `Filee.Cli`: build time only; the packages are self-contained |
| Xcode command line tools | macOS `codesign` |
| Docker | Linux packages (`ubuntu:22.04`; on an Apple-silicon Mac `linux-arm64` is native and `linux-x64` runs under emulation) |
| ssh + scp to a Windows machine with git and the .NET SDK | `win-x64` (the 7-Zip MSI is unpacked with `msiexec`) |
| gh (logged in) | `--publish` |

## Building locally (without GitHub Actions)

The GitHub workflows below build the same packages on GitHub's runners (free for a public repository). To build on your
own computers instead, `filee-build local` does the same:

```sh
go build -o filee-build ./cmd/filee-build
export PATH=/path/to/dotnet:$PATH           # the SDK for macOS
./filee-build local                         # all five RIDs, archives in dist/
./filee-build local --rids osx-arm64,linux-arm64
./filee-build local --publish               # needs all five; tags, pushes the tag, creates the release
```

What each RID does:

| RID | Where | Steps |
|---|---|---|
| `osx-arm64`, `osx-x64` | this Mac (osx-x64 under Rosetta) | `fetch`, `package` (ad-hoc codesign), `smoke` |
| `linux-x64`, `linux-arm64` | Docker `ubuntu:22.04`, matching CPU (emulated when it differs), repository copied in, `.cache` shared | install toolchain, `fetch`, build rhwp from source, glibc check of rhwp, `package`, glibc check of every ELF file; then a **second, clean** container with only the runtime libraries a user installs and a CJK font runs `smoke` |
| `win-x64` | ssh host `windows` (`--windows-host`, `--windows-dir`, `--windows-dotnet`) | the repository and a Windows `filee-build.exe` are copied over, then `fetch`, `package`, `smoke` run there |

An x64 host cross-builds `linux-arm64` (rhwp with `gcc-aarch64-linux-gnu`); everything else is the same commit,
toolchain and flags as the native build.

`--publish` refuses to run with uncommitted changes, unpushed commits, or an existing tag, so the tag always names the
code that built the assets. It writes `SHA256SUMS` and `RELEASE_NOTES.md` (`filee-build release`, which fails if one
of the five archives is missing, an extra file is present, or the tag is not `v<upstream version>-<revision>`),
creates the tag, pushes it, creates a **draft** release with the five archives and `SHA256SUMS`, then publishes it.

## GitHub Actions (for when they can run)

| Workflow | Trigger | Does |
|---|---|---|
| `ci.yml` → `build.yml` | manual (`gh workflow run ci.yml`) | builds the five packages with `filee-build`, smoke-tests each (Linux inside a clean `ubuntu:22.04` container), checks glibc, uploads artifacts |
| `release.yml` | manual on a tag (`gh workflow run release.yml --ref v1.12.0-1`) | the same build, then, only if all five pass: `SHA256SUMS`, release notes, provenance attestation (public repositories), GitHub release |

Actions are pinned to commit SHAs (comments name the version). To switch CI on, change `on:` in `ci.yml` to
`push`/`pull_request`.

### Credentials: GitHub's OIDC token, no stored secrets

The release job uses only the workflow's short-lived `GITHUB_TOKEN` (`contents: write`, this job only) and the job's
OIDC token (`id-token: write`), which Sigstore exchanges for a signing certificate when the build-provenance
attestation is created. There is **no** personal access token, signing key, cloud credential or API key in the repository
or its secrets.

- **Attestation**: `actions/attest` signs a SLSA provenance statement for `SHA256SUMS` (hence every asset). GitHub supports it for **public** repositories
  (private ones need Enterprise Cloud); the step runs only while the repository is public
  (`github.event.repository.private == false`). Verify with
  `gh attestation verify <asset> --repo NomaDamas/filee-cli`. Local releases carry no attestation.
- **macOS signing**: ad-hoc (`codesign -s -`) like upstream's packages. Developer ID signing and notarization would need
  an Apple certificate in GitHub secrets (never in the repository): pass the identity with
  `filee-build package --codesign-identity` and add `xcrun notarytool`. Not done.
- **Consuming a release**: download the assets by tag, compare the SHA-256 with `SHA256SUMS` and, for releases built by
  `release.yml`, run `gh attestation verify`. The Homebrew tap and Scoop bucket (below) do the first part by themselves.

### Runners

`ubuntu-22.04` (Linux x64), `ubuntu-22.04-arm` (Linux arm64, free for public repositories only), `macos-15`,
`macos-15-intel`, `windows-2022`. A private repository cross-builds linux-arm64 on x64 instead.
