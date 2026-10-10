# Releasing

Versions follow semver with a `v` prefix. While pre-1.0, minor bumps may break config.
Pre-releases: `vX.Y.Z-beta.N`. The [GitHub Releases](https://github.com/chenhg5/bot-connect/releases)
page is the source of truth.

## Checklist

1. **Changelog.** Write `changelogs/<version>.md`: a one-paragraph summary, highlights, breaking changes
   (config keys renamed/removed — say how to migrate), known limitations.
2. **Version.** Set `VERSION := <version>` in `Makefile` and `"version"` in `npm/package.json`
   (without the `v`: `v0.1.0` → `0.1.0`).
3. **Gate.** `make check` (gofmt, vet, all tests) must pass, and CI must be green on `main`.
4. **Real-IM smoke test** (not automated): run the bot against a test Feishu app and check
   `/whoami` (role owner), one question, one delegated read-only task with its report, one group @.
5. **Commit and tag** the release commit:
   ```bash
   git commit -am "release: <version>"
   git tag -a <version> -m "<version>"
   git push origin main <version>
   ```
6. **Build from the tag** (binaries embed the tag's commit):
   ```bash
   make release-all          # dist/bot-connect-<version>-{darwin,linux}-{amd64,arm64}.tar.gz,
                             #      bot-connect-<version>-windows-{amd64,arm64}.zip, checksums.txt
   ```
7. **Publish:**
   ```bash
   gh release create <version> dist/*.tar.gz dist/*.zip dist/checksums.txt \
     --title <version> --notes-file changelogs/<version>.md [--prerelease]
   ```
   Use `--prerelease` for `-beta.N` and for every `v0.0.x` preview.
8. **npm** (after the GitHub release exists — the package downloads its assets):
   ```bash
   cd npm && npm publish                   # while there is no stable 1.x, previews go to `latest`
   # once a stable release exists: npm publish --tag beta for -beta.N, so `latest` stays stable
   ```
9. **Installers:** publishing the release triggers CI's `install-scripts` job (install.sh on Linux and
   macOS, install.ps1 on Windows, the npm package on all three) — it must be green.
10. **Verify:** `go install github.com/chenhg5/bot-connect/cmd/bot-connect@<version>` works and
   `bot-connect version --format table` prints it; `curl …/install.sh | sh`, `irm …/install.ps1 | iex` (Windows)
   and `npm i -g bot-connect` install it;
   a downloaded archive's checksum matches `checksums.txt`.
