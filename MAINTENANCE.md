# Maintenance Runbook

## Release Lifecycle
1. Changes are merged to `main`.
2. Maintainer creates and pushes a semantic version tag (`vX.Y.Z`).
3. GitHub Actions workflow at `.github/workflows/release.yml` builds artifacts and publishes a GitHub Release.
4. Installer script (`install.sh`) resolves `releases/latest` and installs the newest release.
5. Users install/update with:

```bash
curl -fsSL https://raw.githubusercontent.com/webforspeed/speedmux/main/install | bash
```

## Runbook: Normal Release
1. Update local branch:

```bash
git checkout main
git pull --ff-only origin main
```

2. Validate:

```bash
go test ./...
```

3. Tag and push:

```bash
git tag -a v0.1.0 -m "speedmux v0.1.0"
git push origin v0.1.0
```

4. Verify workflow and release assets in GitHub:
- `speedmux_<version>_linux_amd64.tar.gz`
- `speedmux_<version>_linux_arm64.tar.gz`
- `speedmux_<version>_darwin_amd64.tar.gz`
- `speedmux_<version>_darwin_arm64.tar.gz`
- `checksums.txt`

5. Smoke test install:

```bash
curl -fsSL https://raw.githubusercontent.com/webforspeed/speedmux/main/install | bash
```

## Runbook: Hotfix Release
1. Land fix on `main`.
2. Run tests:

```bash
go test ./...
```

3. Ship next patch tag:

```bash
git tag -a v0.1.1 -m "hotfix: <summary>"
git push origin v0.1.1
```

## Runbook: Rollback Strategy
Installer is latest-only, so rollback is done by publishing a new fixed version.

1. Revert bad change(s) on `main`.
2. Test:

```bash
go test ./...
```

3. Publish next patch version:

```bash
git tag -a v0.1.2 -m "rollback/fix after v0.1.1"
git push origin v0.1.2
```

## Runbook: Recurring Maintenance
Weekly:
- `go test ./...`
- `go list -m -u all` to check dependency updates

Monthly:
- Validate release workflow assumptions still match `install.sh` (artifact names and checksum file).
- Test install command in a clean shell/session.
