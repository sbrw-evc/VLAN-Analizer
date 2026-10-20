# Zabbix integration

Configure a Zabbix HTTP agent with:

- Snapshot URL: `https://monitor.example.com/api/integrations/zabbix`
- Discovery URL: `https://monitor.example.com/api/integrations/zabbix/discovery`
- Header: `Authorization: Bearer <integration-token>`

The generated token is the `zabbix_token` field at OpenBao KV v2 path `secret/vlan-analizer/app`. A secrets operator can retrieve it through their OpenBao tooling. It is intentionally not returned by the general application settings API. This credential retrieval is only required when configuring Zabbix, not during application installation.

Discovery returns one record per active site/probeable interface pair with `{#SITE_ID}`, `{#SITE}`, `{#INTERFACE}` and `{#VLAN}` macros. Untagged interfaces use `untagged-or-unknown` rather than an inferred VLAN number.

The snapshot contains `generated_at` and an `items` array. Each item contains site ID/name, interface, VLAN ID, status, availability, measurement timestamp and nullable metrics: `dns_ms`, `tcp_ms`, `http_code`, `http_ms`, `page_ms`, `tls_days`.

| Value | State |
| --- | --- |
| `1` | Healthy |
| `0` | Down |
| `2` | Degraded page loading |
| `-1` | No fresh measurement |

Use a dependent item JSONPath matching both site ID and interface. Alert on unknown as a separate state; missing timings are JSON null rather than zero. Disabled sites and interfaces excluded from probing do not appear in discovery. Requests require a token even when a browser session exists.
