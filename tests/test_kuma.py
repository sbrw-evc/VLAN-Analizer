import ssl
from types import SimpleNamespace
from unittest.mock import AsyncMock, MagicMock

import pytest
from fastapi import HTTPException

from app.config import Settings
from app.kuma import MAX_MONITORS, QUERY, parse_monitors, read_monitors
from app.service import Service
from app.store import site_enabled


def monitor(id=1, **kwargs):
    return {"id": id, "name": "Portal", "url": "https://portal.example", "type": "http", "active": 1,
            "parent": None, **kwargs}


def test_web_types_stable_ids_and_no_collision():
    rows = [monitor(n, type=t) for n, t in enumerate(["http", "keyword", "json-query", "real-browser", "ping", "port", "group"], 1)]
    sites, warnings = parse_monitors(rows, "production")
    assert len(sites) == 4 and not warnings
    renamed = parse_monitors([monitor(name="Renamed", url="https://new.example")], "production")[0][0]
    assert renamed["id"] == sites[0]["id"]
    assert parse_monitors([monitor()], "other")[0][0]["id"] != renamed["id"]
    assert len({s["id"] for s in sites}) == 4  # duplicate URLs remain separate Kuma monitors
    assert all(s["source"] == "kuma" for s in sites)


def test_upstream_pause_parent_groups_and_invalid_hierarchy():
    sites, warnings = parse_monitors([monitor(1, type="group", active=0), monitor(2, parent=1),
        monitor(3, active=0), monitor(4, parent=5), monitor(5, parent=4), monitor(6, parent=99)], "test")
    assert [s["source_enabled"] for s in sites] == [0, 0]
    assert all(s["enabled"] == 1 and not site_enabled(s) for s in sites)
    assert len(warnings) == 3
    assert any("cyclic" in w for w in warnings)
    assert any("missing parent" in w for w in warnings)


def test_unsupported_urls_warn_without_leaking_credentials():
    sites, warnings = parse_monitors([monitor(url="https://account:secret@example.com"), monitor(2, url="")], "test")
    assert not sites and len(warnings) == 2
    assert "secret" not in " ".join(warnings) and "account" not in " ".join(warnings)
    with pytest.raises(ValueError, match="1000"):
        parse_monitors([monitor(i) for i in range(MAX_MONITORS+1)], "test")


@pytest.mark.parametrize("tls", [True, False])
def test_database_only_reads_bounded_projection_and_closes(monkeypatch, tls):
    settings = Settings(kuma_host="mysql.internal", kuma_tls=tls)
    connection = MagicMock()
    cursor = connection.cursor.return_value.__enter__.return_value
    cursor.fetchall.return_value = [monitor()]
    connect = MagicMock(return_value=connection)
    monkeypatch.setattr("app.kuma.pymysql.connect", connect)
    assert len(read_monitors(settings, {"kuma_mysql_password": "sensitive"})[0]) == 1
    kwargs = connect.call_args.kwargs
    assert kwargs["ssl_disabled"] is (not tls)
    assert kwargs["connect_timeout"] == kwargs["read_timeout"] == 10
    if tls:
        assert kwargs["ssl"].verify_mode == ssl.CERT_REQUIRED and kwargs["ssl"].check_hostname
    assert cursor.execute.call_args_list[0].args == ("START TRANSACTION READ ONLY",)
    assert cursor.execute.call_args_list[1].args == (QUERY, (MAX_MONITORS + 1,))
    assert "password" not in QUERY and "headers" not in QUERY
    connection.rollback.assert_called_once()
    connection.close.assert_called_once()


def test_database_failure_rolls_back_and_keeps_credentials_required(monkeypatch):
    settings = Settings(kuma_host="mysql.internal")
    connection = MagicMock()
    connection.cursor.return_value.__enter__.return_value.execute.side_effect = RuntimeError("schema error")
    monkeypatch.setattr("app.kuma.pymysql.connect", MagicMock(return_value=connection))
    with pytest.raises(ValueError, match="password is missing"):
        read_monitors(settings, {})
    with pytest.raises(RuntimeError):
        read_monitors(settings, {"kuma_mysql_password": "secret"})
    connection.rollback.assert_called_once()
    connection.close.assert_called_once()


async def test_reconcile_preserves_local_pause_and_separates_sources(monkeypatch):
    incoming = parse_monitors([monitor(), monitor(2, active=0)], "test")[0]
    existing = [{**incoming[0], "enabled": 0}, {**incoming[1], "source_enabled": 1},
                {**incoming[0], "id": "old-kuma"}, {**incoming[0], "id": "manual", "source": "manual"},
                {**incoming[0], "id": "gatus", "source": "gatus"}]
    store = SimpleNamespace(sites=AsyncMock(return_value=existing), save_site=AsyncMock())
    service = Service(store, Settings(kuma_host="mysql.internal"), SimpleNamespace(secrets={}))
    monkeypatch.setattr("app.service.fetch_kuma", AsyncMock(return_value=(incoming, [])))
    result = await service.sync("kuma")
    saved = {c.args[0]["id"]: c.args[0] for c in store.save_site.call_args_list}
    assert result["retired"] == 1
    assert saved["old-kuma"]["deleted"] == 1
    assert saved[incoming[1]["id"]]["source_enabled"] == 0
    assert incoming[0]["enabled"] == 0
    assert "manual" not in saved and "gatus" not in saved
    assert service.last_sync["kuma"] > 0


async def test_partial_or_failed_import_never_retires_existing_sites(monkeypatch):
    store = SimpleNamespace(sites=AsyncMock(return_value=[{"id": "old", "source": "kuma"}]), save_site=AsyncMock())
    service = Service(store, Settings(kuma_host="mysql.internal"), SimpleNamespace(secrets={}))
    monkeypatch.setattr("app.service.fetch_kuma", AsyncMock(return_value=([], ["Invalid monitor"])))
    result = await service.sync("kuma")
    assert result["retired"] == 0
    store.save_site.assert_not_called()
    monkeypatch.setattr("app.service.fetch_kuma", AsyncMock(side_effect=RuntimeError("connection failed")))
    with pytest.raises(RuntimeError):
        await service.sync("kuma")
    store.save_site.assert_not_called()


async def test_unconfigured_source_does_not_clear_imports():
    store = SimpleNamespace(sites=AsyncMock(), save_site=AsyncMock())
    service = Service(store, Settings(), SimpleNamespace(secrets={}))
    for source in ("gatus", "kuma"):
        with pytest.raises(HTTPException) as exc:
            await service.sync(source)
        assert exc.value.status_code == 503
    store.sites.assert_not_called()
