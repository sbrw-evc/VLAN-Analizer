"""Persistent integration configuration; credentials never leave OpenBao on reads."""
import re
from dataclasses import replace
from urllib.parse import urlsplit

import httpx
from pydantic import BaseModel, ConfigDict, Field, model_validator

from app.gatus import fetch_gatus
from app.kuma import fetch_kuma


class Form(BaseModel):
    model_config = ConfigDict(extra="forbid")


def server_url(value):
    p = urlsplit(value)
    if p.scheme not in {"http", "https"} or not p.hostname or p.username or p.password or p.query or p.fragment:
        raise ValueError("Enter an HTTP(S) server URL without credentials or query parameters")
    return value.rstrip("/")


class GatusForm(Form):
    enabled: bool = False
    url: str = Field(default="", max_length=2048)
    config_path: str = Field(default="", max_length=1024)
    token: str | None = Field(default=None, max_length=8192)

    @model_validator(mode="after")
    def validate_source(self):
        if self.url:
            self.url = server_url(self.url)
        if self.enabled and bool(self.url) == bool(self.config_path):
            raise ValueError("Choose either a Gatus API URL or a mounted YAML file")
        return self


class KumaForm(Form):
    enabled: bool = False
    host: str = Field(default="", max_length=253)
    port: int = Field(default=3306, ge=1, le=65535)
    database: str = Field(default="kuma", min_length=1, max_length=64)
    user: str = Field(default="vlan_reader", min_length=1, max_length=80)
    tls: bool = True
    ca: str = Field(default="", max_length=1024)
    instance: str = Field(default="default", min_length=1, max_length=120)
    password: str | None = Field(default=None, max_length=8192)

    @model_validator(mode="after")
    def validate_source(self):
        if self.enabled and (not self.host.strip() or any(c.isspace() for c in self.host)):
            raise ValueError("Enter a valid database host")
        return self


class EntraForm(Form):
    enabled: bool = False
    tenant: str = Field(default="", max_length=36)
    client_id: str = Field(default="", max_length=36)
    client_secret: str | None = Field(default=None, max_length=8192)

    @model_validator(mode="after")
    def validate_source(self):
        guid = r"[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}"
        if self.enabled and not all(re.fullmatch(guid, s) for s in (self.tenant, self.client_id)):
            raise ValueError("Entra tenant and client IDs must be UUIDs")
        self.tenant = self.tenant.lower()
        return self


FORMS = {"gatus": GatusForm, "kuma": KumaForm, "entra": EntraForm}
SECRET_FIELDS = {"gatus": "token", "kuma": "password", "entra": "client_secret"}


def legacy_connections(settings, app_secrets):
    return {
        "gatus": GatusForm(enabled=bool(settings.gatus_url or settings.gatus_config), url=settings.gatus_url,
            config_path=settings.gatus_config, token=app_secrets.get("gatus_token", "")).model_dump(),
        "kuma": KumaForm(enabled=bool(settings.kuma_host), host=settings.kuma_host, port=settings.kuma_port,
            database=settings.kuma_database, user=settings.kuma_user, tls=settings.kuma_tls, ca=settings.kuma_ca,
            instance=settings.kuma_instance, password=app_secrets.get("kuma_mysql_password", "")).model_dump(),
        "entra": EntraForm(enabled=bool(settings.tenant and settings.client_id), tenant=settings.tenant,
            client_id=settings.client_id, client_secret=app_secrets.get("entra_client_secret", "")).model_dump(),
    }


def merge_form(source, form, current):
    value = form.model_dump()
    key = SECRET_FIELDS[source]
    if value[key] is None:
        value[key] = current.get(key, "")
    if value["enabled"] and source != "gatus" and not value[key]:
        raise ValueError("A credential is required to enable this integration")
    return value


def apply_connections(base, values, app_secrets):
    g, k, e = (values[s] for s in ("gatus", "kuma", "entra"))
    settings = replace(base, gatus_url=g["url"] if g["enabled"] else "",
        gatus_config=g["config_path"] if g["enabled"] else "", kuma_host=k["host"] if k["enabled"] else "",
        kuma_port=k["port"], kuma_database=k["database"], kuma_user=k["user"], kuma_tls=k["tls"],
        kuma_ca=k["ca"], kuma_instance=k["instance"], tenant=e["tenant"] if e["enabled"] else "",
        client_id=e["client_id"] if e["enabled"] else "")
    credentials = {**app_secrets, "gatus_token": g.get("token") or "",
                   "kuma_mysql_password": k.get("password") or "", "entra_client_secret": e.get("client_secret") or ""}
    return settings, credentials


def public_connections(values, public_url):
    result = {}
    for source, value in values.items():
        key = SECRET_FIELDS[source]
        result[source] = {k: v for k, v in value.items() if k != key}
        result[source][key + "_set"] = bool(value.get(key))
    result["entra"]["redirect_uri"] = public_url + "/auth/entra/callback"
    return result


async def test_connection(source, settings, credentials):
    if source == "gatus":
        sites, warnings = await fetch_gatus(settings, credentials)
        return {"ok": True, "sites": len(sites), "warnings": warnings}
    if source == "kuma":
        sites, warnings = await fetch_kuma(settings, credentials)
        return {"ok": True, "sites": len(sites), "warnings": warnings}
    if not credentials.get("entra_client_secret"):
        raise ValueError("Entra client secret is required")
    issuer = f"https://login.microsoftonline.com/{settings.tenant}/v2.0"
    async with httpx.AsyncClient(timeout=15, trust_env=False) as client:
        r = await client.get(issuer + "/.well-known/openid-configuration")
        r.raise_for_status()
        if r.json().get("issuer") != issuer:
            raise ValueError("Entra issuer does not match this tenant")
    return {"ok": True, "message": "Tenant discovery verified. Client credentials and app roles are verified at sign-in."}
