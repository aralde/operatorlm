# winget packaging

`0.2.4/` holds the manifests for the first submission of `aralde.OperatorLM`
(portable install of the release `.exe`, command alias `operatorlm`).

## First submission (manual, once)

1. Fork `microsoft/winget-pkgs`.
2. Copy `0.2.4/` to `manifests/a/aralde/OperatorLM/0.2.4/` in the fork.
3. Open a PR; the bot validates the URL, SHA256 and runs a Defender scan.
   The binary is unsigned, so expect a possible SmartScreen/false-positive
   round of review.
4. Once merged, `winget install aralde.OperatorLM` works.

## Automatic updates

After the first merge, set in the GitHub repo:

- secret `WINGET_TOKEN`: classic PAT with `public_repo` scope (the default
  `GITHUB_TOKEN` cannot open PRs against another repository);
- variable `WINGET_ENABLED=true`.

The `winget` job in `.github/workflows/release.yml` then opens the bump PR for
every `v*` tag, using the `OperatorLM-windows-amd64.exe` asset.

## Notes

- Self-update from the tray rewrites the `.exe` in place, so winget will see a
  hash different from the installed one. `winget upgrade` still works; a
  `winget uninstall` of a modified portable needs `--force`.
- Only after the package is live should the admin directory's "winget" field
  be filled in (the form validates the ID against the catalogue).
