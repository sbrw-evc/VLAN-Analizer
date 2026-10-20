"""Real browser and database checks. Explicitly enabled in CI / a Linux test host."""
import asyncio
import os
from datetime import datetime, timezone
from types import SimpleNamespace
from uuid import uuid4

import httpx
import pytest

from app.auth import Bao
from app.probes import run_probe
from app.store import Store


@pytest.mark.skipif(not os.getenv("RUN_BROWSER_TESTS"), reason="Enable RUN_BROWSER_TESTS with Chromium installed")
async def test_real_browser_page_and_failed_resources():
    requests = []
    async def website(reader, writer):
        request = (await reader.readuntil(b"\r\n\r\n")).decode()
        path = request.split(" ")[1]
        requests.append(path)
        if path == "/":
            status, body, content_type = "200 OK", b'<html><body><h1>Portal</h1><img src="/missing.png"></body></html>', "text/html"
        else:
            status, body, content_type = "404 Not Found", b"missing", "text/plain"
        writer.write((f"HTTP/1.1 {status}\r\nContent-Length: {len(body)}\r\nContent-Type: {content_type}\r\nConnection: close\r\n\r\n").encode() + body)
        await writer.drain()
        writer.close()
    server = await asyncio.start_server(website, "127.0.0.1", 8080)
    try:
        result = await run_probe({"id": "browser-test", "url": "http://127.0.0.1:8080/"},
            {"interface": "lo", "addresses": ["127.0.0.1/8"], "vlan_id": None},
            SimpleNamespace(dns_servers={"lo": []}, timeout=10, browser=True))
        assert result["http"]["code"] == 200
        assert result["tcp_ms"] >= 0
        assert result["status"] == "degraded"
        assert result["browser"]["status"] == 200
        assert result["browser"]["bad_responses"][0]["status"] == 404
        assert "/missing.png" in requests
    finally:
        server.close()
        await server.wait_closed()


@pytest.mark.skipif(not os.getenv("RUN_STORAGE_TESTS"), reason="Enable RUN_STORAGE_TESTS with ClickHouse and OpenBao test instances")
async def test_real_storage_clickhouse_schema_versions_history_and_openbao_approle(tmp_path, monkeypatch):
    settings = SimpleNamespace(clickhouse_url="http://127.0.0.1:8123", clickhouse_user="vlan",
                               bao_url="http://127.0.0.1:8200", bao_mount="secret")
    store = Store(settings, "integration-test-password")
    try:
        await store.init()
        site_id = "integration-" + str(uuid4())
        site = {"id": site_id, "name": "Portal", "url": "https://example.com", "source": "manual", "enabled": 1}
        await store.save_site(site)
        await store.save_site({**site, "enabled": 0})
        assert next(s for s in await store.sites() if s["id"] == site_id)["enabled"] == 0
        import json
        at = datetime.now(timezone.utc).isoformat(timespec="milliseconds")
        result = {"at": at, "site_id": site_id, "interface": "vlan20", "status": "up"}
        await store.insert("checks", [{**result, "payload": json.dumps(result)}])
        assert await store.history(site_id, "vlan20", 24) == [result]
        assert result in await store.latest()
        await store.save_site({**site, "source_enabled": 0})
        assert next(s for s in await store.sites() if s["id"] == site_id)["source_enabled"] == 0
        await store.save_site({**site, "deleted": 1})
        assert not any(s["id"] == site_id for s in await store.sites())
    finally:
        await store.close()
    async with httpx.AsyncClient(base_url=settings.bao_url, headers={"X-Vault-Token": "integration-test-token"}) as root:
        async def post(path, data):
            response = await root.post("/v1/" + path, json=data)
            response.raise_for_status()
            return response.json() if response.content else {}
        await post("sys/auth/approle", {"type": "approle"})
        await post("sys/policies/acl/vlan-test", {"policy": 'path "secret/data/vlan-analizer/*" { capabilities = ["create", "read", "update"] }'})
        await post("auth/approle/role/vlan-test", {"token_policies": ["vlan-test"], "token_ttl": "60s"})
        role = await root.get("/v1/auth/approle/role/vlan-test/role-id")
        role.raise_for_status()
        secret = await post("auth/approle/role/vlan-test/secret-id", {})
        for name, value in [("BAO_ROLE_ID", role.json()["data"]["role_id"]), ("BAO_SECRET_ID", secret["data"]["secret_id"])]:
            path = tmp_path / name
            path.write_text(value)
            monkeypatch.setenv(name + "_FILE", str(path))
    bao = Bao(settings)
    try:
        await bao.write("users/integration", {"password_hash": "test-hash", "role": "viewer"})
        assert (await bao.read("users/integration"))["role"] == "viewer"
        assert await bao.read("nonexistent") is None
    finally:
        await bao.client.aclose()


@pytest.mark.skipif(not os.getenv("RUN_KUMA_TESTS"), reason="Enable RUN_KUMA_TESTS with disposable MariaDB")
async def test_real_kuma_mysql_readonly_import():
    import pymysql
    from app.config import Settings
    from app.kuma import fetch_kuma
    root = pymysql.connect(host="127.0.0.1", port=3307, user="root", password="integration-root-password",
                           database="kuma", autocommit=True, ssl_disabled=True)
    try:
        with root.cursor() as cursor:
            cursor.execute("CREATE TABLE monitor (id BIGINT PRIMARY KEY, name VARCHAR(255), url TEXT, "
                           "type VARCHAR(40), active TINYINT, parent BIGINT NULL) CHARACTER SET utf8mb4")
            cursor.executemany("INSERT INTO monitor VALUES (%s, %s, %s, %s, %s, %s)", [
                (1, "Paused group", None, "group", 0, None),
                (2, "Русский портал", "https://portal.example", "http", 1, 1),
                (3, "Active site", "https://active.example", "keyword", 1, None),
                (4, "Ping", None, "ping", 1, None)])
            cursor.execute("REVOKE ALL PRIVILEGES ON kuma.* FROM 'vlan_reader'@'%'")
            cursor.execute("GRANT SELECT (id, name, url, type, active, parent) ON kuma.monitor TO 'vlan_reader'@'%'")
    finally:
        root.close()
    sites, warnings = await fetch_kuma(Settings(kuma_host="127.0.0.1", kuma_port=3307, kuma_tls=False),
                                       {"kuma_mysql_password": "integration-kuma-password"})
    assert not warnings and len(sites) == 2
    assert sites[0]["name"] == "Русский портал" and sites[0]["source_enabled"] == 0
    assert sites[1]["source_enabled"] == 1
    reader = pymysql.connect(host="127.0.0.1", port=3307, user="vlan_reader",
                             password="integration-kuma-password", database="kuma", ssl_disabled=True)
    try:
        with reader.cursor() as cursor:
            with pytest.raises(pymysql.err.OperationalError):
                cursor.execute("DELETE FROM monitor WHERE id = 4")
    finally:
        reader.close()
