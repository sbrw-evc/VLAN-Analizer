"""First-run lifecycle. Only AppRole bootstrap credentials are stored on local disk."""
import asyncio
import hmac
import json
import logging
import os
import secrets
import tempfile
from dataclasses import replace
from pathlib import Path
from urllib.parse import urlsplit

import httpx
from fastapi import HTTPException
from pydantic import Field, model_validator

from app.auth import Auth, Bao, hasher, sanitized_user
from app.config import Settings
from app.connections import (EntraForm, Form, apply_connections, legacy_connections,
                             server_url, test_connection)
from app.service import Service
from app.store import Store

log = logging.getLogger(__name__)


class BaoForm(Form):
    url: str = Field(max_length=2048)
    mount: str = Field(default="secret", pattern=r"^[a-zA-Z0-9_-]{1,64}$")
    mode: str = Field(default="token", pattern=r"^(token|approle)$")
    token: str = Field(default="", max_length=8192)
    role_id: str = Field(default="", max_length=8192)
    secret_id: str = Field(default="", max_length=8192)

    @model_validator(mode="after")
    def validate_connection(self):
        self.url = server_url(self.url)
        if self.mode == "token" and not self.token:
            raise ValueError("Enter an OpenBao provisioning token")
        if self.mode == "approle" and not (self.role_id and self.secret_id):
            raise ValueError("Enter AppRole RoleID and SecretID")
        return self


class ClickHouseForm(Form):
    url: str = Field(max_length=2048)
    user: str = Field(default="vlan", min_length=1, max_length=120)
    database: str = Field(default="default", pattern=r"^[a-zA-Z0-9_]{1,64}$")
    password: str = Field(min_length=1, max_length=8192)

    @model_validator(mode="after")
    def validate_connection(self):
        self.url = server_url(self.url)
        return self


class SetupForm(Form):
    public_url: str = Field(max_length=2048)
    bao: BaoForm
    clickhouse: ClickHouseForm
    admin_username: str = Field(pattern=r"^[a-zA-Z0-9_.-]{1,64}$")
    admin_password: str = Field(min_length=12, max_length=1024)
    entra: EntraForm = Field(default_factory=EntraForm)

    @model_validator(mode="after")
    def validate_origin(self):
        self.public_url = self.public_url.rstrip("/")
        Settings(public_url=self.public_url)
        if self.entra.enabled and not self.entra.client_secret:
            raise ValueError("Entra client secret is required")
        return self


def atomic_json(path, value):
    path = Path(path)
    fd, tmp = tempfile.mkstemp(prefix=".write-", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as f:
            os.fchmod(f.fileno(), 0o600)
            json.dump(value, f)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, path)
        directory = os.open(path.parent, os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)


def connection_settings(base, form):
    return replace(base, public_url=form.public_url, bao_url=form.bao.url, bao_mount=form.bao.mount,
                   clickhouse_url=form.clickhouse.url, clickhouse_user=form.clickhouse.user,
                   clickhouse_database=form.clickhouse.database,
                   tenant=form.entra.tenant if form.entra.enabled else "",
                   client_id=form.entra.client_id if form.entra.enabled else "")


async def test_bao(form):
    settings = Settings(bao_url=form.url, bao_mount=form.mount)
    if form.mode == "approle":
        bao = Bao(settings, form.role_id, form.secret_id)
        try:
            await bao.read("app")
        finally:
            await bao.client.aclose()
    else:
        async with httpx.AsyncClient(base_url=form.url, headers={"X-Vault-Token": form.token},
                                      timeout=15, trust_env=False) as client:
            r = await client.get("/v1/sys/mounts")
            r.raise_for_status()
            mount = r.json()["data"].get(form.mount + "/")
            if mount and (mount["type"] != "kv" or mount.get("options", {}).get("version") != "2"):
                raise ValueError("Choose a KV v2 mount")
            r = await client.get("/v1/sys/auth")
            r.raise_for_status()
    return {"ok": True, "message": "OpenBao connection verified"}


async def test_clickhouse(form):
    store = Store(Settings(clickhouse_url=form.url, clickhouse_user=form.user,
                           clickhouse_database=form.database), form.password)
    try:
        await store.query("SELECT 1")
    finally:
        await store.close()
    return {"ok": True, "message": "ClickHouse connection verified; schema permissions are checked when completing setup"}


async def provision_role(form, role_name):
    async with httpx.AsyncClient(base_url=form.url, headers={"X-Vault-Token": form.token},
                                  timeout=15, trust_env=False) as client:
        async def post(path, body):
            r = await client.post("/v1/" + path, json=body)
            r.raise_for_status()
            return r.json() if r.content else {}
        mounts = await client.get("/v1/sys/mounts")
        mounts.raise_for_status()
        if form.mount + "/" not in mounts.json()["data"]:
            await post("sys/mounts/" + form.mount, {"type": "kv", "options": {"version": "2"}})
        auth = await client.get("/v1/sys/auth")
        auth.raise_for_status()
        if "approle/" not in auth.json()["data"]:
            await post("sys/auth/approle", {"type": "approle"})
        policy = (f'path "{form.mount}/data/vlan-analizer/app" {{ capabilities = ["create", "read"] }}\n'
                  f'path "{form.mount}/data/vlan-analizer/users/*" {{ capabilities = ["create", "read", "update"] }}\n'
                  f'path "{form.mount}/data/vlan-analizer/connections/*" {{ capabilities = ["create", "read", "update"] }}')
        await post("sys/policies/acl/" + role_name, {"policy": policy})
        await post("auth/approle/role/" + role_name, {"token_policies": [role_name], "token_ttl": "1h",
                   "token_max_ttl": "4h", "secret_id_ttl": "0", "secret_id_num_uses": 0})
        role = await client.get(f"/v1/auth/approle/role/{role_name}/role-id")
        role.raise_for_status()
        secret = await post(f"auth/approle/role/{role_name}/secret-id", {})
        return {"role_id": role.json()["data"]["role_id"], "secret_id": secret["data"]["secret_id"]}


class Installation:
    def __init__(self, app, directory=None):
        self.app = app
        self.directory = Path(directory or os.getenv("APP_DATA_DIR", "/var/lib/vlan-analizer"))
        self.directory.mkdir(mode=0o700, parents=True, exist_ok=True)
        self.config_path = self.directory / "installation.json"
        self.pending_path = self.directory / "pending.json"
        self.token_path = self.directory / "setup-token"
        self.lock = asyncio.Lock()
        self.worker = self.recovery = None
        self.bao = self.store = None
        self.ready = False
        self.error = None
        self.connections = None
        self.base = Settings()
        self.config = json.loads(self.config_path.read_text()) if self.config_path.exists() else None
        self.legacy = bool(os.getenv("BAO_ROLE_ID_FILE") and os.getenv("BAO_SECRET_ID_FILE")) and not self.config
        if not self.initialized and not self.token_path.exists():
            # Never place this token in URLs, HTML, API responses, logs or localStorage.
            fd = os.open(self.token_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(fd, "w") as f:
                f.write(secrets.token_urlsafe(48))

    @property
    def initialized(self):
        return bool(self.config or self.legacy)

    def status(self):
        return {"required": not self.initialized, "ready": self.ready, "error": self.error,
                "entra_enabled": bool(self.ready and self.app.state.settings.tenant)}

    def authorize_setup(self, request):
        if self.initialized:
            raise HTTPException(409, "Initial setup is already complete")
        # Token is the authority, not client IP or untrusted forwarded headers.
        origin = urlsplit(request.headers.get("origin", ""))
        if origin.scheme not in {"http", "https"} or origin.netloc != request.headers.get("host"):
            raise HTTPException(403, "Same-origin setup request required")
        supplied = request.headers.get("x-setup-token", "")
        expected = self.token_path.read_text().strip()
        if not hmac.compare_digest(supplied, expected):
            raise HTTPException(401, "Invalid setup access key")

    async def start(self):
        if self.initialized:
            try:
                await self.activate()
            except Exception:
                self.error = "Dependencies unavailable. Verify OpenBao, ClickHouse and stored credentials. Retrying automatically."
                log.warning("Configured installation dependencies are unavailable")
                self.recovery = asyncio.create_task(self.retry())

    async def retry(self):
        while not self.ready:
            await asyncio.sleep(30)
            try:
                await self.activate()
            except Exception:
                log.warning("Installation recovery is waiting for dependencies")

    async def activate(self):
        base = self.base
        credentials = {}
        if self.config:
            base = replace(base, **self.config["settings"])
            credentials = json.loads((self.directory / "approle.json").read_text())
        bao = Bao(base, **credentials)
        store = None
        try:
            app_secrets = await bao.read("app")
            if not app_secrets:
                raise ValueError("Application secrets missing")
            connections = legacy_connections(base, app_secrets)
            for source in connections:
                try:
                    saved = await bao.read("connections/" + source)
                except httpx.HTTPStatusError as exc:
                    if not self.legacy or exc.response.status_code != 403:
                        raise
                    saved = None
                if saved is not None:
                    connections[source] = saved
            settings, runtime_secrets = apply_connections(base, connections, app_secrets)
            store = Store(settings, app_secrets["clickhouse_password"])
            await store.init()
            auth = Auth(settings, bao, runtime_secrets)
            service = Service(store, settings, auth)
        except BaseException:
            await bao.client.aclose()
            if store:
                await store.close()
            raise
        self.bao, self.store, self.base = bao, store, base
        self.app_secrets, self.connections = app_secrets, connections
        self.app.state.auth, self.app.state.store = auth, store
        self.app.state.settings, self.app.state.service = settings, service
        self.worker = asyncio.create_task(service.loop())
        self.error, self.ready = None, True

    async def complete(self, form):
        async with self.lock:
            if self.initialized:
                raise HTTPException(409, "Initial setup is already complete")
            await test_bao(form.bao)
            await test_clickhouse(form.clickhouse)
            base = connection_settings(self.base, form)
            if form.entra.enabled:
                await test_connection("entra", base, {"entra_client_secret": form.entra.client_secret})
            # Journal supports retry after a partial failure without replacing an existing installation.
            public_settings = {k: getattr(base, k) for k in ("public_url", "bao_url", "bao_mount", "clickhouse_url",
                               "clickhouse_user", "clickhouse_database", "tenant", "client_id")}
            if self.pending_path.exists():
                pending = json.loads(self.pending_path.read_text())
                if pending["settings"] != public_settings or pending["admin"] != form.admin_username:
                    raise HTTPException(409, "Retry the incomplete setup with the original connection settings and administrator name")
                credentials = json.loads((self.directory / "approle.json").read_text())
            else:
                installation_id = secrets.token_hex(16)
                credentials = (await provision_role(form.bao, "vlan-" + installation_id) if form.bao.mode == "token"
                               else {"role_id": form.bao.role_id, "secret_id": form.bao.secret_id})
                pending = {"id": installation_id, "settings": public_settings, "admin": form.admin_username}
                atomic_json(self.directory / "approle.json", credentials)
                atomic_json(self.pending_path, pending)
            bao = Bao(base, **credentials)
            store = Store(base, form.clickhouse.password)
            try:
                await store.init()
                current = await bao.read("app")
                if current and current.get("installation_id") != pending["id"]:
                    raise HTTPException(409, "OpenBao already contains another installation; no secrets were replaced")
                if not current:
                    await bao.write("app", {"installation_id": pending["id"], "session_key": secrets.token_urlsafe(64),
                        "zabbix_token": secrets.token_urlsafe(48), "clickhouse_password": form.clickhouse.password,
                        "entra_client_secret": "", "gatus_token": "", "kuma_mysql_password": ""}, cas=0)
                elif current["clickhouse_password"] != form.clickhouse.password:
                    raise HTTPException(409, "Retry with the original ClickHouse password")
                account = await bao.read("users/" + form.admin_username)
                if account:
                    if account.get("role") != "admin" or account.get("disabled"):
                        raise HTTPException(409, "Existing administrator cannot be replaced")
                    await asyncio.to_thread(hasher.verify, account["password_hash"], form.admin_password)
                else:
                    record = await asyncio.to_thread(sanitized_user, form.admin_username, form.admin_password, "admin")
                    await bao.write("users/" + form.admin_username, record, cas=0)
                await bao.write("connections/entra", form.entra.model_dump())
                config = {"version": 1, "settings": public_settings}
                atomic_json(self.config_path, config)
                self.config = config
            finally:
                await bao.client.aclose()
                await store.close()
            self.token_path.unlink(missing_ok=True)
            self.pending_path.unlink(missing_ok=True)
            try:
                await self.activate()
            except Exception:
                self.error = "Setup saved. Waiting for dependencies to reconnect."
                self.recovery = asyncio.create_task(self.retry())
            return {"ok": True, "ready": self.ready}

    async def close(self):
        for task in (self.worker, self.recovery):
            if task:
                task.cancel()
                await asyncio.gather(task, return_exceptions=True)
        if self.store:
            await self.store.close()
        if self.bao:
            await self.bao.client.aclose()

    async def update_connection(self, source, form, test_only=False):
        from app.connections import merge_form, public_connections
        async with self.lock:
            async with self.app.state.service.lock:
                value = merge_form(source, form, self.connections[source])
                values = {**self.connections, source: value}
                settings, credentials = apply_connections(self.base, values, self.app_secrets)
                if test_only:
                    if not value["enabled"]:
                        raise ValueError("Enable the integration to test it")
                    return await test_connection(source, settings, credentials)
                # Write first, then swap a complete settings snapshot for the running worker.
                await self.bao.write("connections/" + source, value)
                self.connections = values
                self.app.state.settings = settings
                self.app.state.auth.settings = settings
                self.app.state.auth.secrets = credentials
                self.app.state.service.settings = settings
                self.app.state.service.last_sync.pop(source, None)
                self.app.state.service.wake.set()
                return public_connections(self.connections, settings.public_url)
