# Versioning & Docker Build

## Project Overview
- Go service: Atlassian-Firebase auth bridge
- Docker image registry: GitHub Container Registry (GHCR)
- Image: `ghcr.io/jinnotgin/atlassian-firebase-auth-bridge`
- CI workflow: `.github/workflows/docker.yml`
- Multi-arch: linux/amd64 + linux/arm64

## How Versioning Works

The tag format is `v*.*.*` (e.g. `v0.4`, `v1.0.0`). The CI workflow triggers on git tags matching this pattern.

### To release a new version:
```bash
git tag v<VERSION>        # e.g. git tag v0.4
git push origin v<VERSION>
```

### What the CI produces per trigger:

| Trigger              | Docker tags created                          | Pushed? |
|----------------------|----------------------------------------------|---------|
| Git tag `v*.*.*`     | `:<tag>-amd64`, `:<tag>-arm64`, `:<tag>`     | Yes     |
| Push to `main`       | `:sha-<12chars>-amd64/arm64`, `:sha-…`, `:latest` | Yes |
| Pull request         | `:pr-<number>` (build only)                  | No      |

- Pushing to `main` also updates the `:latest` tag.
- Git tag pushes do **NOT** update `:latest`.

## Key Files
- `Dockerfile` — multi-stage distroless build, output binary is `/auth-bridge`
- `cmd/server/main.go` — entrypoint
- `internal/authbridge/handler.go` — core handler logic

## Notes
- The Go binary does **NOT** currently embed a version string. Version is only tracked via git tags and Docker image tags.
- The workflow uses GitHub Actions cache (`type=gha`) for Docker layer caching.
- The manifest job merges per-arch images into a single multi-arch manifest.
