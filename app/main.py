import asyncio
import hmac
import logging
from contextlib import asynccontextmanager
from datetime import datetime, timezone
from pathlib import Path
from uuid import uuid4

from fastapi import FastAPI, HTTPException, Query, Request
from fastapi.responses import FileResponse, JSONResponse, RedirectResponse
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel, Field

from app.auth import sanitized_user
from app.connections import FORMS, public_connections
from app.installation import (BaoForm, ClickHouseForm, Installation, SetupForm, test_bao, test_clickhouse)
from app.gatus import validate_url
from app.store import site_enabled

log = logging.getLogger(__name__)


@asynccontextmanager
async def lifespan(app):
    installation = Installation(app)
    app.state.installation = installation
    try:
        await installation.start()
        yield
    finally:
        await installation.close()


app = FastAPI(title="VLAN Analizer", version="0.2.0", lifespan=lifespan,
              docs_url=None, redoc_url=None, openapi_url=None)
static = Path(__file__).parent / "static"
app.mount("/static", StaticFiles(directory=static), name="static")


@app.middleware("http")
async def headers(request, call_next):
    installation = getattr(request.app.state, "installation", None)
    protected = request.url.path.startswith(("/api/", "/auth/")) and not request.url.path.startswith("/api/setup/")
    if installation and not installation.ready and protected:
        response = JSONResponse({"detail": "Complete initial setup or restore configured dependencies"}, status_code=503)
    else:
        response = await call_next(request)
    response.headers["X-Content-Type-Options"] = "nosniff"
    response.headers["Referrer-Policy"] = "no-referrer"
    response.headers["Content-Security-Policy"] = (
        "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; "
        "connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
    response.headers["Cache-Control"] = "no-store"
    return response


@app.get("/")
async def index():
    return FileResponse(static / "index.html")


@app.get("/healthz")
async def health():
    return {"status": "ok"}


@app.get("/readyz")
async def ready(request: Request):
    installation = getattr(request.app.state, "installation", None)
    if installation and not installation.ready:
        raise HTTPException(503, "Setup required or dependencies unavailable")
    await request.app.state.store.query("SELECT 1")
    await request.app.state.auth.bao.read("app")
    service = request.app.state.service
    if service.error:
        raise HTTPException(503, "Monitoring cycle failed")
    return {"status": "ready"}


@app.get("/api/setup/status")
async def setup_status(request: Request):
    return request.app.state.installation.status()


@app.post("/api/setup/test/openbao")
async def setup_test_bao(body: BaoForm, request: Request):
    request.app.state.installation.authorize_setup(request)
    try:
        return await test_bao(body)
    except Exception:
        raise HTTPException(502, "OpenBao connection failed; check URL, seal status, credentials and permissions") from None


@app.post("/api/setup/test/clickhouse")
async def setup_test_ch(body: ClickHouseForm, request: Request):
    request.app.state.installation.authorize_setup(request)
    try:
        return await test_clickhouse(body)
    except Exception:
        raise HTTPException(502, "ClickHouse connection failed; check URL, database and credentials") from None


@app.post("/api/setup/complete")
async def setup_complete(body: SetupForm, request: Request):
    installation = request.app.state.installation
    installation.authorize_setup(request)
    try:
        return await installation.complete(body)
    except HTTPException:
        raise
    except Exception:
        log.warning("Initial setup failed; configuration remains protected")
        raise HTTPException(502, "Setup could not finish. Check connections, credentials and write/schema permissions; retry with the same settings") from None


@app.get("/api/settings/connections")
async def get_connections(request: Request):
    await request.app.state.auth.admin(request)
    installation = request.app.state.installation
    return public_connections(installation.connections, request.app.state.settings.public_url)


async def change_connection(source, body, request, test_only=False):
    request.app.state.auth.check_origin(request)
    await request.app.state.auth.admin(request)
    if source not in FORMS:
        raise HTTPException(404, "Unknown integration")
    try:
        form = FORMS[source].model_validate(body)
        return await request.app.state.installation.update_connection(source, form, test_only)
    except (ValueError, TypeError):
        raise HTTPException(422, "Invalid integration settings or missing credential") from None
    except Exception:
        raise HTTPException(502, "Integration connection or save failed; check credentials, permissions and server settings") from None


@app.put("/api/settings/connections/{source}")
async def save_connection(source: str, body: dict, request: Request):
    return await change_connection(source, body, request)


@app.post("/api/settings/connections/{source}/test")
async def test_saved_connection(source: str, body: dict, request: Request):
    return await change_connection(source, body, request, test_only=True)


class Login(BaseModel):
    username: str = Field(max_length=64)
    password: str = Field(max_length=1024)


@app.post("/auth/local")
async def local(body: Login, request: Request):
    auth = request.app.state.auth
    auth.check_origin(request)
    token = await auth.local_login(body.username, body.password, request.client.host)
    response = JSONResponse({"ok": True})
    auth.set_cookie(response, token)
    return response


@app.get("/auth/entra")
async def entra(request: Request):
    url, cookie = request.app.state.auth.entra_start()
    response = RedirectResponse(url)
    request.app.state.auth.set_cookie(response, cookie, "oidc", 600)
    return response


@app.get("/auth/entra/callback")
async def entra_callback(request: Request, code: str = "", state: str = "", error: str = ""):
    if error or not code:
        raise HTTPException(401, "Entra authentication failed")
    try:
        token = await request.app.state.auth.entra_callback(code, state, request.cookies.get("oidc", ""))
    except (HTTPException, asyncio.CancelledError):
        raise
    except Exception:
        log.exception("Entra callback failed")
        raise HTTPException(401, "Entra authentication failed") from None
    response = RedirectResponse("/")
    response.delete_cookie("oidc", path="/")
    request.app.state.auth.set_cookie(response, token)
    return response


@app.post("/auth/logout")
async def logout(request: Request):
    request.app.state.auth.check_origin(request)
    response = JSONResponse({"ok": True})
    response.delete_cookie("session", path="/")
    return response


@app.get("/api/me")
async def me(request: Request):
    user = await request.app.state.auth.user(request)
    return {k: user.get(k) for k in ("sub", "name", "role", "provider")}


def fresh_rows(rows, max_age):
    now = datetime.now(timezone.utc)
    result = []
    for row in rows:
        age = (now - datetime.fromisoformat(row["at"]).astimezone(timezone.utc)).total_seconds()
        result.append({**row, "measured_status": row["status"], "age_seconds": round(age),
                       "status": "unknown" if age > max_age else row["status"]})
    return result


async def snapshot(request):
    service, store = request.app.state.service, request.app.state.store
    sites = await store.sites()
    ids = {s["id"] for s in sites if site_enabled(s)}
    ifaces = {i["interface"] for i in service.inventory["interfaces"] if i["probeable"]}
    results = [r for r in await store.latest() if r["site_id"] in ids and r["interface"] in ifaces]
    return {"sites": sites, **service.inventory, "inventory_at": service.inventory_at,
            "results": fresh_rows(results, request.app.state.settings.stale_after),
            "worker": {"running": service.running, "last_cycle": service.last_cycle,
                       "error": service.error, "gatus_warnings": service.gatus_warnings,
                       "kuma_warnings": service.kuma_warnings}}


@app.get("/api/dashboard")
async def dashboard(request: Request):
    await request.app.state.auth.user(request)
    return await snapshot(request)


class SiteBody(BaseModel):
    name: str = Field(min_length=1, max_length=120)
    url: str = Field(max_length=2048)
    enabled: bool = True


@app.post("/api/sites", status_code=201)
async def create_site(body: SiteBody, request: Request):
    auth = request.app.state.auth
    auth.check_origin(request)
    await auth.admin(request)
    try:
        validate_url(body.url)
    except ValueError as exc:
        raise HTTPException(422, str(exc)) from None
    if len(await request.app.state.store.sites()) >= 1000:
        raise HTTPException(409, "Site limit of 1000 reached")
    site = {"id": str(uuid4()), "name": body.name, "url": body.url,
            "source": "manual", "enabled": int(body.enabled), "deleted": 0}
    async with request.app.state.service.lock:
        await request.app.state.store.save_site(site)
    request.app.state.service.wake.set()
    return site


@app.put("/api/sites/{site_id}")
async def update_site(site_id: str, body: SiteBody, request: Request):
    auth, service = request.app.state.auth, request.app.state.service
    auth.check_origin(request)
    await auth.admin(request)
    try:
        validate_url(body.url)
    except ValueError as exc:
        raise HTTPException(422, str(exc)) from None
    async with service.lock:
        site = next((s for s in await service.store.sites() if s["id"] == site_id), None)
        if not site:
            raise HTTPException(404, "Site not found")
        if site["source"] in {"gatus", "kuma"} and body.url != site["url"]:
            raise HTTPException(409, "Change imported URLs in the source system")
        site.update(name=body.name, url=body.url, enabled=int(body.enabled))
        await service.store.save_site(site)
    service.wake.set()
    return site


@app.delete("/api/sites/{site_id}")
async def delete_site(site_id: str, request: Request):
    auth, service = request.app.state.auth, request.app.state.service
    auth.check_origin(request)
    await auth.admin(request)
    async with service.lock:
        site = next((s for s in await service.store.sites() if s["id"] == site_id), None)
        if not site:
            raise HTTPException(404, "Site not found")
        if site["source"] in {"gatus", "kuma"}:
            raise HTTPException(409, "Disable imported sites or remove them in the source system")
        await service.store.save_site({**site, "deleted": 1})
    return {"ok": True}


@app.post("/api/checks/run", status_code=202)
async def run_now(request: Request):
    request.app.state.auth.check_origin(request)
    await request.app.state.auth.admin(request)
    request.app.state.service.wake.set()
    return {"queued": True}


@app.post("/api/gatus/sync")
async def sync(request: Request):
    request.app.state.auth.check_origin(request)
    await request.app.state.auth.admin(request)
    return await request.app.state.service.sync()


@app.post("/api/kuma/sync")
async def sync_kuma(request: Request):
    request.app.state.auth.check_origin(request)
    await request.app.state.auth.admin(request)
    try:
        result = await request.app.state.service.sync("kuma")
    except HTTPException:
        raise
    except Exception:
        log.warning("Manual Kuma import failed")
        raise HTTPException(502, "Kuma import failed; verify database connection and configuration") from None
    request.app.state.service.wake.set()
    return result


@app.get("/api/history")
async def history(request: Request, site_id: str, interface: str, hours: int = Query(default=24, ge=1, le=2160)):
    await request.app.state.auth.user(request)
    return await request.app.state.store.history(site_id, interface, hours)


class LocalUser(BaseModel):
    username: str = Field(max_length=64)
    password: str = Field(max_length=1024)
    role: str = "viewer"
    disabled: bool = False


@app.put("/api/users")
async def upsert_user(body: LocalUser, request: Request):
    auth = request.app.state.auth
    auth.check_origin(request)
    await auth.admin(request)
    try:
        record = await asyncio.to_thread(sanitized_user, body.username, body.password, body.role, body.disabled)
    except ValueError as exc:
        raise HTTPException(422, str(exc)) from None
    await auth.bao.write("users/" + body.username, record)
    return {"username": body.username, "role": body.role, "disabled": body.disabled}


async def machine_auth(request):
    expected = request.app.state.auth.secrets.get("zabbix_token", "")
    supplied = request.headers.get("authorization", "").removeprefix("Bearer ")
    if not expected or not hmac.compare_digest(expected, supplied):
        raise HTTPException(401, "Invalid integration token")


def zabbix_payload(data):
    lookup = {(r["site_id"], r["interface"]): r for r in data["results"]}
    items = []
    for site in data["sites"]:
        if not site_enabled(site):
            continue
        for iface in data["interfaces"]:
            if not iface["probeable"]:
                continue
            row = lookup.get((site["id"], iface["interface"]), {})
            status = row.get("status", "unknown")
            items.append({"site_id": site["id"], "site": site["name"], "interface": iface["interface"],
                          "vlan_id": iface["vlan_id"], "status": status,
                          "available": {"up": 1, "down": 0, "degraded": 2}.get(status, -1),
                          "at": row.get("at"), "dns_ms": row.get("dns", {}).get("ms"),
                          "tcp_ms": row.get("tcp_ms"), "http_code": row.get("http", {}).get("code"),
                          "http_ms": row.get("http", {}).get("total_ms"),
                          "page_ms": row.get("browser", {}).get("load_ms"),
                          "tls_days": row.get("tls", {}).get("expires_days")})
    return items


@app.get("/api/integrations/zabbix")
async def zabbix(request: Request):
    await machine_auth(request)
    return {"generated_at": datetime.now(timezone.utc).isoformat(),
            "items": zabbix_payload(await snapshot(request))}


@app.get("/api/integrations/zabbix/discovery")
async def discovery(request: Request):
    await machine_auth(request)
    return [{"{#SITE_ID}": i["site_id"], "{#SITE}": i["site"], "{#INTERFACE}": i["interface"],
             "{#VLAN}": str(i["vlan_id"]) if i["vlan_id"] is not None else "untagged-or-unknown"}
            for i in zabbix_payload(await snapshot(request))]
