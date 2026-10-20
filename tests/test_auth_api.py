import time
from datetime import datetime, timedelta, timezone
from types import SimpleNamespace
from unittest.mock import AsyncMock

import httpx
import jwt
import pytest
from fastapi import HTTPException
from fastapi.testclient import TestClient

from app.auth import Auth, Bao, sanitized_user
from app.config import Settings
from app.main import app, fresh_rows, zabbix_payload


@pytest.fixture
def auth():
    settings = Settings(public_url="https://vlan.example.com", tenant="00000000-0000-0000-0000-000000000000", client_id="client")
    bao = SimpleNamespace(read=AsyncMock(), write=AsyncMock())
    return Auth(settings, bao, {"session_key": "x" * 64, "zabbix_token": "test-token", "entra_client_secret": "secret"})


def test_token_expiry_and_audience(auth):
    assert auth.decode(auth.sign({"sub": "admin"}))["sub"] == "admin"
    for token in [auth.sign({}, lifetime=-1), auth.sign({}, audience="vlan-oidc"), "bad"]:
        with pytest.raises(HTTPException) as e:
            auth.decode(token)
        assert e.value.status_code == 401


async def test_local_login_and_disable(auth):
    account = sanitized_user("engineer", "twelve-or-more-characters", "viewer")
    auth.bao.read.return_value = account
    token = await auth.local_login("engineer", "twelve-or-more-characters", "1.2.3.4")
    assert auth.decode(token)["role"] == "viewer"
    with pytest.raises(HTTPException) as e:
        await auth.local_login("engineer", "wrong", "1.2.3.4")
    assert e.value.status_code == 401
    account["disabled"] = True
    with pytest.raises(HTTPException):
        await auth.local_login("engineer", "twelve-or-more-characters", "1.2.3.4")


def test_rate_limit(auth):
    for _ in range(10):
        auth.rate_limit("address")
    with pytest.raises(HTTPException) as e:
        auth.rate_limit("address")
    assert e.value.status_code == 429


async def test_bao_reauthenticates_after_lease_and_uses_kv2(monkeypatch, auth):
    monkeypatch.setattr("app.auth.secret_file", lambda name: name + "-value")
    paths = []
    def handle(request):
        paths.append(request.url.path)
        if request.url.path.endswith("login"):
            return httpx.Response(200, json={"auth": {"client_token": "leased", "lease_duration": 60}})
        assert request.headers["X-Vault-Token"] == "leased"
        return httpx.Response(200, json={"data": {"data": {"value": 42}}})
    bao = Bao(auth.settings)
    await bao.client.aclose()
    bao.client = httpx.AsyncClient(base_url="http://bao", transport=httpx.MockTransport(handle))
    assert await bao.read("app") == {"value": 42}
    await bao.read("app")
    bao.expiry = 0
    await bao.read("app")
    assert paths.count("/v1/auth/approle/login") == 2
    assert "/v1/secret/data/vlan-analizer/app" in paths
    await bao.client.aclose()


def test_api_auth_roles_csrf_and_machine_token(auth):
    app.state.auth = auth
    app.state.settings = auth.settings
    app.state.service = SimpleNamespace(inventory={"interfaces": [], "bridge_vlans": []}, inventory_at=None,
        running=False, last_cycle=None, error=None, gatus_warnings=[], kuma_warnings=[], sync=AsyncMock(return_value={"imported": 1, "retired": 0, "warnings": []}), wake=SimpleNamespace(set=lambda: None))
    app.state.store = SimpleNamespace(sites=AsyncMock(return_value=[]), latest=AsyncMock(return_value=[]))
    client = TestClient(app)
    assert client.get("/api/dashboard").status_code == 401
    token = auth.sign({"sub": "oid", "provider": "entra", "role": "viewer"})
    client.cookies.set("session", token)
    assert client.get("/api/dashboard").status_code == 200
    assert client.post("/api/kuma/sync", headers={"Origin": auth.settings.public_url}).status_code == 403
    assert client.post("/api/checks/run").status_code == 403
    assert client.post("/api/checks/run", headers={"Origin": auth.settings.public_url}).status_code == 403
    client.cookies.set("session", auth.sign({"sub": "oid", "provider": "entra", "role": "admin"}))
    assert client.post("/api/checks/run", headers={"Origin": "https://evil.example"}).status_code == 403
    assert client.post("/api/checks/run", headers={"Origin": auth.settings.public_url}).status_code == 202
    assert client.post("/api/kuma/sync").status_code == 403
    assert client.post("/api/kuma/sync", headers={"Origin": auth.settings.public_url}).json()["imported"] == 1
    app.state.service.sync.assert_awaited_with("kuma")
    app.state.service.sync.side_effect = RuntimeError("secret database credentials")
    failed = client.post("/api/kuma/sync", headers={"Origin": auth.settings.public_url})
    assert failed.status_code == 502 and "secret database credentials" not in failed.text
    assert client.get("/api/integrations/zabbix").status_code == 401
    assert client.get("/api/integrations/zabbix", headers={"Authorization": "Bearer test-token"}).json()["items"] == []
    assert "frame-ancestors 'none'" in client.get("/").headers["content-security-policy"]
    assert client.get("/openapi.json").status_code == 404


async def test_local_session_revoked_after_user_change(auth):
    auth.bao.read.return_value = {"role": "admin", "version": 2}
    request = SimpleNamespace(cookies={"session": auth.sign({"sub": "admin", "provider": "local", "version": 1})})
    with pytest.raises(HTTPException) as e:
        await auth.user(request)
    assert e.value.status_code == 401


def test_stale_and_zabbix_never_report_old_green():
    old = (datetime.now(timezone.utc) - timedelta(hours=1)).isoformat()
    rows = fresh_rows([{"at": old, "site_id": "a", "interface": "eth0", "status": "up"}], 300)
    assert rows[0]["status"] == "unknown" and rows[0]["measured_status"] == "up"
    data = {"sites": [{"id": "a", "name": "Portal", "enabled": 1}, {"id": "off", "enabled": 0}],
            "interfaces": [{"interface": "eth0", "vlan_id": None, "probeable": True},
                           {"interface": "eth0.20", "vlan_id": 20, "probeable": True}], "results": rows}
    payload = zabbix_payload(data)
    assert len(payload) == 2
    assert all(r["available"] == -1 for r in payload)
    assert all(r["http_ms"] is None for r in payload)


def test_oidc_pkce_state_and_nonce(auth):
    from urllib.parse import parse_qs, urlsplit
    url, cookie = auth.entra_start()
    params = parse_qs(urlsplit(url).query)
    flow = auth.decode(cookie, "vlan-oidc")
    assert params["code_challenge_method"] == ["S256"]
    assert params["state"] == [flow["state"]] and params["nonce"] == [flow["nonce"]]


async def test_oidc_rejects_state_mismatch_before_token_exchange(auth):
    _, cookie = auth.entra_start()
    with pytest.raises(HTTPException, match="state mismatch"):
        await auth.entra_callback("code", "bad-state", cookie)


async def test_entra_claims_signature_roles_and_nonce(auth, monkeypatch):
    from cryptography.hazmat.primitives.asymmetric import rsa
    key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    jwk = jwt.algorithms.RSAAlgorithm.to_jwk(key.public_key(), as_dict=True)
    jwk["kid"] = "test-key"
    claims = {"exp": int(time.time()) + 300, "iat": int(time.time()), "sub": "subject", "oid": "object",
              "tid": auth.settings.tenant, "iss": f"https://login.microsoftonline.com/{auth.settings.tenant}/v2.0",
              "aud": "client", "nonce": "nonce", "roles": ["VLAN.Viewer"]}
    def handle(request):
        if request.url.path.endswith("/token"):
            return httpx.Response(200, json={"id_token": jwt.encode(claims, key, algorithm="RS256", headers={"kid": "test-key"})})
        return httpx.Response(200, json={"keys": [jwk]})
    original = httpx.AsyncClient
    monkeypatch.setattr("app.auth.httpx.AsyncClient", lambda **kwargs: original(transport=httpx.MockTransport(handle)))
    cookie = auth.sign({"state": "state", "nonce": "nonce", "verifier": "v" * 64}, "vlan-oidc", 600)
    assert auth.decode(await auth.entra_callback("code", "state", cookie))["role"] == "viewer"
    claims["roles"] = []
    with pytest.raises(HTTPException) as exc:
        await auth.entra_callback("code", "state", cookie)
    assert exc.value.status_code == 403
    claims["roles"] = ["VLAN.Admin"]
    claims["nonce"] = "different"
    with pytest.raises(HTTPException) as exc:
        await auth.entra_callback("code", "state", cookie)
    assert exc.value.status_code == 401
    claims["nonce"] = "nonce"
    claims["aud"] = "another-app"
    with pytest.raises(HTTPException) as exc:
        await auth.entra_callback("code", "state", cookie)
    assert exc.value.status_code == 401
