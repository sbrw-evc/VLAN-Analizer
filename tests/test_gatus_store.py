import json
from types import SimpleNamespace
from unittest.mock import AsyncMock

import httpx
import pytest

from app.gatus import parse_endpoints, validate_url
from app.service import Service
from app.store import Store


def test_gatus_missing_urls_duplicates_and_invalid_protocols():
    sites, skipped = parse_endpoints([
        {"name": "Hidden"}, {"name": "Portal", "url": "https://portal.example"},
        {"name": "DB", "url": "tcp://db:5432"}])
    assert len(sites) == 1 and len(skipped) == 2
    assert sites[0]["source"] == "gatus"
    assert sites[0]["id"] == parse_endpoints({"endpoints": [{"url": "https://portal.example"}]})[0][0]["id"]


@pytest.mark.parametrize("url", ["file:///etc/passwd", "ftp://example.com", "https://u:p@example.com", "https://example.com:22", "https://[::1]"])
def test_reject_invalid_targets(url):
    with pytest.raises(ValueError):
        validate_url(url)


async def test_clickhouse_parameters_and_json_insert():
    seen = []
    def handle(request):
        seen.append(request)
        return httpx.Response(200, text='{"payload":"{\\"status\\":\\"up\\"}"}\n')
    store = Store(SimpleNamespace(clickhouse_url="http://ch", clickhouse_user="vlan"), "secret")
    await store.client.aclose()
    store.client = httpx.AsyncClient(base_url="http://ch", transport=httpx.MockTransport(handle))
    assert await store.history("' OR 1=1 --", "eth0", 24) == [{"status": "up"}]
    request = seen[-1]
    assert "' OR 1=1 --" not in request.content.decode()
    assert request.url.params["param_site"] == "' OR 1=1 --"
    await store.save_site({"id": "test", "name": "Русский сайт\nname", "url": "https://example.com", "source": "manual", "enabled": 1})
    lines = seen[-1].content.decode().splitlines()
    assert json.loads(lines[1])["name"] == "Русский сайт\nname"
    with pytest.raises(ValueError):
        await store.insert("sites; DROP TABLE checks", [])
    await store.close()


async def test_worker_persists_error_and_continues_other_paths(monkeypatch):
    settings = SimpleNamespace(include=set(), concurrency=2, timeout=5, gatus_url="", gatus_config="")
    store = SimpleNamespace(insert=AsyncMock(), sites=AsyncMock(return_value=[{"id": "a", "enabled": 1}, {"id": "b", "enabled": 1}]))
    inventory = {"interfaces": [{"interface": "eth0.20", "probeable": True}], "bridge_vlans": []}
    monkeypatch.setattr("app.service.discover", AsyncMock(return_value=inventory))
    async def probe(site, iface, settings):
        if site["id"] == "a":
            raise RuntimeError("probe failed")
        return {"at": "2026-01-01T00:00:00+00:00", "site_id": "b", "interface": "eth0.20", "status": "up"}
    monkeypatch.setattr("app.service.run_probe", probe)
    service = Service(store, settings, SimpleNamespace(secrets={}))
    await service.cycle()
    checks = [c.args[1][0] for c in store.insert.call_args_list if c.args[0] == "checks"]
    assert {c["status"] for c in checks} == {"error", "up"}
    assert not service.running and service.last_cycle
