# Uptime Kuma database import

Configure **Administration → Uptime Kuma · MySQL / MariaDB** after installation. Enter host, port, database, username, password and a stable instance ID. Connections are tested before saving only when you choose **Test connection**. Saved changes apply immediately; use **Sync Kuma** to import on demand. Enabled sources also synchronize approximately every five minutes between monitoring cycles.

The importer reads only `id`, `name`, `url`, `type`, `active` and `parent` from the `monitor` table, in a read-only transaction. It does not read notification credentials or change the source database. Use a dedicated account with only these privileges:

```sql
CREATE USER 'vlan_reader'@'monitor-host' IDENTIFIED BY 'replace-with-a-strong-password';
GRANT SELECT (id, name, url, type, active, parent)
ON kuma.monitor TO 'vlan_reader'@'monitor-host';
```

This SQL is for the operator of an existing Kuma database; it is not an installation step for VLAN Analizer. Kuma must use a compatible MySQL/MariaDB schema. SQLite is not supported by this integration.

Enable TLS to require certificate and hostname validation. An optional CA path refers to a file mounted inside the container. Without a custom CA the system trust store is used. Never use TLS-disabled connections across an untrusted network. The password is stored in OpenBao and never returned by the settings API; leaving its field blank preserves it.

Supported HTTP-oriented monitor types are `http`, `keyword`, `json-query` and `real-browser`. The application imports their destination URLs and performs its own checks; Kuma-specific assertions, request bodies, authentication and notification settings are not copied. Paused monitors or monitors beneath paused groups remain disabled at the source. A local enable switch cannot override a source pause.

Stable IDs combine the instance ID and Kuma monitor ID. Keep the instance ID unchanged for the same database. Repeated syncs preserve local enable switches. Deleted monitors are retired only after a complete import without parsing warnings; a failed import leaves existing sites intact. Invalid URLs and parent cycles are skipped with warnings. Imports are limited to 1,000 source monitors. Website URLs support HTTP(S) on ports 80, 443, 8080 and 8443.
