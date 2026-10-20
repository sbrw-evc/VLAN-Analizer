"""All durable configuration, inventory and measurements live in ClickHouse."""
import json
import time
from datetime import datetime, timezone

import httpx

SCHEMA = [
    """CREATE TABLE IF NOT EXISTS sites (
      id String, name String, url String, source String, enabled UInt8, source_enabled UInt8 DEFAULT 1,
      version UInt64, deleted UInt8 DEFAULT 0
    ) ENGINE = ReplacingMergeTree(version) ORDER BY id""",
    "ALTER TABLE sites ADD COLUMN IF NOT EXISTS source_enabled UInt8 DEFAULT 1",
    """CREATE TABLE IF NOT EXISTS inventory (
      at DateTime64(3, 'UTC'), payload String
    ) ENGINE = MergeTree ORDER BY at TTL toDateTime(at) + INTERVAL 30 DAY""",
    """CREATE TABLE IF NOT EXISTS checks (
      at DateTime64(3, 'UTC'), site_id String, interface String,
      status LowCardinality(String), payload String
    ) ENGINE = MergeTree PARTITION BY toYYYYMM(at)
      ORDER BY (site_id, interface, at) TTL toDateTime(at) + INTERVAL 90 DAY""",
]


def site_enabled(site):
    return bool(site["enabled"] and site.get("source_enabled", 1))


def stamp():
    return datetime.now(timezone.utc).isoformat(timespec="milliseconds")


class Store:
    def __init__(self, settings, password):
        self.client = httpx.AsyncClient(base_url=settings.clickhouse_url,
            auth=(settings.clickhouse_user, password), timeout=30, trust_env=False)

    async def close(self):
        await self.client.aclose()

    async def query(self, sql, params=None):
        r = await self.client.post("/", params=params, content=sql)
        r.raise_for_status()
        return r.text

    async def rows(self, sql, **params):
        raw = await self.query(sql + " FORMAT JSONEachRow", {"param_" + k: str(v) for k, v in params.items()})
        return [json.loads(line) for line in raw.splitlines() if line]

    async def init(self):
        for sql in SCHEMA:
            await self.query(sql)

    async def insert(self, table, rows):
        if table not in {"sites", "inventory", "checks"}:
            raise ValueError("Unknown table")
        if rows:
            await self.query(f"INSERT INTO {table} FORMAT JSONEachRow\n" + "\n".join(json.dumps(r) for r in rows),
                             {"date_time_input_format": "best_effort"})

    async def sites(self):
        return await self.rows("SELECT * FROM sites FINAL WHERE deleted = 0 ORDER BY name")

    async def save_site(self, site):
        await self.insert("sites", [{**site, "version": time.time_ns()}])

    async def latest(self):
        return [json.loads(r["payload"]) for r in await self.rows(
            "SELECT argMax(payload, at) AS payload FROM checks GROUP BY site_id, interface")]

    async def history(self, site_id, interface, hours):
        return [json.loads(r["payload"]) for r in await self.rows(
            "SELECT payload FROM checks WHERE site_id = {site:String} AND interface = {iface:String} "
            "AND at >= now() - toIntervalHour({hours:UInt32}) ORDER BY at DESC LIMIT 1000",
            site=site_id, iface=interface, hours=hours)]
