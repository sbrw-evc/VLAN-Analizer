# Acceptance and diagnostics

## Automated checks

CI runs `go vet ./...`, formatting checks, JavaScript syntax checks and `go test -race ./...`. Unit and socket tests cover bounded scheduling, configuration snapshots, freshness, credentials, authorization, source imports, invalid-interface failures, HTTP/CONNECT proxying and tunnel shutdown.

Linux integration checks use a real production image with Chromium and a disposable MariaDB instance. The import user has only column-level SELECT privileges, and the test verifies that writes fail.

The fresh-install check copies only `compose.yaml` into a clean directory, overrides the image with the just-built CI image and starts it without `.env` or host preparation files. Playwright completes the actual browser wizard using managed services, signs in, changes monitoring settings, configures Gatus/Kuma, imports sites, adds a manual site and verifies inventory. Restart checks exercise persistent credentials/settings, a closed setup wizard and automatic OpenBao unsealing.

## Host acceptance

On the target Linux host, compare the Networks page with the host's configured VLAN interfaces and routes. Add a controlled website reachable from one selected VLAN and blocked from another. Confirm matrix differences and inspect DNS, TCP, TLS, HTTP, browser resource failures and traceroute details. Interface isolation depends on the host's routing/firewall configuration, which cannot be reproduced by a generic GitHub runner.

Disable an imported monitor in Kuma and synchronize to confirm source pause handling. Temporarily block the ClickHouse connection and confirm monitoring errors are visible. Let a measurement exceed the configured freshness threshold and confirm it becomes unknown instead of staying green.

## Optional diagnostics

Installation does not require diagnostic commands. If a service cannot start, `docker compose ps` and `docker compose logs --tail=100` provide lifecycle information. `/healthz` reports that HTTP is running; `/readyz` requires completed setup, reachable ClickHouse and a successful monitoring cycle. Before the wizard completes, the application container can report unhealthy while the setup page remains available.

Application port `8000` must be free. Management services publish no host ports; the helper refreshes their internal bridge address map automatically. Preserve named volumes when restarting. If OpenBao cannot unseal, verify that the original bootstrap and OpenBao volumes belong to the same deployment. Do not delete one volume as a troubleshooting shortcut.
