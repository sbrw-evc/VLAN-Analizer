"""One-time provisioning against an initialized, unsealed OpenBao; never prints secrets."""
import getpass
import os
import secrets
from pathlib import Path

import httpx

from app.auth import sanitized_user


def private_write(path, content):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    try:
        os.fchmod(fd, 0o600)
        os.write(fd, content.encode())
    finally:
        os.close(fd)


def main():
    directory = Path(os.environ.get("SECRETS_DIR", "secrets"))
    if not (directory / "clickhouse_password").exists():
        raise SystemExit("Run deploy/prepare.py first")
    if (directory / "bao_role_id").exists():
        raise SystemExit("Already provisioned: this script will not overwrite credentials")
    token_path = os.environ.get("BAO_TOKEN_FILE")
    token = Path(token_path).read_text().strip() if token_path else getpass.getpass("OpenBao provisioning token: ")
    username = input("Initial administrator username [admin]: ").strip() or "admin"
    password = getpass.getpass("Administrator password (12+ characters): ")
    confirmation = getpass.getpass("Repeat password: ")
    if password != confirmation:
        raise SystemExit("Passwords differ")
    user = sanitized_user(username, password, "admin")
    entra_secret = getpass.getpass("Entra client secret (Enter to configure later): ")
    with httpx.Client(base_url=os.environ.get("BAO_ADDR", "http://127.0.0.1:8200"),
                      headers={"X-Vault-Token": token}, timeout=20, trust_env=False) as client:
        def post(path, data):
            r = client.post("/v1/" + path, json=data)
            r.raise_for_status()
            return r.json() if r.content else {}

        mounts = client.get("/v1/sys/mounts")
        mounts.raise_for_status()
        if "secret/" not in mounts.json()["data"]:
            post("sys/mounts/secret", {"type": "kv", "options": {"version": "2"}})
        auth = client.get("/v1/sys/auth")
        auth.raise_for_status()
        if "approle/" not in auth.json()["data"]:
            post("sys/auth/approle", {"type": "approle"})
        post("sys/policies/acl/vlan-analizer", {"policy": Path(__file__).with_name("bao-policy.hcl").read_text()})
        # This AppRole must be able to log in after token expiry; protect its mounted SecretID.
        post("auth/approle/role/vlan-analizer", {"token_policies": ["vlan-analizer"], "token_ttl": "1h",
                                               "token_max_ttl": "4h", "secret_id_ttl": "0",
                                               "secret_id_num_uses": 0})
        existing = client.get("/v1/secret/data/vlan-analizer/app")
        if existing.status_code != 404:
            existing.raise_for_status()
            raise SystemExit("App secret already exists: provision AppRole credentials manually to preserve it")
        post("secret/data/vlan-analizer/app", {"data": {
            "session_key": secrets.token_urlsafe(64), "zabbix_token": secrets.token_urlsafe(48),
            "clickhouse_password": (directory / "clickhouse_password").read_text().strip(),
            "entra_client_secret": entra_secret, "gatus_token": "", "kuma_mysql_password": ""}})
        post("secret/data/vlan-analizer/users/" + username, {"data": user})
        role = client.get("/v1/auth/approle/role/vlan-analizer/role-id")
        role.raise_for_status()
        secret = post("auth/approle/role/vlan-analizer/secret-id", {})
        private_write(directory / "bao_role_id", role.json()["data"]["role_id"])
        private_write(directory / "bao_secret_id", secret["data"]["secret_id"])
    print("Provisioned. Start the application; integration tokens remain in OpenBao.")


if __name__ == "__main__":
    main()
