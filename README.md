# VLAN Analizer

VLAN Analizer monitors website reachability from each configured Linux host interface. Its dashboard shows a website × interface availability matrix, host VLAN inventory and detailed network diagnostics, and it creates the VLAN interfaces of a trunk port on Debian, Ubuntu, CentOS and RHEL hosts. The application runs in Docker and stores measurements in ClickHouse and credentials in OpenBao.

## Install

Requires a Linux Docker host with Docker Engine and the Docker Compose plugin. Host networking is required to observe the host's actual interfaces. Docker Desktop does not provide equivalent host VLAN access.

1. Download **[compose.yaml](https://github.com/sbrw-evc/VLAN-Analizer/releases/latest/download/compose.yaml)** into an empty directory.
2. Start the application:

   ```sh
   docker compose up -d
   ```

3. Open `http://<linux-host>:8000` and complete the setup wizard.

The single Compose file downloads the application from public GitHub Container Registry and starts persistent OpenBao and ClickHouse services and the host network agent that manages VLAN interfaces. Initialization, random database credentials, OpenBao provisioning and unsealing happen automatically. No `.env`, host scripts, registry login or secret retrieval commands are required.

The wizard sets the application URL, selects bundled or external OpenBao and ClickHouse, creates an administrator, and optionally connects Microsoft Entra ID. Complete the first installation on a trusted network: the first completed wizard claims the administrator account and closes setup permanently. Configure Gatus, Kuma MySQL/MariaDB, interfaces, DNS and scan parameters in **Administration** after signing in.

## Features

- Linux interface, 802.1Q VLAN, address, route and bridge VLAN inventory.
- Creation and management of 802.1Q VLAN interfaces on trunk ports, persisted through netplan (Ubuntu), NetworkManager (RHEL, CentOS), ifupdown (Debian) or network-scripts (CentOS 7); batches such as `20, 30-35`, DHCP or static IPv4, MTU and per-VLAN DNS. See [VLAN interfaces](docs/vlans.md).
- A web app in the design of the Beacon/Ledger/Keystone family: light and dark themes, English and Russian, animated controls, phone layout. See [design guidelines](docs/design.md).
- Source-bound IPv4 DNS, TCP, TLS, HTTP(S), Chromium page loading and UDP traceroute checks.
- HTTP status, first-byte and total response time; browser load/DOM timing and failed resources.
- Certificate subject, issuer, SANs, expiry, fingerprint, TLS version and cipher.
- Availability matrix, path diagnostics and up to 1,000 history samples per query.
- Manual websites, Gatus API/YAML imports and read-only Kuma MySQL/MariaDB imports.
- Local administrator/viewer accounts with Argon2id password hashes in OpenBao.
- Optional Entra ID with explicit `VLAN.Admin` / `VLAN.Viewer` application roles.
- Zabbix HTTP agent snapshot and low-level discovery endpoints.

The backend, bootstrap helper and network probes use Go. A bounded goroutine worker pool schedules site/interface pairs; independent probe stages run concurrently. Chromium runs through a local proxy whose outgoing sockets bind to the selected interface. Failed binding never falls back to the default route. Browser checks use Chromium DevTools directly from Go; Python is not part of the application image.

## Configuration

Operational settings are saved through the application UI, not environment variables:

| Location | Settings |
| --- | --- |
| First-run wizard | Application URL, OpenBao, ClickHouse, administrator, optional Entra ID |
| Administration → Monitoring | Interfaces, per-interface IPv4 DNS, interval, concurrency, timeout, freshness, browser checks |
| Administration → Connections | Gatus, Kuma MySQL/MariaDB, Entra ID |
| Administration → Users | Local account passwords, roles and disable state |
| Host network → VLAN interfaces | Trunk port, VLAN IDs, addressing, MTU, route metric, DNS and monitoring of each VLAN |

There is no required `.env`. The optional `.env.example` contains only `VLAN_ANALIZER_VERSION` to select an image tag. Release Compose assets already default to their release version. An empty interface selection automatically uses eligible host IPv4 interfaces, excluding Docker bridges and veth devices. You may explicitly select other eligible interfaces.

## Storage and operation

ClickHouse stores website definitions and time series: checks expire after 90 days and inventory after 30 days. OpenBao stores password hashes, signing/integration tokens and connection settings. Installation metadata and bootstrap AppRole credentials are persisted in Docker volumes with restricted file permissions.

The bundled OpenBao uses persistent file storage, not development mode. To support unattended startup, its unseal material is stored in the protected bootstrap volume. The initial root token is revoked after provisioning. This convenience mode does not separate unseal keys from the host; protect and back up all application, bootstrap, OpenBao and ClickHouse volumes together. The wizard also supports externally managed OpenBao for deployments requiring separate key custody.

The `netagent` service runs privileged with the host PID namespace and no network; the application reaches it only through a Unix socket in a shared volume. Remove the service to disable VLAN management.

Management services stay on an internal Docker bridge with no published ports. The bootstrap helper discovers their addresses; host-network application clients use that protected address map through the host’s bridge route. The application listens on port `8000`. It uses host networking and `NET_RAW`, with other application capabilities dropped. For HTTPS use a reverse proxy and enter the public HTTPS origin in the wizard; an example is in `deploy/nginx.conf.example`.

Update by downloading the new release Compose file into the existing deployment directory and running `docker compose up -d`. Keep the same directory/project name to reuse named volumes. Back up volumes before version upgrades. `docker compose down` preserves data; `docker compose down -v` deletes it.

Existing v0.2 installations can retain their original deployment files and application data volume when switching to the Go image: the application migrates `installation.json` / `approle.json`, keeps the existing external service endpoints and uses the existing ClickHouse schema. Moving an existing deployment to the new bundled Compose topology additionally requires preserving its original service data, ports and project volume names; do not overwrite a working deployment with empty volumes.

## Scope and limitations

Probes require already configured, routed host IPv4 interfaces. Administrators can create VLAN interfaces on a trunk port; otherwise the application uses the interfaces the host already has. It does not configure switches, scan switch trunks or infer an untagged VLAN number. IPv6 addresses are displayed as inventory only. Probe destinations and redirects support HTTP(S) on ports 80, 443, 8080 and 8443. Browser traffic uses the same port restrictions. A blocked resolver or route is reported as a failure on that interface.

`up` means HTTP 2xx/3xx and, when enabled, successful page loading. Failed page resources produce `degraded`; failed DNS/TCP/TLS/HTTP produces `down`. Missing or expired measurements appear as `unknown`. Traceroute can be filtered by a network without making HTTP unavailable. Measurements include client and container overhead; they are not a substitute for a synthetic user journey or a performance benchmark.

## Development and validation

```sh
go test -race ./...
go vet ./...
cd web && npm ci && npm test && npm run build   # React app embedded into the Go binary
npm run dev                                       # hot reload on :5173, API proxied to :8000
docker compose -f compose.yaml -f compose.dev.yaml up -d --build
```

GitHub Actions validates formatting, static analysis, the web app's types, translations and build, race tests, source-bound sockets, real Chromium, a read-only MariaDB importer, the complete browser wizard and persistence after an OpenBao/application restart. UI tests use Node.js and Playwright. Docker integration tests run on Linux GitHub runners.

Changing `VERSION` on `main` triggers validation, linux/amd64 GHCR image publication, generated release notes and standalone Compose release assets. See [release workflow](docs/releases.md).

- [Setup and security model](docs/setup.md)
- [VLAN interfaces on trunk ports](docs/vlans.md)
- [Design guidelines](docs/design.md)
- [Kuma database import](docs/kuma.md)
- [Architecture and design review](docs/architecture.md)
- [Zabbix integration](docs/zabbix.md)
- [Acceptance and diagnostics](docs/acceptance.md)
