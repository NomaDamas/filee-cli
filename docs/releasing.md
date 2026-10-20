# Releasing and publishing

## What runs

| Workflow | Trigger | Does |
|---|---|---|
| `ci.yml` → `build.yml` | push to `main`, pull requests, manual | builds the five packages, runs the smoke test on each (Linux inside a clean `ubuntu:22.04` container), checks glibc ≤ 2.35, uploads artifacts |
| `release.yml` | push of a tag `v*` | the same build, then, only if all five pass: `SHA256SUMS`, release notes, provenance attestation (public repositories), GitHub release |

Actions are pinned to commit SHAs (comments name the version).

## Cutting a release

1. `UPSTREAM` has the right tag, commit and `revision`; CI on `main` is green.
2. `git tag v<upstream version>-<revision> && git push origin v<upstream version>-<revision>`, e.g. `v1.12.0-1`.
   `scripts/prepare_release.py` fails the release if the tag does not equal `v` + upstream version + `-` + revision,
   if the tag is not on `main`, or if one of the five assets is missing.
3. The release is created as a draft, then published once all assets are uploaded.

## Credentials: GitHub's OIDC token, no stored secrets

The release job uses only the workflow's short-lived `GITHUB_TOKEN` (`contents: write`, scoped to this job) and the
job's OIDC token (`id-token: write`), which Sigstore exchanges for a signing certificate when the build-provenance
attestation is created. There is **no** personal access token, signing key, cloud credential or API key in the
repository or in repository secrets. Forks and pull requests get a read-only token.

- **Attestation**: `actions/attest` signs a SLSA provenance statement for `SHA256SUMS` (hence every asset) with a
  Sigstore certificate that names this repository, workflow file and tag. GitHub supports it for **public**
  repositories on all plans; for a private repository it needs Enterprise Cloud. The step is therefore skipped while
  the repository is private and turns on by itself after the repository is made public (`github.event.repository.private`).
  Consumers verify with `gh attestation verify <asset> --repo NomaDamas/filee-cli`.
- **macOS signing**: packages are ad-hoc signed (`codesign -s -`) like upstream's. Developer ID signing and
  notarization would need an Apple certificate: add it as GitHub Actions *secrets* (never in the repository), pass the
  identity with `build_package.py --codesign-identity`, and add `xcrun notarytool` to `build.yml`. Not done.
- **Dazzi consumption**: `stage-tools.mjs` should download assets by tag from `NomaDamas/filee-cli`, compare the
  SHA-256 with the pinned value from `SHA256SUMS`, and (once public) may run `gh attestation verify`. For a private
  repository it needs a token that can read releases; keep it in the consumer's secrets.

## Runners

All build on GitHub-hosted runners: `ubuntu-22.04` (Linux x64, and Linux arm64 as a cross build while the
repository is private), `ubuntu-22.04-arm` (Linux arm64 once the repository is public: free for public repositories
only), `macos-15` (arm64), `macos-15-intel` (x64), `windows-2022`.

Linux arm64 on a private repository: rhwp is cross-compiled with `gcc-aarch64-linux-gnu` from the same pinned
commit, toolchain and flags (plus `--target`), `dotnet publish -r linux-arm64` is a cross publish, and the smoke test
runs in an arm64 `ubuntu:22.04` container under QEMU.
