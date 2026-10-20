export type Role = 'admin' | 'viewer';

export interface Session {
  sub: string;
  name: string;
  provider: 'local' | 'entra';
  role: Role;
}

export interface SetupStatus {
  required: boolean;
  ready: boolean;
  error: string;
  entra_enabled: boolean;
  managed_available: boolean;
  version: string;
}

export interface Site {
  id: string;
  name: string;
  url: string;
  source: 'manual' | 'gatus' | 'kuma';
  enabled: number;
  source_enabled: number;
  deleted: number;
}

export interface HostInterface {
  interface: string;
  ifindex: number;
  kind: string;
  vlan_id: number | null;
  parent: string | number | null;
  master: string | number | null;
  mac: string;
  mtu: number;
  state: string;
  addresses: string[];
  ipv6: string[];
  routes: Record<string, unknown>[];
  eligible: boolean;
  probeable: boolean;
}

export type Status = 'up' | 'down' | 'degraded' | 'error' | 'unknown';

export interface CheckResult {
  at: string;
  site_id: string;
  interface: string;
  vlan_id: number | null;
  status: Status;
  measured_status?: Status;
  age_seconds?: number;
  dns?: { addresses: string[]; servers: string[]; ms: number };
  tcp_ms?: number;
  remote_ip?: string;
  tcp_error?: string;
  tls?: { valid: boolean; error?: string; subject?: string; issuer?: string; san?: string[]; not_after?: string; expires_days?: number; version?: string; cipher?: string; handshake_ms?: number; sha256?: string };
  http?: { code: number; url: string; ttfb_ms: number; total_ms: number; bytes: number; redirects: number };
  http_error?: string;
  browser?: { ok: boolean; error?: string; load_ms?: number; dom_ms?: number; elapsed_ms?: number; resources?: number; failed_requests?: { error: string }[]; bad_responses?: { url: string; status: number }[] };
  trace?: { output?: string; error?: string };
  errors?: { stage: string; message: string }[];
}

export interface Dashboard {
  sites: Site[];
  interfaces: HostInterface[];
  bridge_vlans: Record<string, unknown>[];
  inventory_at: string;
  results: CheckResult[];
  worker: { running: boolean; last_cycle: string; error: string; gatus_warnings: string[]; kuma_warnings: string[] | null };
}

export interface ScanConfig {
  interfaces: string[];
  dns_servers: Record<string, string[]>;
  interval: number;
  concurrency: number;
  timeout: number;
  stale_after: number;
  browser: boolean;
}

export interface Connections {
  gatus: { enabled: boolean; url: string; config_path: string; token_set: boolean };
  kuma: { enabled: boolean; host: string; port: number; database: string; user: string; tls: boolean; ca: string; instance: string; password_set: boolean };
  entra: { enabled: boolean; tenant: string; client_id: string; client_secret_set: boolean; redirect_uri: string };
}

export type VlanMode = 'dhcp' | 'static' | 'none';

export interface VlanSpec {
  name: string;
  parent: string;
  vlan: number;
  mode: VlanMode;
  address?: string;
  gateway?: string;
  mtu?: number;
  metric?: number;
}

export interface LiveLink {
  name: string;
  kind: string;
  parent?: string;
  vlan?: number;
  state: string;
  up: boolean;
  mtu: number;
  mac: string;
  addresses: string[];
}

export interface ManagedVlan extends VlanSpec {
  backend: string;
  created: string;
  present: boolean;
  live: LiveLink;
}

export interface Trunk extends LiveLink {
  vlans: LiveLink[];
}

export interface HostEnvironment {
  distro: { id: string; name: string; version: string; family: string };
  backend: string;
  persistent: boolean;
  supported: boolean;
  warnings: string[];
  error?: string;
}

export type VlanListing =
  | { available: false; detail: string }
  | { available: true; environment: HostEnvironment; interfaces: ManagedVlan[]; trunks: Trunk[]; monitoring: { interfaces: string[]; dns_servers: Record<string, string[]> } };
