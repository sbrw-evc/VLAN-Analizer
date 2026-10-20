"""Read-only import of HTTP monitors from Uptime Kuma 2.x MySQL/MariaDB."""
import asyncio
import hashlib
import ssl

import pymysql

from app.gatus import validate_url

MAX_MONITORS = 1000
WEB_TYPES = {"http", "keyword", "json-query", "real-browser"}
QUERY = "SELECT id, name, url, type, active, parent FROM monitor ORDER BY id LIMIT %s"


def parse_monitors(rows, instance):
    if len(rows) > MAX_MONITORS:
        raise ValueError("Kuma import exceeds the limit of 1000 monitors (including groups)")
    by_id = {str(row["id"]): row for row in rows}
    if len(by_id) != len(rows):
        raise ValueError("Duplicate Kuma monitor IDs")
    namespace = hashlib.sha256(instance.encode()).hexdigest()[:16]
    sites, warnings = [], []

    def upstream_active(row):
        visited, active = set(), True
        while row:
            monitor_id = str(row["id"])
            if monitor_id in visited:
                raise ValueError("cyclic parent hierarchy")
            visited.add(monitor_id)
            if row["active"] not in (0, 1):
                raise ValueError("invalid active flag")
            active = active and bool(row["active"])
            parent = row.get("parent")
            if parent in (None, 0, "0", ""):
                break
            row = by_id.get(str(parent))
            if row is None:
                raise ValueError("missing parent monitor")
        return int(active)

    for row in rows:
        if row["type"] not in WEB_TYPES:
            continue
        # Warnings identify the record, never expose URLs that may contain credentials.
        label = f"Kuma monitor {row['id']}"
        try:
            url = validate_url(row.get("url") or "")
            source_enabled = upstream_active(row)
        except (ValueError, TypeError) as exc:
            warnings.append(f"{label}: {exc}")
            continue
        sites.append({"id": f"kuma-{namespace}-{row['id']}", "name": (row.get("name") or label)[:120],
                      "url": url, "source": "kuma", "enabled": 1, "source_enabled": source_enabled,
                      "deleted": 0})
    return sites, warnings


def read_monitors(settings, credentials):
    if not settings.kuma_host or not settings.kuma_database or not settings.kuma_user:
        raise ValueError("Configure KUMA_MYSQL_HOST, KUMA_MYSQL_DATABASE and KUMA_MYSQL_USER")
    password = credentials.get("kuma_mysql_password")
    if not isinstance(password, str) or not password:
        raise ValueError("OpenBao app/kuma_mysql_password is missing")
    tls = ssl.create_default_context(cafile=settings.kuma_ca or None) if settings.kuma_tls else None
    connection = pymysql.connect(host=settings.kuma_host, port=settings.kuma_port,
        user=settings.kuma_user, password=password, database=settings.kuma_database,
        charset="utf8mb4", cursorclass=pymysql.cursors.DictCursor,
        connect_timeout=10, read_timeout=10, write_timeout=10, autocommit=False,
        ssl=tls, ssl_disabled=not settings.kuma_tls)
    try:
        with connection.cursor() as cursor:
            # No schema migrations or writes are performed in the Kuma database.
            cursor.execute("START TRANSACTION READ ONLY")
            cursor.execute(QUERY, (MAX_MONITORS + 1,))
            rows = cursor.fetchall()
        return parse_monitors(rows, settings.kuma_instance)
    finally:
        try:
            connection.rollback()
        finally:
            connection.close()


async def fetch_kuma(settings, credentials):
    return await asyncio.to_thread(read_monitors, settings, credentials)
