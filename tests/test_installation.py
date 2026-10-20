import asyncio
import json
from types import SimpleNamespace
from unittest.mock import AsyncMock

import httpx
import pytest
from fastapi import HTTPException
from fastapi.testclient import TestClient

from app.config import Settings
from app.connections import (EntraForm, GatusForm, KumaForm, legacy_connections,
                             merge_form, public_connections)
from app.installation import Installation, SetupForm, atomic_json
from app.main import app


@pytest.fixture
def installation(tmp_path, monkeypatch):
    monkeypatch.delenv("BAO_ROLE_ID_FILE", raising=False)
    monkeypatch.delenv("BAO_SECRET_ID_FILE", raising=False)
    previous = getattr(app.state, "installation", None)
    manager = Installation(app, tmp_path)
    app.state.installation = manager
    yield manager
    if previous is None:
        del app.state.installation
    else:
        app.state.installation = previous


def setup_form():
    return SetupForm(public_url="http://testserver", bao={"url": "http://bao", "mode": "approle",
        "role_id": "role", "secret_id": "secret"}, clickhouse={"url": "http://ch", "password": "db-secret"},
        admin_username="admin", admin_password="long-admin-password")


def setup_headers(manager):
    return {"Origin": "http://testserver", "X-Setup-Token": manager.token_path.read_text()}


def test_setup_key_permission_and_no_disclosure(installation):
    assert installation.token_path.stat().st_mode & 0o777 == 0o600
    client = TestClient(app)
    assert client.get("/api/setup/status").json()["required"] is True
    assert installation.token_path.read_text() not in client.get("/api/setup/status").text
    assert client.get("/api/me").status_code == 503
    body = setup_form().bao.model_dump()
    assert client.post("/api/setup/test/openbao", json=body).status_code == 403
    assert client.post("/api/setup/test/openbao", json=body, headers={"Origin": "http://testserver"}).status_code == 401
    headers = {**setup_headers(installation), "Origin": "http://evil.example"}
    assert client.post("/api/setup/test/openbao", json=body, headers=headers).status_code == 403


def test_connection_failure_does_not_echo_secrets(installation, monkeypatch):
    monkeypatch.setattr("app.main.test_bao", AsyncMock(side_effect=RuntimeError("secret detail")))
    client = TestClient(app)
    response = client.post("/api/setup/test/openbao", json=setup_form().bao.model_dump(), headers=setup_headers(installation))
    assert response.status_code == 502 and "secret detail" not in response.text
    assert not installation.initialized


async def test_complete_persists_only_bootstrap_and_closes_setup(installation, monkeypatch):
    memory = {}
    class FakeBao:
        def __init__(self, *a, **k):
            self.client = SimpleNamespace(aclose=AsyncMock())
        async def read(self, path):
            return memory.get(path)
        async def write(self, path, value, cas=None):
            assert cas != 0 or path not in memory
            memory[path] = value
    monkeypatch.setattr("app.installation.test_bao", AsyncMock())
    monkeypatch.setattr("app.installation.test_clickhouse", AsyncMock())
    monkeypatch.setattr("app.installation.Bao", FakeBao)
    monkeypatch.setattr("app.installation.Store", lambda *a: SimpleNamespace(init=AsyncMock(), close=AsyncMock()))
    installation.activate = AsyncMock()
    form = setup_form()
    assert (await installation.complete(form))["ok"]
    assert installation.initialized and not installation.token_path.exists()
    assert memory["users/admin"]["role"] == "admin"
    assert memory["users/admin"]["password_hash"].startswith("$argon2id$")
    assert memory["app"]["clickhouse_password"] == form.clickhouse.password
    text = installation.config_path.read_text()
    assert form.admin_password not in text and form.clickhouse.password not in text and '"secret_id"' not in text
    assert json.loads((installation.directory / "approle.json").read_text())["secret_id"] == "secret"
    with pytest.raises(HTTPException) as exc:
        await installation.complete(form)
    assert exc.value.status_code == 409
    assert TestClient(app).post("/api/setup/complete", json=form.model_dump(), headers={"Origin": "http://testserver"}).status_code == 409


async def test_existing_application_never_overwritten(installation, monkeypatch):
    write = AsyncMock()
    class FakeBao:
        def __init__(self, *a, **k):
            self.client = SimpleNamespace(aclose=AsyncMock())
            self.write = write
        async def read(self, path):
            return {"installation_id": "another-installation"}
    monkeypatch.setattr("app.installation.test_bao", AsyncMock())
    monkeypatch.setattr("app.installation.test_clickhouse", AsyncMock())
    monkeypatch.setattr("app.installation.Bao", FakeBao)
    monkeypatch.setattr("app.installation.Store", lambda *a: SimpleNamespace(init=AsyncMock(), close=AsyncMock()))
    with pytest.raises(HTTPException) as exc:
        await installation.complete(setup_form())
    assert exc.value.status_code == 409
    write.assert_not_awaited()
    assert not installation.config_path.exists()


def test_existing_unavailable_installation_never_reopens_setup(installation):
    installation.config = {"settings": {}}
    assert installation.status()["required"] is False
    with pytest.raises(HTTPException) as exc:
        installation.authorize_setup(SimpleNamespace(headers={}))
    assert exc.value.status_code == 409


def test_atomic_config_mode_and_restore(installation):
    atomic_json(installation.config_path, {"settings": {"public_url": "https://example.com"}})
    manager = Installation(app, installation.directory)
    assert manager.initialized and not manager.ready
    assert installation.config_path.stat().st_mode & 0o777 == 0o600


def test_public_settings_hide_credentials_and_blank_preserves():
    base = Settings()
    values = legacy_connections(base, {"kuma_mysql_password": "kuma-secret", "gatus_token": "gatus-secret", "entra_client_secret": "entra-secret"})
    visible = public_connections(values, base.public_url)
    text = json.dumps(visible)
    assert not any(s in text for s in ("kuma-secret", "gatus-secret", "entra-secret"))
    assert visible["kuma"]["password_set"] is True
    assert merge_form("kuma", KumaForm(), values["kuma"])["password"] == "kuma-secret"
    with pytest.raises(ValueError):
        merge_form("kuma", KumaForm(enabled=True, host="db"), {})
    with pytest.raises(ValueError):
        GatusForm(enabled=True, url="http://gatus", config_path="/config/gatus.yaml")
    with pytest.raises(ValueError):
        EntraForm(enabled=True, tenant="bad", client_id="bad")


async def test_save_applies_immediately_without_returning_secrets(installation):
    installation.base = Settings()
    installation.app_secrets = {"session_key": "x" * 64}
    installation.connections = legacy_connections(installation.base, {})
    installation.bao = SimpleNamespace(write=AsyncMock())
    app.state.service = SimpleNamespace(lock=asyncio.Lock(), last_sync={"kuma": 10}, wake=asyncio.Event())
    app.state.auth = SimpleNamespace()
    result = await installation.update_connection("kuma", KumaForm(enabled=True, host="db", password="hidden"))
    assert result["kuma"]["password_set"] and "password" not in result["kuma"]
    assert app.state.settings.kuma_host == "db" and app.state.auth.secrets["kuma_mysql_password"] == "hidden"
    assert app.state.service.wake.is_set() and "kuma" not in app.state.service.last_sync
    installation.bao.write.assert_awaited_once()


async def test_failed_save_leaves_running_settings_unchanged(installation):
    installation.app_secrets = {}
    installation.connections = legacy_connections(Settings(), {})
    installation.bao = SimpleNamespace(write=AsyncMock(side_effect=RuntimeError("unavailable")))
    app.state.service = SimpleNamespace(lock=asyncio.Lock())
    with pytest.raises(RuntimeError):
        await installation.update_connection("gatus", GatusForm(enabled=True, url="https://gatus"))
    assert installation.connections["gatus"]["enabled"] is False


async def test_test_connection_does_not_persist(installation, monkeypatch):
    installation.app_secrets = {}
    installation.connections = legacy_connections(Settings(), {})
    installation.bao = SimpleNamespace(write=AsyncMock())
    app.state.service = SimpleNamespace(lock=asyncio.Lock())
    probe = AsyncMock(return_value={"ok": True})
    monkeypatch.setattr("app.installation.test_connection", probe)
    result = await installation.update_connection("gatus", GatusForm(enabled=True, url="https://gatus"), test_only=True)
    assert result["ok"] and installation.connections["gatus"]["enabled"] is False
    installation.bao.write.assert_not_awaited()


async def test_bao_uses_cas_for_initial_creation(monkeypatch):
    from app.auth import Bao
    requests = []
    def handler(request):
        requests.append(request)
        return httpx.Response(200, json={"auth": {"client_token": "runtime", "lease_duration": 60}})
    bao = Bao(Settings(), "role", "secret")
    await bao.client.aclose()
    bao.client = httpx.AsyncClient(base_url="http://bao", transport=httpx.MockTransport(handler))
    await bao.write("app", {"key": "value"}, cas=0)
    assert json.loads(requests[-1].content)["options"]["cas"] == 0
    await bao.client.aclose()


def test_connection_settings_require_admin_and_origin(installation):
    from app.auth import Auth
    settings = Settings(public_url="http://testserver")
    installation.ready = True
    installation.connections = legacy_connections(settings, {"kuma_mysql_password": "never-return-me"})
    app.state.settings = settings
    app.state.auth = Auth(settings, SimpleNamespace(), {"session_key": "x" * 64})
    client = TestClient(app)
    assert client.get("/api/settings/connections").status_code == 401
    client.cookies.set("session", app.state.auth.sign({"sub": "viewer", "provider": "entra", "role": "viewer"}))
    assert client.get("/api/settings/connections").status_code == 403
    assert client.put("/api/settings/connections/gatus", json={}, headers={"Origin": "http://testserver"}).status_code == 403
    client.cookies.set("session", app.state.auth.sign({"sub": "admin", "provider": "entra", "role": "admin"}))
    result = client.get("/api/settings/connections")
    assert result.status_code == 200 and "never-return-me" not in result.text
    assert client.put("/api/settings/connections/gatus", json={}).status_code == 403
