import asyncio
import json
import ssl
import time
from datetime import datetime, timezone
from urllib.parse import urlsplit

from app.network import bound_socket, command, resolve, resolvers
from app.proxy import proxy
from app.store import stamp


def certificate_info(tls):
    from cryptography import x509
    from cryptography.hazmat.primitives import hashes
    cert = x509.load_der_x509_certificate(tls.getpeercert(binary_form=True))
    try:
        san = cert.extensions.get_extension_for_class(x509.SubjectAlternativeName).value
        names = [str(v.value) for v in san]
    except x509.ExtensionNotFound:
        names = []
    return {"version": tls.version(), "cipher": tls.cipher()[0],
            "subject": cert.subject.rfc4514_string(), "issuer": cert.issuer.rfc4514_string(),
            "san": names, "not_before": cert.not_valid_before_utc.isoformat(),
            "not_after": cert.not_valid_after_utc.isoformat(),
            "expires_days": (cert.not_valid_after_utc - datetime.now(timezone.utc)).days,
            "sha256": cert.fingerprint(hashes.SHA256()).hex()}


def tcp_tls(host, ip, port, interface, source, https):
    data = {}
    with bound_socket(interface, source) as sock:
        start = time.perf_counter()
        sock.connect((ip, port))
        data["tcp_ms"] = round((time.perf_counter() - start) * 1000, 2)
        data["remote_ip"] = ip
        if https:
            start = time.perf_counter()
            try:
                with ssl.create_default_context().wrap_socket(sock, server_hostname=host) as tls:
                    data["tls"] = {"valid": True, **certificate_info(tls),
                                   "handshake_ms": round((time.perf_counter() - start) * 1000, 2)}
            except ssl.SSLError as exc:
                data["tls"] = {"valid": False, "error": str(exc)}
                if isinstance(exc, ssl.SSLCertVerificationError):
                    # A second connection collects the invalid leaf certificate for diagnostics only.
                    # HTTP and browser validation always remain strict.
                    context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
                    context.check_hostname = False
                    context.verify_mode = ssl.CERT_NONE
                    try:
                        with bound_socket(interface, source) as diagnostic:
                            diagnostic.connect((ip, port))
                            with context.wrap_socket(diagnostic, server_hostname=host) as tls:
                                data["tls"].update(certificate_info(tls))
                    except Exception as diagnostic_error:
                        data["tls"]["diagnostic_error"] = str(diagnostic_error)[:500]
    return data


async def browser_check(proxy_url, url, timeout):
    from playwright.async_api import async_playwright
    async with async_playwright() as p:
        browser = await p.chromium.launch(headless=True,
            args=["--disable-quic", "--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1", "--force-webrtc-ip-handling-policy=disable_non_proxied_udp"],
            proxy={"server": proxy_url, "bypass": "<-loopback>"})
        context = await browser.new_context(ignore_https_errors=False, service_workers="block")
        failed, bad = [], []
        page = await context.new_page()
        page.on("requestfailed", lambda r: failed.append({"url": r.url, "error": r.failure}))
        page.on("response", lambda r: bad.append({"url": r.url, "status": r.status}) if r.status >= 400 else None)
        start = time.perf_counter()
        try:
            response = await page.goto(url, wait_until="load", timeout=timeout * 1000)
            timing = await page.evaluate("""() => {
              const n = performance.getEntriesByType('navigation')[0];
              return {dom_ms: n.domContentLoadedEventEnd, load_ms: n.loadEventEnd,
                      ttfb_ms: n.responseStart, transfer_bytes: n.transferSize,
                      resources: performance.getEntriesByType('resource').length};
            }""")
            return {"ok": bool(response) and response.status < 400 and not failed and not bad,
                    "status": response.status if response else None, "final_url": page.url,
                    "elapsed_ms": round((time.perf_counter() - start) * 1000, 2),
                    **timing, "failed_requests": failed[:50], "bad_responses": bad[:50]}
        except Exception as exc:
            return {"ok": False, "error": str(exc)[:1500], "failed_requests": failed[:50],
                    "bad_responses": bad[:50]}
        finally:
            await context.close()
            await browser.close()


async def run_probe(site, iface, settings):
    result = {"at": stamp(), "site_id": site["id"], "interface": iface["interface"],
              "vlan_id": iface["vlan_id"], "status": "down", "errors": []}
    name = iface["interface"]
    source = iface["addresses"][0].split("/")[0]
    servers = settings.dns_servers.get(name, resolvers())
    parsed = urlsplit(site["url"])
    host, port = parsed.hostname, parsed.port or (443 if parsed.scheme == "https" else 80)
    start = time.perf_counter()
    try:
        result["dns"] = await asyncio.to_thread(resolve, host, name, source, servers)
        result["dns"]["ms"] = round((time.perf_counter() - start) * 1000, 2)
        ip = result["dns"]["addresses"][0]
    except Exception as exc:
        result["errors"].append({"stage": "dns", "message": str(exc)[:1000]})
        return result
    for candidate in result["dns"]["addresses"]:
        try:
            result.update(await asyncio.to_thread(tcp_tls, host, candidate, port, name, source, parsed.scheme == "https"))
            ip = candidate
            break
        except Exception as exc:
            result["errors"].append({"stage": "tcp", "ip": candidate, "message": str(exc)[:1000]})
    try:
        async with proxy(name, source, servers) as (proxy_url, handler):
            # --disable prevents ~/.curlrc; env proxy bypass is explicitly disabled.
            raw = await command("curl", "--disable", "--silent", "--show-error", "--location",
                "--max-redirs", "5", "--max-time", str(settings.timeout),
                "--max-filesize", "10485760", "--proto", "=http,https", "--proto-redir", "=http,https",
                "--proxy", proxy_url, "--noproxy", "", "--output", "/dev/null", "--write-out", "%{json}",
                "--", site["url"], timeout=settings.timeout + 5)
            http = json.loads(raw)
            result["http"] = {"code": http["http_code"], "url": http["url_effective"],
                              "ttfb_ms": round(http["time_starttransfer"] * 1000, 2),
                              "total_ms": round(http["time_total"] * 1000, 2),
                              "bytes": http["size_download"], "redirects": http["num_redirects"]}
            if 200 <= http["http_code"] < 400 and result.get("tcp_ms") is not None:
                result["status"] = "up"
            if settings.browser:
                result["browser"] = await browser_check(proxy_url, site["url"], settings.timeout)
                if not result["browser"]["ok"] and result["status"] == "up":
                    result["status"] = "degraded"
            if handler.failures:
                result["proxy_errors"] = handler.failures[:20]
    except Exception as exc:
        result["errors"].append({"stage": "http", "message": str(exc)[:1000]})
    if result.get("tls", {}).get("valid") is False:
        result["status"] = "down"
    try:
        result["trace"] = {"output": await command("traceroute", "-n", "-i", name, "-s", source,
            "-m", "12", "-q", "1", "-w", "1", ip, timeout=16), "method": "udp"}
    except Exception as exc:
        result["trace"] = {"error": str(exc)[:1000]}
    return result
