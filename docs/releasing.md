# Releasing

Versions follow semver with a `v` prefix. While pre-1.0, minor bumps may break config.
Pre-releases: `vX.Y.Z-beta.N`. The [GitHub Releases](https://github.com/chenhg5/bot-connect/releases)
page is the source of truth.

## Checklist

1. **Changelog.** Write `changelogs/<version>.md`: a one-paragraph summary, highlights, breaking changes
   (config keys renamed/removed — say how to migrate), known limitations.
2. **Version.** Set `VERSION := <version>` in `Makefile`.
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
   make release-all          # dist/bot-connect-<version>-{darwin,linux}-{amd64,arm64}.tar.gz + checksums.txt
   ```
7. **Publish:**
   ```bash
   gh release create <version> dist/*.tar.gz dist/checksums.txt \
     --title <version> --notes-file changelogs/<version>.md [--prerelease]
   ```
   Use `--prerelease` for `-beta.N` and for every `v0.0.x` preview.
8. **Verify:** `go install github.com/chenhg5/bot-connect/cmd/bot-connect@<version>` works and
   `bot-connect version` prints it; a downloaded archive's checksum matches `checksums.txt`.
