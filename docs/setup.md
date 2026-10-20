# First-run setup

Download the release `compose.yaml`, run `docker compose up -d`, and open `http://<linux-host>:8000`. No `.env` or preparation commands are required. Complete setup on a trusted network because the first completed wizard creates the initial administrator.

## Wizard

1. **Application URL:** enter the exact browser origin, including port when applicable. This controls same-origin write checks, secure cookies and Entra redirect URLs.
2. **OpenBao:** keep the automatically managed service, or supply an external URL and either an operator token or an existing AppRole. Operator tokens only provision a scoped AppRole and are not persisted by the application. The selected mount must be KV v2.
3. **ClickHouse:** keep the automatically generated bundled connection, or enter an external HTTP(S) endpoint, database, user and password. The database must exist; the user must be able to create/alter and read/write application tables.
4. **Administrator:** create a local account with a password of at least 12 bytes. The application stores an Argon2id hash in OpenBao.
5. **Entra ID (optional):** supply tenant ID, application ID and client secret, or skip this step. Add the displayed web redirect URI to the Entra application and assign `VLAN.Admin` or `VLAN.Viewer` application roles. Discovery testing does not verify client credentials or role assignment; those are checked during sign-in.

Setup verifies the storage connections and writes installation metadata atomically. Once complete, the setup endpoints refuse further configuration. A failed partial installation can be retried using the same service settings and administrator credentials; existing secrets and accounts are not overwritten by a different installation.

## Settings after installation

Sign in and open **Administration**. **Monitoring** includes interface selection, IPv4 DNS overrides, interval, concurrent site/interface checks, timeout, stale threshold and browser probing. Leave interfaces unselected to use eligible interfaces automatically. Add a DNS override per interface with up to four IPv4 resolvers. DNS requests bind to that interface too; a loopback system resolver may be unreachable through a VLAN, in which case enter a reachable resolver.

**Connections** configures Gatus, Kuma MySQL/MariaDB and Entra ID. Test and save are separate actions. Empty password/token/secret fields preserve saved values, and read responses expose only credential-presence flags. These settings are stored in OpenBao. The initial OpenBao/ClickHouse endpoints are installation settings; changing the storage topology of an existing installation requires a planned data migration.

## Bundled service lifecycle

Compose starts a Go initialization job, persistent ClickHouse and OpenBao, a Go bootstrap helper, the privileged host network agent (`netagent`, see [VLAN interfaces](vlans.md)) and the application. Random credentials are generated once. The helper initializes OpenBao, stores recovery material in the bootstrap volume, creates a restricted AppRole, revokes its initial root token, and automatically unseals OpenBao after a restart. The application independently retries dependency activation during startup.

Protect the host and named volumes. The managed bootstrap volume contains unseal material and AppRole credentials, so it belongs in the same access-control and backup boundary as OpenBao. This is an unattended local deployment model, not external KMS auto-unseal. For independent key management, select external OpenBao in the wizard.

The application volume contains a restricted installation file and AppRole bootstrap credentials; ClickHouse passwords, user hashes, signing keys and integration credentials live in OpenBao. Back up the application, bootstrap, ClickHouse users/data and OpenBao volumes as a consistent deployment. Deleting only one storage volume can make the remaining installation unusable.

## External AppRole policy

`deploy/bao-policy.hcl` contains the minimum KV paths for an existing AppRole using the default `secret` mount. The application needs create/read on `vlan-analizer/app` and create/read/update on `users/*` and `connections/*`. Use an AppRole with persistent SecretID and renewable or reissuable tokens. External OpenBao must be initialized and unsealed by its operator; no CLI actions are necessary for the bundled installation.

Sessions use HttpOnly, SameSite cookies and a signing key stored in OpenBao. HTTPS origins enable Secure cookies. Local password/role updates revoke that account's existing sessions. Disabling Entra sign-in rejects existing Entra sessions; Entra role changes take effect on the next sign-in or session expiry.
