# Linux host acceptance checklist

This checklist distinguishes unit tests from evidence that traffic really travels through each VLAN. It requires your configured Linux test host and authorized destinations.

1. Confirm host inventory with `ip -d address show`, `ip route show table all`, `ip rule show`, `bridge vlan show`. Match VLAN IDs, addresses, MAC/MTU and parent/master with the Networks page. An untagged NIC must show an unknown VLAN ID.
2. Restrict `INTERFACES` to intended L3 interfaces. Confirm that bridge slaves and addressless interfaces do not produce availability cells.
3. Verify a working gateway and source-policy route for **each** selected interface. If multiple default routes exist, test policy routing before accepting any result. The application never changes this state.
4. Capture traffic on one interface at a time, e.g. `sudo tcpdump -ni eno1.20 'port 53 or port 80 or port 443'`. Run a controlled site check. Confirm DNS, the HTML request, redirects and subordinate resource requests use the selected interface/source. Compare with the other VLAN. Do not rely solely on the dashboard to prove isolation.
5. For two test VLANs, permit a controlled site from one and block it from the other at the appropriate upstream firewall. Expect healthy/down accordingly. Repeat with DNS blocked and with a subresource blocked. A main page 200 plus a resource 404/timeout should be degraded.
6. Use a known valid HTTPS site, an expired leaf and a hostname-mismatched certificate. Expect TLS metadata for valid certificates and `valid=false` for invalid ones; no invalid site should be healthy. Check internal CA trust separately in Python, curl and Chromium.
7. Change a manual URL, pause it, resume it and delete it. Imported Gatus entries should retain pauses, deduplicate by URL and retire after removal from a fully readable source. Hidden API URLs must produce a warning; YAML import should recover them.
8. Restart the app. Sites and historical results must persist in ClickHouse. Interrupt a scan and wait beyond `STALE_AFTER`: old green measurements must become unknown. Restart ClickHouse/OpenBao and confirm failures appear without exposing secrets. OpenBao must be unsealed after its restart.
9. Confirm unauthenticated requests cannot read inventory/results. Test local viewer/admin. Change a local password and disable an account to confirm sessions are revoked. Test Entra roles and an unassigned tenant account. Test logout and CSRF rejection for another Origin.
10. Read both Zabbix endpoints with a valid integration token, then without/with an invalid token. Confirm unknown availability is -1, missing timings are null, and paused/deleted sites are excluded from LLD. Check Zabbix `nodata` alerts when the application is stopped.
11. Inspect 24-hour history and compare HTTP total, independent TCP/TLS and browser timings. They measure different requests and include different overhead; exact equality is not expected.
12. Observe RAM/CPU and scan duration for the expected site × interface count. Set concurrency and freshness accordingly. Confirm the host stays usable under repeated Chromium timeouts.

Current scope limits: IPv4 probes; leaf certificate metadata rather than complete certificate-chain analysis; initial browser load rather than authenticated user journeys; a single Linux host and application worker; single-node OpenBao/ClickHouse in the supplied Compose.
