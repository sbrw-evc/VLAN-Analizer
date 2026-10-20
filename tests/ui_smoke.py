"""Browser UI smoke test with explicit synthetic API fixtures, no production services."""
import json
import subprocess
import sys
from pathlib import Path

from playwright.sync_api import sync_playwright

root = Path(__file__).parents[1]
server = subprocess.Popen([sys.executable, "-m", "http.server", "8765", "--bind", "127.0.0.1", "--directory", str(root / "app")],
                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
fixture = {
    "sites": [{"id": "portal", "name": "Customer portal", "url": "https://portal.example.com", "source": "manual", "enabled": 1}],
    "interfaces": [{"interface": "eno1.20", "vlan_id": 20, "probeable": True, "state": "UP", "kind": "vlan", "parent": "eno1",
                    "master": None, "addresses": ["192.0.2.10/24"], "ipv6": [], "mac": "02:00:00:00:00:20", "mtu": 1500, "routes": []}],
    "bridge_vlans": [], "inventory_at": "2026-10-20T10:00:00Z", "results": [
        {"site_id": "portal", "interface": "eno1.20", "status": "up", "at": "2026-10-20T10:00:00Z", "http": {"total_ms": 82, "code": 200}}],
    "worker": {"running": False, "last_cycle": "2026-10-20T10:00:00Z", "error": None, "gatus_warnings": []}}
try:
    with sync_playwright() as p:
        browser = p.chromium.launch()
        page = browser.new_page(viewport={"width": 1440, "height": 1000})
        errors = []
        page.on("pageerror", lambda e: errors.append(str(e)))
        connections = {
            "gatus": {"enabled": False, "url": "", "config_path": "", "token_set": False},
            "kuma": {"enabled": False, "host": "", "port": 3306, "database": "kuma", "user": "vlan_reader", "tls": True, "ca": "", "instance": "default", "password_set": False},
            "entra": {"enabled": False, "tenant": "", "client_id": "", "client_secret_set": False, "redirect_uri": "http://127.0.0.1:8765/auth/entra/callback"}}
        def route(request):
            path = request.request.url.split("8765")[-1]
            payload = {"sub": "admin", "name": "Test admin", "role": "admin", "provider": "local"} if path == "/api/me" else fixture if path == "/api/dashboard" else [] if path.startswith("/api/history") else {"imported": 1, "retired": 0, "warnings": [], "ok": True}
            if path == "/api/setup/status":
                payload = {"required": False, "ready": True, "entra_enabled": False}
            if path.startswith("/api/settings/connections"):
                payload = connections
            request.fulfill(status=200, content_type="application/json", body=json.dumps(payload))
        page.route("**/api/**", route)
        page.goto("http://127.0.0.1:8765/static/index.html")
        page.get_by_text("Customer portal", exact=False).first.wait_for()
        assert page.locator("#site-count").inner_text() == "1"
        assert page.locator("#matrix .pill").inner_text() == "● Healthy"
        page.locator("#sync-kuma").click()
        page.get_by_text("Imported 1 sites.", exact=False).wait_for()
        page.locator("#matrix .pill").click()
        assert page.locator("#detail-dialog").is_visible()
        page.locator('[data-close="detail-dialog"]').click()
        page.get_by_role("button", name="+ Add website").click()
        page.locator('#site-form input[name="name"]').fill("Internal app")
        page.locator('#site-form input[name="url"]').fill("https://internal.example.com")
        page.get_by_role("button", name="Save website").click()
        page.locator('[data-tab="networks"]').click()
        assert "192.0.2.10/24" in page.locator("#network-cards").inner_text()
        page.locator('[data-tab="settings"]').click()
        page.locator('#gatus-form input[name="url"]').fill("https://status.example.com")
        page.get_by_role("button", name="Save Gatus", exact=True).click()
        page.get_by_text("Saved. Settings apply immediately.", exact=True).wait_for()
        page.locator('#user-form input[name="username"]').fill("reader")
        page.locator('#user-form input[name="password"]').fill("long-test-password")
        page.get_by_role("button", name="Save user").click()
        page.get_by_text("User saved.", exact=False).wait_for()
        page.set_viewport_size({"width": 390, "height": 844})
        page.locator('[data-tab="overview"]').click()
        assert page.evaluate("document.documentElement.scrollWidth <= innerWidth")
        assert not errors, errors
        wizard = browser.new_page(viewport={"width": 1200, "height": 1000})
        pending = {"required": True}
        def wizard_route(request):
            path = request.request.url.split("8765")[-1]
            if path == "/api/setup/status":
                payload = {"required": pending["required"], "ready": not pending["required"], "entra_enabled": False}
            elif path == "/api/setup/complete":
                body = request.request.post_data_json
                assert body["admin_username"] == "engineer" and not body["entra"]["enabled"]
                pending["required"] = False
                payload = {"ok": True, "ready": True}
            elif path == "/api/me":
                request.fulfill(status=401, content_type="application/json", body='{"detail":"Sign in"}')
                return
            else:
                payload = {"ok": True, "message": "Connection verified"}
            request.fulfill(status=200, content_type="application/json", body=json.dumps(payload))
        wizard.route("**/api/**", wizard_route)
        wizard.goto("http://127.0.0.1:8765/static/index.html")
        wizard.locator('#setup-form input[name="setup_token"]').fill("test-setup-token")
        wizard.locator('#setup-next').click()
        wizard.locator('#setup-form input[name="bao_token"]').fill("test-provision-token")
        wizard.locator('#setup-next').click()
        wizard.locator('#setup-form input[name="ch_password"]').fill("test-clickhouse-password")
        wizard.locator('#setup-next').click()
        wizard.locator('#setup-form input[name="admin_username"]').fill("engineer")
        wizard.locator('#setup-form input[name="admin_password"]').fill("long-admin-password")
        wizard.locator('#setup-form input[name="admin_confirm"]').fill("long-admin-password")
        wizard.locator('#setup-next').click()
        wizard.locator('#setup-next').click()
        wizard.locator('#login').wait_for(state="visible")
        assert not pending["required"]
        wizard.close()
        browser.close()
finally:
    server.terminate()
    server.wait(timeout=5)
