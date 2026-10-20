"""Linux inventory and IPv4 sockets pinned to the selected output device."""
import asyncio
import ipaddress
import json
import re
import socket
from pathlib import Path

import dns.flags
import dns.message
import dns.query
import dns.rcode
import dns.rdatatype


async def command(*args, timeout=10):
    proc = await asyncio.create_subprocess_exec(*args, stdout=asyncio.subprocess.PIPE,
                                                stderr=asyncio.subprocess.PIPE)
    try:
        out, err = await asyncio.wait_for(proc.communicate(), timeout)
    except BaseException:
        proc.kill()
        await proc.communicate()
        raise
    if proc.returncode:
        raise RuntimeError(err.decode(errors="replace")[:500])
    return out.decode(errors="replace")


def inventory_from_ip(links, routes, include=()):
    inventory = []
    for link in links:
        name = link["ifname"]
        if name == "lo" or (include and name not in include):
            continue
        info = link.get("linkinfo", {})
        ipv4 = [f"{a['local']}/{a['prefixlen']}" for a in link.get("addr_info", [])
                if a["family"] == "inet"]
        kind = info.get("info_kind", "physical")
        inventory.append({
            "interface": name, "ifindex": link["ifindex"], "kind": kind,
            "vlan_id": info.get("info_data", {}).get("id") if kind == "vlan" else None,
            "parent": link.get("link"), "master": link.get("master"),
            "mac": link.get("address"), "mtu": link.get("mtu"),
            "state": link.get("operstate", "UNKNOWN"), "addresses": ipv4,
            "ipv6": [f"{a['local']}/{a['prefixlen']}" for a in link.get("addr_info", [])
                     if a["family"] == "inet6"],
            "routes": [r for r in routes if r.get("dev") == name],
            "probeable": bool(ipv4) and "UP" in link.get("flags", []) and not link.get("master"),
        })
    return inventory


async def discover(include=()):
    links, routes = await asyncio.gather(command("ip", "-j", "-d", "address", "show"),
                                         command("ip", "-j", "route", "show", "table", "all"))
    items = inventory_from_ip(json.loads(links), json.loads(routes), include)
    try:
        bridge = json.loads(await command("bridge", "-j", "vlan", "show"))
    except (RuntimeError, FileNotFoundError):
        bridge = []
    return {"interfaces": items, "bridge_vlans": bridge}


def bound_socket(interface, source, socktype=socket.SOCK_STREAM):
    if not re.fullmatch(r"[a-zA-Z0-9_.:@-]{1,15}", interface):
        raise ValueError("Invalid interface")
    sock = socket.socket(socket.AF_INET, socktype)
    try:
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_BINDTODEVICE, interface.encode() + b"\0")
        sock.bind((source, 0))
        sock.settimeout(5)
        return sock
    except BaseException:
        sock.close()
        raise


def resolvers():
    return [line.split()[1] for line in Path("/etc/resolv.conf").read_text().splitlines()
            if line.startswith("nameserver ") and ":" not in line.split()[1]]


def resolve(host, interface, source, servers):
    try:
        ip = ipaddress.ip_address(host)
        if ip.version != 4:
            raise ValueError("IPv6 probes are not supported in v0.1")
        return {"addresses": [str(ip)], "answers": [], "server": None}
    except ValueError:
        if ":" in host:
            raise ValueError("IPv6 probes are not supported in v0.1") from None
    answers, errors = [], []
    for server in servers:
        try:
            query = dns.message.make_query(host, "A")
            with bound_socket(interface, source, socket.SOCK_DGRAM) as sock:
                sock.setblocking(False)
                response = dns.query.udp(query, server, timeout=5, sock=sock)
            if response.flags & dns.flags.TC:
                with bound_socket(interface, source) as sock:
                    sock.connect((server, 53))
                    sock.setblocking(False)
                    response = dns.query.tcp(query, server, timeout=5, sock=sock)
            if response.rcode() != dns.rcode.NOERROR:
                raise RuntimeError(dns.rcode.to_text(response.rcode()))
            addresses = []
            for rrset in response.answer:
                answers.append(rrset.to_text())
                if rrset.rdtype == dns.rdatatype.A:
                    addresses.extend(str(a) for a in rrset)
            if not addresses:
                raise RuntimeError("No A record returned")
            return {"addresses": addresses, "answers": answers, "server": server}
        except Exception as exc:
            errors.append(f"{server}: {exc}")
    raise RuntimeError("DNS failed: " + "; ".join(errors or ["No IPv4 resolver configured"]))
