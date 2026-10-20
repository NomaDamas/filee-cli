# DRAFT, not posted: "Offer CLI-only release assets" (KnifeLemon/Filee)

**Title:** CLI-only release assets (no GUI) for use from other programs

Hi, and thank you for Filee.

`filee-cli` works without the GUI (I checked on macOS arm64 and Windows 11: `filee-cli convert ... --json`, exit
codes 0/1/2/3). Programs that call Filee as a separate process (we use it from an AI agent's file-conversion tool and
from a desktop app) currently have to download the whole portable package or installer, which includes Avalonia and the
tray app.

Would you consider publishing CLI-only archives next to the existing ones, e.g.
`Filee-<version>-cli-<rid>.tar.gz` / `.zip`, with only `filee-cli`, its runtime and native libraries, `engines/rhwp`,
`engines/7zip`, `LICENSE` and `THIRD-PARTY-NOTICES.md`? Roughly: `dotnet publish src/Filee.Cli -r <rid>
--self-contained -p:FileeRelease=true -p:PublishReadyToRun=false` plus the two bundled engines, as in
`build/build-portable.ps1`.

Until then we publish an **unofficial** build at https://github.com/NomaDamas/filee-cli (built from your tag
without modifications, notices generated from the package contents, clearly labelled as unofficial). If you would
rather we not do this, or want a different name or wording, or have a trademark policy for the Filee name, please say so and
we will change it. We would be glad to retire our build once there is an official CLI-only asset.

Questions: is naming the executable `filee-cli` and mentioning "Filee" in the description fine with you?
