"""Self-hosted Gatus: YAML is authoritative when the status API hides URLs."""
import hashlib
from urllib.parse import urlsplit

import httpx
import yaml


def validate_url(url):
    parsed = urlsplit(url)
    if parsed.scheme not in {"http", "https"} or not parsed.hostname or parsed.username or parsed.password:
        raise ValueError("Use an HTTP(S) URL without credentials")
    if parsed.port not in {None, 80, 443, 8080, 8443}:
        raise ValueError("Allowed web ports: 80, 443, 8080, 8443")
    if ":" in parsed.hostname:
        raise ValueError("IPv6 probes are not supported in v0.1")
    if len(url) > 2048:
        raise ValueError("URL is too long")
    return url


def parse_endpoints(data):
    if isinstance(data, dict):
        entries = data.get("endpoints", [])
    else:
        entries = data
    if not isinstance(entries, list):
        raise ValueError("Expected a Gatus endpoint list")
    if len(entries) > 1000:
        raise ValueError("Gatus import exceeds the limit of 1000 endpoints")
    sites, skipped = [], []
    for endpoint in entries:
        if not isinstance(endpoint, dict):
            raise ValueError("Invalid Gatus endpoint")
        url = endpoint.get("url")
        if not url:
            skipped.append(endpoint.get("name", "unnamed") + ": URL not exposed; mount Gatus YAML")
            continue
        try:
            validate_url(url)
        except ValueError as exc:
            skipped.append(f"{endpoint.get('name', url)}: {exc}")
            continue
        sites.append({"id": "gatus-" + hashlib.sha256(url.encode()).hexdigest()[:24],
                      "name": endpoint.get("name", url)[:120], "url": url,
                      "source": "gatus", "enabled": 1, "deleted": 0})
    return sites, skipped


async def fetch_gatus(settings, secrets):
    if settings.gatus_config:
        from pathlib import Path
        return parse_endpoints(yaml.safe_load(Path(settings.gatus_config).read_text()))
    if not settings.gatus_url:
        return [], []
    headers = {}
    if secrets.get("gatus_token"):
        headers["Authorization"] = "Bearer " + secrets["gatus_token"]
    async with httpx.AsyncClient(timeout=20, trust_env=False) as client:
        response = await client.get(settings.gatus_url.rstrip("/") + "/api/v1/endpoints/statuses", headers=headers)
        response.raise_for_status()
        return parse_endpoints(response.json())
