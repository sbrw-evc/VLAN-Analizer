import asyncio
import socket
from unittest.mock import Mock

import dns.message
import dns.rrset
import pytest

from app.network import bound_socket, command, discover, inventory_from_ip, resolve
from app.proxy import proxy


def test_inventory_never_guesses_vlan_one_and_excludes_l2_slaves():
    items = inventory_from_ip([
        {"ifname": "eth0", "ifindex": 2, "flags": ["UP"], "addr_info": [{"family": "inet", "local": "10.0.0.2", "prefixlen": 24}]},
        {"ifname": "eth0.20", "ifindex": 3, "flags": ["UP"], "master": "br20",
         "linkinfo": {"info_kind": "vlan", "info_data": {"id": 20}},
         "addr_info": [{"family": "inet", "local": "10.20.0.2", "prefixlen": 24}]},
        {"ifname": "eth0.30", "ifindex": 4, "flags": [], "addr_info": [],
         "linkinfo": {"info_kind": "vlan", "info_data": {"id": 30}}},
        {"ifname": "lo", "ifindex": 1},
    ], [{"dev": "eth0", "dst": "default", "gateway": "10.0.0.1"}])
    assert len(items) == 3
    assert items[0]["vlan_id"] is None and items[0]["probeable"]
    assert items[1]["vlan_id"] == 20 and not items[1]["probeable"]
    assert not items[2]["probeable"]
    assert items[0]["routes"][0]["gateway"] == "10.0.0.1"
    assert len(inventory_from_ip([{"ifname": "eth0", "ifindex": 2}], [], {"eth1"})) == 0


def test_socket_binding_closes_on_permission_failure(monkeypatch):
    sock = Mock()
    sock.setsockopt.side_effect = PermissionError("no capability")
    monkeypatch.setattr(socket, "socket", lambda *a: sock)
    with pytest.raises(PermissionError):
        bound_socket("eth0.10", "10.0.0.2")
    sock.close.assert_called_once()
    sock.connect.assert_not_called()


def test_dns_uses_bound_socket_and_falls_back_only_to_configured_server(monkeypatch):
    sock = Mock()
    sock.__enter__ = Mock(return_value=sock)
    sock.__exit__ = Mock(return_value=False)
    calls = []
    monkeypatch.setattr("app.network.bound_socket", lambda *a: calls.append(a) or sock)
    query = dns.message.make_query("portal.example", "A")
    response = dns.message.make_response(query)
    response.answer.append(dns.rrset.from_text("portal.example", 60, "IN", "A", "192.0.2.12"))
    def udp(q, where, **kwargs):
        assert kwargs["sock"] is sock
        if where == "192.0.2.1":
            raise TimeoutError("unreachable")
        return response
    monkeypatch.setattr("dns.query.udp", udp)
    result = resolve("portal.example", "eth0.20", "192.0.2.2", ["192.0.2.1", "192.0.2.3"])
    assert result["addresses"] == ["192.0.2.12"]
    assert result["server"] == "192.0.2.3"
    assert all(c[:2] == ("eth0.20", "192.0.2.2") for c in calls)


def test_ip_literal_and_ipv6_rejection():
    assert resolve("192.0.2.1", "eth0", "192.0.2.2", [])["addresses"] == ["192.0.2.1"]
    with pytest.raises(ValueError, match="IPv6"):
        resolve("::1", "eth0", "192.0.2.2", [])


async def test_subprocess_timeout_kills_child():
    with pytest.raises(TimeoutError):
        await command("python", "-c", "import time;time.sleep(20)", timeout=0.05)


async def test_discovery_reports_netlink_failure_instead_of_empty_success(monkeypatch):
    async def broken(*a, **k):
        raise RuntimeError("netlink denied")
    monkeypatch.setattr("app.network.command", broken)
    with pytest.raises(RuntimeError, match="netlink"):
        await discover()


async def test_real_bound_proxy_http_and_connect():
    seen = []
    async def upstream(reader, writer):
        seen.append(await reader.readuntil(b"\r\n\r\n"))
        writer.write(b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nOK")
        await writer.drain()
        writer.close()
    try:
        server = await asyncio.start_server(upstream, "127.0.0.1", 8080)
    except OSError:
        pytest.skip("Loopback port 8080 unavailable")
    try:
        async with proxy("lo", "127.0.0.1", []) as (url, handler):
            for connect in [False, True]:
                r, w = await asyncio.open_connection("127.0.0.1", int(url.rsplit(":", 1)[1]))
                if connect:
                    w.write(b"CONNECT 127.0.0.1:8080 HTTP/1.1\r\nHost: 127.0.0.1:8080\r\n\r\n")
                    await w.drain()
                    assert b"200 Connection Established" in await r.readuntil(b"\r\n\r\n")
                    w.write(b"GET /secure HTTP/1.1\r\nHost: localhost\r\n\r\n")
                else:
                    w.write(b"GET http://127.0.0.1:8080/plain?q=1 HTTP/1.1\r\nHost: localhost\r\nProxy-Authorization: secret\r\n\r\n")
                await w.drain()
                assert (await asyncio.wait_for(r.read(), 5)).endswith(b"OK")
                w.close()
            assert not handler.failures
        assert seen[0].startswith(b"GET /plain?q=1 HTTP/1.1")
        assert b"Proxy-Authorization" not in seen[0]
        assert seen[1].startswith(b"GET /secure")
    finally:
        server.close()
        await server.wait_closed()
