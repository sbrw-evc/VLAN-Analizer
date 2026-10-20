import ipaddress
import json
import os
from dataclasses import dataclass, field
from pathlib import Path
from urllib.parse import urlsplit


def secret_file(name: str) -> str:
    path = os.environ.get(name + "_FILE")
    if not path:
        raise RuntimeError(f"{name}_FILE must point to a mounted secret")
    return Path(path).read_text().strip()


@dataclass
class Settings:
    clickhouse_url: str = field(default_factory=lambda: os.getenv("CLICKHOUSE_URL", "http://127.0.0.1:8123"))
    clickhouse_user: str = field(default_factory=lambda: os.getenv("CLICKHOUSE_USER", "vlan"))
    bao_url: str = field(default_factory=lambda: os.getenv("BAO_ADDR", "http://127.0.0.1:8200"))
    bao_mount: str = field(default_factory=lambda: os.getenv("BAO_KV_MOUNT", "secret"))
    public_url: str = field(default_factory=lambda: os.getenv("PUBLIC_URL", "https://vlan.example.com").rstrip("/"))
    tenant: str = field(default_factory=lambda: os.getenv("ENTRA_TENANT_ID", ""))
    client_id: str = field(default_factory=lambda: os.getenv("ENTRA_CLIENT_ID", ""))
    interval: int = field(default_factory=lambda: max(30, int(os.getenv("CHECK_INTERVAL", "120"))))
    concurrency: int = field(default_factory=lambda: max(1, int(os.getenv("CHECK_CONCURRENCY", "3"))))
    timeout: int = field(default_factory=lambda: min(120, max(5, int(os.getenv("CHECK_TIMEOUT", "20")))))
    stale_after: int = field(default_factory=lambda: max(60, int(os.getenv("STALE_AFTER", "600"))))
    include: set = field(default_factory=lambda: set(filter(None, (name.strip() for name in os.getenv("INTERFACES", "").split(",")))))
    dns_servers: dict = field(default_factory=lambda: json.loads(os.getenv("VLAN_DNS", "{}")))
    gatus_url: str = field(default_factory=lambda: os.getenv("GATUS_URL", ""))
    gatus_config: str = field(default_factory=lambda: os.getenv("GATUS_CONFIG", ""))
    kuma_host: str = field(default_factory=lambda: os.getenv("KUMA_MYSQL_HOST", ""))
    kuma_port: int = field(default_factory=lambda: int(os.getenv("KUMA_MYSQL_PORT", "3306")))
    kuma_database: str = field(default_factory=lambda: os.getenv("KUMA_MYSQL_DATABASE", "kuma"))
    kuma_user: str = field(default_factory=lambda: os.getenv("KUMA_MYSQL_USER", "vlan_reader"))
    kuma_tls: bool = field(default_factory=lambda: os.getenv("KUMA_MYSQL_TLS", "true").lower() == "true")
    kuma_ca: str = field(default_factory=lambda: os.getenv("KUMA_MYSQL_CA", ""))
    kuma_instance: str = field(default_factory=lambda: os.getenv("KUMA_INSTANCE_ID", "default"))
    browser: bool = field(default_factory=lambda: os.getenv("BROWSER_ENABLED", "true").lower() == "true")

    def __post_init__(self):
        if not 1 <= self.kuma_port <= 65535 or not self.kuma_instance.strip():
            raise ValueError("Kuma requires a valid TCP port and nonempty KUMA_INSTANCE_ID")
        origin = urlsplit(self.public_url)
        if (origin.scheme not in {"http", "https"} or not origin.hostname or origin.path
                or origin.query or origin.fragment or origin.username or origin.password):
            raise ValueError("PUBLIC_URL must be an HTTP(S) origin without path or credentials")
        if not isinstance(self.dns_servers, dict):
            raise ValueError("VLAN_DNS must be an interface-to-server-list JSON object")
        for interface, servers in self.dns_servers.items():
            if not isinstance(servers, list) or not servers:
                raise ValueError(f"VLAN_DNS[{interface}] must be a nonempty list of IPv4 addresses")
            for server in servers:
                ipaddress.IPv4Address(server)

    @property
    def secure_cookie(self):
        return urlsplit(self.public_url).scheme == "https"
