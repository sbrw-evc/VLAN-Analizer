# Releases and Docker packages

`VERSION` is the canonical application version. Update it in a pull request and merge into `main` to trigger `.github/workflows/release.yml`. Tags and manual dispatch are also supported; their version must match `VERSION`, and manual releases must use current `main`. Existing published versions are never replaced.

The pipeline:

1. Validates version metadata and checks whether the release exists.
2. Runs the reusable CI workflow: Go formatting/vet/race tests, real Chromium, MariaDB read-only import, clean Compose installation, browser wizard and restart persistence.
3. Builds and publishes the `linux/amd64` image to GHCR. ARM hosts are not supported.
4. Publishes a GitHub Release with generated change notes, image digest, standalone `compose.yaml`, an optional documentation archive and `SHA256SUMS`.

Stable image tags include `vX.Y.Z`, `X.Y.Z`, `X.Y` and `latest`. Prerelease tags do not update `latest`. The release Compose asset defaults directly to its version; `.env` is optional and may contain only an image-version override.

The public package is `ghcr.io/sbrw-evc/vlan-analizer`. Package visibility must remain public so a fresh installation can pull anonymously. GitHub Actions uses the repository `GITHUB_TOKEN` with `contents:write` and `packages:write` only in the publish job. No personal access token is required by the workflow or the installer.

The runtime image contains a native Go binary with the embedded web app, Chromium, iproute2, traceroute, CA roots and tini. The React app is built by a Node stage of the Dockerfile; neither Node nor Python is part of the runtime image. Development UI tests use Node/Playwright outside the runtime image.

For local development, use `compose.dev.yaml` together with the standard Compose file. The production Compose file always pulls the published image and requires no source checkout or local build.
