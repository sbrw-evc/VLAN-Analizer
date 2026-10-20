import asyncio
import base64
import hashlib
import hmac
import re
import secrets
import time
from collections import defaultdict, deque
from urllib.parse import urlencode

import httpx
import jwt
from argon2 import PasswordHasher
from argon2.exceptions import VerificationError, InvalidHashError
from fastapi import HTTPException, Request

from app.config import secret_file

hasher = PasswordHasher()
USERNAME = re.compile(r"[a-zA-Z0-9_.-]{1,64}")


class Bao:
    def __init__(self, settings):
        self.settings = settings
        self.client = httpx.AsyncClient(base_url=settings.bao_url, timeout=10, trust_env=False)
        self.token = None
        self.expiry = 0
        self.lock = asyncio.Lock()

    async def headers(self):
        async with self.lock:
            if time.monotonic() >= self.expiry:
                response = await self.client.post("/v1/auth/approle/login", json={
                    "role_id": secret_file("BAO_ROLE_ID"), "secret_id": secret_file("BAO_SECRET_ID")})
                response.raise_for_status()
                auth = response.json()["auth"]
                self.token = auth["client_token"]
                self.expiry = time.monotonic() + max(1, auth["lease_duration"] - 30)
        return {"X-Vault-Token": self.token}

    async def read(self, path):
        r = await self.client.get(f"/v1/{self.settings.bao_mount}/data/vlan-analizer/{path}",
                                  headers=await self.headers())
        if r.status_code == 404:
            return None
        r.raise_for_status()
        return r.json()["data"]["data"]

    async def write(self, path, data):
        r = await self.client.post(f"/v1/{self.settings.bao_mount}/data/vlan-analizer/{path}",
                                   headers=await self.headers(), json={"data": data})
        r.raise_for_status()


class Auth:
    def __init__(self, settings, bao, app_secrets):
        self.settings, self.bao, self.secrets = settings, bao, app_secrets
        if len(app_secrets.get("session_key", "")) < 32:
            raise RuntimeError("OpenBao app/session_key must contain at least 32 characters")
        self.attempts = defaultdict(deque)
        self.jwks = None
        self.jwks_at = 0

    def sign(self, payload, audience="vlan-session", lifetime=28800):
        return jwt.encode({**payload, "iat": int(time.time()), "exp": int(time.time()) + lifetime,
                           "aud": audience}, self.secrets["session_key"], algorithm="HS256")

    def decode(self, token, audience="vlan-session"):
        try:
            return jwt.decode(token, self.secrets["session_key"], algorithms=["HS256"], audience=audience,
                              options={"require": ["exp", "iat", "aud"]})
        except jwt.InvalidTokenError:
            raise HTTPException(401, "Session expired or invalid") from None

    async def user(self, request: Request):
        user = self.decode(request.cookies.get("session", ""))
        if user.get("provider") == "local":
            current = await self.bao.read("users/" + user["sub"])
            if not current or current.get("disabled") or current.get("version") != user.get("version"):
                raise HTTPException(401, "Account or session revoked")
            user["role"] = current["role"]
        return user

    async def admin(self, request):
        user = await self.user(request)
        if user.get("role") != "admin":
            raise HTTPException(403, "Administrator role required")
        return user

    def check_origin(self, request):
        origin = request.headers.get("origin")
        if not origin or origin != self.settings.public_url:
            raise HTTPException(403, "Origin must match PUBLIC_URL")

    def rate_limit(self, key):
        now = time.monotonic()
        # Bounded map and per-address fixed sliding window; do not trust X-Forwarded-For.
        if len(self.attempts) >= 10000 and key not in self.attempts:
            raise HTTPException(429, "Login capacity reached; retry later")
        queue = self.attempts[key]
        while queue and queue[0] < now - 300:
            queue.popleft()
        if len(queue) >= 10:
            raise HTTPException(429, "Too many attempts; wait five minutes")
        queue.append(now)

    async def local_login(self, username, password, client):
        self.rate_limit(client)
        account = await self.bao.read("users/" + username) if USERNAME.fullmatch(username) else None
        try:
            if not account or account.get("disabled"):
                # Perform a hash even for an unknown user to reduce timing differences.
                await asyncio.to_thread(hasher.hash, password[:1024])
                raise HTTPException(401, "Invalid credentials")
            await asyncio.to_thread(hasher.verify, account["password_hash"], password)
        except (VerificationError, InvalidHashError):
            raise HTTPException(401, "Invalid credentials") from None
        return self.sign({"sub": username, "provider": "local", "role": account["role"],
                          "version": account["version"]})

    def set_cookie(self, response, token, name="session", max_age=28800):
        response.set_cookie(name, token, httponly=True, secure=self.settings.secure_cookie,
                            samesite="lax", max_age=max_age, path="/")

    def entra_start(self):
        if not self.settings.tenant or not self.settings.client_id:
            raise HTTPException(503, "Entra ID is not configured")
        # Single tenant only. Avoid accepting common/organizations as a trusted issuer.
        if not re.fullmatch(r"[0-9a-fA-F-]{36}", self.settings.tenant):
            raise HTTPException(503, "ENTRA_TENANT_ID must be a tenant UUID")
        state, nonce, verifier = secrets.token_urlsafe(32), secrets.token_urlsafe(32), secrets.token_urlsafe(64)
        challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).rstrip(b"=").decode()
        token = self.sign({"state": state, "nonce": nonce, "verifier": verifier}, "vlan-oidc", 600)
        url = f"https://login.microsoftonline.com/{self.settings.tenant}/oauth2/v2.0/authorize?" + urlencode({
            "client_id": self.settings.client_id, "response_type": "code", "response_mode": "query",
            "redirect_uri": self.settings.public_url + "/auth/entra/callback", "scope": "openid profile email",
            "state": state, "nonce": nonce, "code_challenge": challenge, "code_challenge_method": "S256"})
        return url, token

    async def entra_callback(self, code, state, cookie):
        flow = self.decode(cookie, "vlan-oidc")
        if not hmac.compare_digest(flow["state"], state):
            raise HTTPException(401, "OIDC state mismatch")
        base = f"https://login.microsoftonline.com/{self.settings.tenant}"
        async with httpx.AsyncClient(timeout=15, trust_env=False) as client:
            r = await client.post(base + "/oauth2/v2.0/token", data={
                "grant_type": "authorization_code", "client_id": self.settings.client_id,
                "client_secret": self.secrets["entra_client_secret"], "code": code,
                "redirect_uri": self.settings.public_url + "/auth/entra/callback",
                "code_verifier": flow["verifier"]})
            r.raise_for_status()
            raw = r.json()["id_token"]
            kid = jwt.get_unverified_header(raw).get("kid")
            if time.monotonic() - self.jwks_at > 3600 or not self.jwks or not any(
                    k.get("kid") == kid for k in self.jwks["keys"]):
                keys = await client.get(base + "/discovery/v2.0/keys")
                keys.raise_for_status()
                self.jwks, self.jwks_at = keys.json(), time.monotonic()
        key = next((k for k in self.jwks["keys"] if k.get("kid") == kid), None)
        if not key:
            raise HTTPException(401, "Unrecognized Entra signing key")
        try:
            claims = jwt.decode(raw, jwt.PyJWK.from_dict(key).key, algorithms=["RS256"],
                issuer=base + "/v2.0", audience=self.settings.client_id,
                options={"require": ["exp", "iat", "sub", "nonce", "tid", "oid"]})
        except jwt.InvalidTokenError:
            raise HTTPException(401, "Invalid Entra token") from None
        if claims["tid"] != self.settings.tenant or not hmac.compare_digest(claims["nonce"], flow["nonce"]):
            raise HTTPException(401, "Entra tenant/nonce mismatch")
        roles = set(claims.get("roles", []))
        if not roles.intersection({"VLAN.Admin", "VLAN.Viewer"}):
            raise HTTPException(403, "Assign VLAN.Admin or VLAN.Viewer in Entra ID")
        return self.sign({"sub": claims["oid"], "name": claims.get("name", "Entra user"),
                          "provider": "entra", "role": "admin" if "VLAN.Admin" in roles else "viewer"})


def sanitized_user(username, password, role, disabled=False):
    if not USERNAME.fullmatch(username) or role not in {"viewer", "admin"}:
        raise ValueError("Invalid username or role")
    if not 12 <= len(password) <= 1024:
        raise ValueError("Password must contain 12–1024 characters")
    return {"password_hash": hasher.hash(password), "role": role, "disabled": disabled,
            "version": time.time_ns()}
