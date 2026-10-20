# Architecture and design review

The backend, probes and host network agent are Go. The web app is React + Vite in `web/`, built in its own Docker stage and embedded into the Go binary; Node is not part of the runtime image. Chromium remains a separate browser process.

## Boundaries

| Responsibility | Implementation | Boundary |
| --- | --- | --- |
| HTTP, authorization and DTO validation | `http.go`, `auth.go` | `http.Handler`, `SecretStore` |
| Installation and service lifecycle | `application.go`, `bundled.go`, `files.go` | Atomic installation journal, dependency activation |
| Sites, inventory and measurements | `storage.go` | `Repository` |
| Linux inventory and bound sockets | `network.go` | `InventoryReader` |
| Network diagnostics and browser | `probes.go`, `proxy.go` | `Prober`, `Browser` |
| External website sources | `importers.go` | `Importer` |
| Scheduling and dashboard snapshots | `worker.go` | Injected repository, inventory and probe interfaces |
| VLAN interface API | `vlans.go` | `HostNetwork` (agent client over a Unix socket) |
| Host network agent | `internal/hostnet` | `Host` (commands and files in the host namespaces), `Backend` per persistence format |
| Web app | `web/src` | REST API under `/api`, session cookie |

Go expresses object-oriented design through structs, methods and composition. Components depend on small interfaces at their consumption boundaries; inheritance and a class hierarchy are unnecessary. Interface adapters can be replaced by test doubles without emulating HTTP or SQL internals.

## SOLID and DRY review

- **Single responsibility:** HTTP handlers coordinate validation, authorization and service calls. SQL, KV, import parsing, browser automation and Linux socket behavior are separate adapters.
- **Open/closed:** another importer or probe adapter can implement an existing interface. Registering a new integration still requires explicit UI/API wiring; this is intentional rather than a claim of zero-change extensibility.
- **Liskov substitution:** adapters obey context cancellation and return errors rather than silently changing interfaces/routes. Tests substitute in-memory storage and deterministic probes using the same contracts.
- **Interface segregation:** browser, inventory, imports and secrets each expose only their respective operations. The repository groups the application's cohesive persistence operations.
- **Dependency inversion:** scheduling and authentication operate against interfaces. Installation is the composition boundary that constructs concrete HTTP/ClickHouse/OpenBao adapters.
- **DRY:** shared URL validation, bound dialing, JSON requests, atomic persistence, session parsing and source reconciliation avoid duplicate policy logic. Domain-specific error handling remains explicit.

This review describes concrete design decisions, not formal proof of adherence. The HTTP router and composition root still know the supported integrations; adding many new integrations would justify a registry rather than expanding their switches.

## Concurrency and lifetime

A bounded worker pool schedules one job per active website/interface pair. The configured limit is 1–32 jobs, default 3. Within each job TCP/TLS, HTTP, browser navigation and traceroute run concurrently after DNS. Each check reuses its DNS answers across these stages; the next check resolves afresh. Go schedules goroutines across available CPUs; there is no Python interpreter or GIL in this path. Browser processes consume substantially more memory than socket probes, so concurrency remains explicitly configurable.

Contexts bound network requests, subprocesses, browser navigation and shutdown. Long-lived monitoring uses the application process context, never the setup HTTP request context. A queued wake-up coalesces repeated scan requests, cycles do not overlap, import reconciliation is serialized, and measurements are written in batches of up to 64. Configuration snapshots are protected by a read/write mutex and replaced as whole values.

Bundled storage stays on an internal Docker bridge without published ports. The bootstrap helper resolves service IPs and atomically refreshes a protected address map. HTTP clients translate reserved managed aliases to those IPs through the host bridge route; missing mappings fail without an external DNS fallback. Container address changes are picked up on new connections.

The OpenBao token cache and OIDC provider cache have separate locks. Argon2 login work has a fixed capacity and login attempts are rate limited. CONNECT tunnels are tracked and closed when their per-check proxy shuts down. DNS and all outgoing probe sockets use the selected source/interface; an interface error is propagated rather than retried through the default route.

Concurrent code is checked with `go test -race`. Integration CI exercises real Chromium, MariaDB column-level SELECT access, bundled storage, browser setup and automatic unsealing after restart. No numeric performance gain is claimed without a representative deployment benchmark.
