import asyncio
import json
import logging
import time

from fastapi import HTTPException

from app.gatus import fetch_gatus
from app.kuma import fetch_kuma
from app.network import discover
from app.probes import run_probe
from app.store import site_enabled, stamp

log = logging.getLogger(__name__)


class Service:
    def __init__(self, store, settings, auth):
        self.store, self.settings, self.auth = store, settings, auth
        self.inventory = {"interfaces": [], "bridge_vlans": []}
        self.inventory_at = None
        self.last_cycle = None
        self.error = None
        self.gatus_warnings = []
        self.kuma_warnings = []
        self.running = False
        self.wake = asyncio.Event()
        self.lock = asyncio.Lock()
        self.last_sync = {}

    async def sync(self, source="gatus"):
        if source not in {"gatus", "kuma"}:
            raise ValueError("Unknown import source")
        configured = (self.settings.gatus_url or self.settings.gatus_config) if source == "gatus" else self.settings.kuma_host
        if not configured:
            raise HTTPException(503, f"{source.title()} import is not configured")
        fetch = fetch_gatus if source == "gatus" else fetch_kuma
        async with self.lock:
            sites, warnings = await fetch(self.settings, self.auth.secrets)
            existing = {s["id"]: s for s in await self.store.sites()}
            for site in sites:
                if existing.get(site["id"], {}).get("enabled") == 0:
                    site["enabled"] = 0
                if any(existing.get(site["id"], {}).get(k) != site[k] for k in site):
                    await self.store.save_site(site)
            retired = 0
            if not warnings:
                imported_ids = {site["id"] for site in sites}
                for old in existing.values():
                    if old["source"] == source and old["id"] not in imported_ids:
                        await self.store.save_site({**old, "deleted": 1})
                        retired += 1
            setattr(self, f"{source}_warnings", warnings)
            self.last_sync[source] = time.monotonic()
            return {"imported": len(sites), "retired": retired, "warnings": warnings}

    async def cycle(self):
        self.running = True
        try:
            self.inventory = await discover(self.settings.include)
            self.inventory_at = stamp()
            await self.store.insert("inventory", [{"at": self.inventory_at, "payload": json.dumps(self.inventory)}])
            for source, configured in [("gatus", self.settings.gatus_url or self.settings.gatus_config),
                                       ("kuma", getattr(self.settings, "kuma_host", ""))]:
                if configured and time.monotonic() - self.last_sync.get(source, 0) > 300:
                    try:
                        await self.sync(source)
                    except Exception:
                        # DB driver exception strings can include usernames; keep public errors generic.
                        setattr(self, f"{source}_warnings", [f"{source.title()} sync failed; verify connection and configuration"])
                        log.warning("%s import failed", source)
            sites = [s for s in await self.store.sites() if site_enabled(s)]
            ifaces = [i for i in self.inventory["interfaces"] if i["probeable"]]
            semaphore = asyncio.Semaphore(self.settings.concurrency)

            async def check(site, iface):
                async with semaphore:
                    # Hard wall-time stops stuck browser/proxy/trace tasks.
                    try:
                        result = await asyncio.wait_for(run_probe(site, iface, self.settings),
                                                        self.settings.timeout * 2 + 60)
                    except Exception as exc:
                        result = {"at": stamp(), "site_id": site["id"], "interface": iface["interface"],
                                  "status": "error", "errors": [{"stage": "worker", "message": str(exc)[:500]}]}
                    await self.store.insert("checks", [{"at": result["at"], "site_id": result["site_id"],
                        "interface": result["interface"], "status": result["status"], "payload": json.dumps(result)}])
            # A fixed number of workers avoids allocating one task per site × interface.
            jobs = iter((s, i) for s in sites for i in ifaces)

            async def consumer():
                for site, iface in jobs:
                    await check(site, iface)
            await asyncio.gather(*(consumer() for _ in range(self.settings.concurrency)))
            self.last_cycle, self.error = stamp(), None
        finally:
            self.running = False

    async def loop(self):
        while True:
            self.wake.clear()
            try:
                await self.cycle()
            except asyncio.CancelledError:
                raise
            except Exception as exc:
                self.error = type(exc).__name__
                log.exception("Monitoring cycle failed")
            try:
                await asyncio.wait_for(self.wake.wait(), self.settings.interval)
            except TimeoutError:
                pass
