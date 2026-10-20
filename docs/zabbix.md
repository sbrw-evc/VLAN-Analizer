# Zabbix integration

The application is polled by Zabbix **HTTP agent**. It does not implement the Zabbix JSON-RPC API or push data using `zabbix_sender`.

## Endpoints

Both endpoints require `Authorization: Bearer <zabbix_token>`. Obtain the token from OpenBao KV v2 at `secret/vlan-analizer/app`; use a Zabbix secret-text macro `{$VLAN_ANALIZER.TOKEN}`. Do not place it in URLs or dashboard source.

- `GET /api/integrations/zabbix`: current site/interface metrics.
- `GET /api/integrations/zabbix/discovery`: LLD array with `{#SITE_ID}`, `{#SITE}`, `{#INTERFACE}`, `{#VLAN}`.

Set `{$VLAN_ANALIZER.URL}` to the HTTPS origin reachable by your Zabbix server/proxy. Enable certificate and hostname validation.

## Master HTTP item

Create an item on the monitoring host:

| Setting | Value |
|---|---|
| Name | VLAN Analizer snapshot |
| Type | HTTP agent |
| Key | `vlan.snapshot` |
| URL | `{$VLAN_ANALIZER.URL}/api/integrations/zabbix` |
| Request | GET |
| Header | `Authorization: Bearer {$VLAN_ANALIZER.TOKEN}` |
| Type of information | Text |
| Update interval | `60s` (adjust to scan cadence) |
| Required status codes | `200` |

Example response (synthetic):

```json
{"generated_at":"2026-10-20T10:00:00Z","items":[
  {"site_id":"abc-123","site":"Portal","interface":"eno1.20","vlan_id":20,
   "status":"up","available":1,"at":"2026-10-20T09:59:00Z",
   "dns_ms":2.1,"tcp_ms":4.2,"http_code":200,"http_ms":85.0,"page_ms":140.0,"tls_days":45}
]}
```

## Discovery and dependent items

Create an HTTP agent discovery rule with key `vlan.discovery`, URL `{$VLAN_ANALIZER.URL}/api/integrations/zabbix/discovery`, the same header, and a 5-minute interval. It returns a modern LLD array, without a legacy `data` wrapper.

Create dependent item prototypes using `vlan.snapshot` as master:

| Key | Information type | JSONPath preprocessing |
|---|---|---|
| `vlan.available["{#SITE_ID}","{#INTERFACE}"]` | Numeric (float) | `$.items[?(@.site_id == "{#SITE_ID}" && @.interface == "{#INTERFACE}")].available.first()` |
| `vlan.http.code["{#SITE_ID}","{#INTERFACE}"]` | Numeric (unsigned) | `$.items[?(@.site_id == "{#SITE_ID}" && @.interface == "{#INTERFACE}")].http_code.first()` |
| `vlan.http.ms["{#SITE_ID}","{#INTERFACE}"]` | Numeric (float), ms | `$.items[?(@.site_id == "{#SITE_ID}" && @.interface == "{#INTERFACE}")].http_ms.first()` |
| `vlan.dns.ms["{#SITE_ID}","{#INTERFACE}"]` | Numeric (float), ms | `$.items[?(@.site_id == "{#SITE_ID}" && @.interface == "{#INTERFACE}")].dns_ms.first()` |
| `vlan.tcp.ms["{#SITE_ID}","{#INTERFACE}"]` | Numeric (float), ms | `$.items[?(@.site_id == "{#SITE_ID}" && @.interface == "{#INTERFACE}")].tcp_ms.first()` |
| `vlan.page.ms["{#SITE_ID}","{#INTERFACE}"]` | Numeric (float), ms | `$.items[?(@.site_id == "{#SITE_ID}" && @.interface == "{#INTERFACE}")].page_ms.first()` |
| `vlan.tls.days["{#SITE_ID}","{#INTERFACE}"]` | Numeric (float), days | `$.items[?(@.site_id == "{#SITE_ID}" && @.interface == "{#INTERFACE}")].tls_days.first()` |

Use signed numeric (float) storage for availability and certificate days: unknown is `-1`, expired certificates can have negative days. Add a value map: `1=Healthy`, `0=Down`, `2=Degraded`, `-1=Unknown`.

For optional metrics, add JavaScript preprocessing **after JSONPath**:

```javascript
if (value === 'null' || value === '' || value === null) {
    throw 'Metric unavailable';
}
return Number(value);
```

Enable **Custom on fail → Discard value**. Never replace missing timings with zero. Retain `available=-1` for missing/stale availability so that loss of monitoring is visible. When `status=unknown` old timing fields may still be present for diagnostics; use availability/freshness to qualify them, not latency alone.

Prototype names can include `{#SITE} / {#INTERFACE}`. Site IDs and discovered interface names are internal generated/kernel values; site names are display labels and must not be interpolated into JSONPath filters.

## Suggested triggers

Replace `VLAN monitor` below with your exact Zabbix host name. Choose a duration at least as long as a complete application scan cycle.

```text
max(/VLAN monitor/vlan.available["{#SITE_ID}","{#INTERFACE}"],5m)=0
```

Down for the entire five-minute window. Separate triggers should cover `last(...)=2` (degraded), `last(...)=-1` (unknown), and `nodata(/VLAN monitor/vlan.snapshot,5m)=1` (application/integration unreachable).

For certificate expiry, use `last(...tls.days...)<14` together with a freshness/availability condition, and allow negative values. Set a lifetime for lost discovery resources so removed sites/interfaces do not leave permanent alerts. Notification channels remain configured in Zabbix.
