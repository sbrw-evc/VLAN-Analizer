import asyncio
import datetime
import ssl
from types import SimpleNamespace
from unittest.mock import AsyncMock

from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import rsa
from cryptography.x509.oid import NameOID

from app.probes import run_probe, tcp_tls


async def test_dns_failure_never_uses_default_route(monkeypatch):
    def failed(*a):
        raise RuntimeError("VLAN resolver unreachable")
    monkeypatch.setattr("app.probes.resolve", failed)
    command = AsyncMock()
    monkeypatch.setattr("app.probes.command", command)
    result = await run_probe({"id": "site", "url": "https://portal.example"},
        {"interface": "eth0.20", "vlan_id": 20, "addresses": ["192.0.2.2/24"]},
        SimpleNamespace(dns_servers={"eth0.20": ["192.0.2.1"]}, timeout=5, browser=False))
    assert result["status"] == "down"
    assert result["errors"][0]["stage"] == "dns"
    command.assert_not_called()


async def test_invalid_tls_collects_leaf_without_marking_valid(tmp_path):
    key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    subject = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, "localhost")])
    now = datetime.datetime.now(datetime.timezone.utc)
    cert = (x509.CertificateBuilder().subject_name(subject).issuer_name(subject).public_key(key.public_key())
            .serial_number(x509.random_serial_number()).not_valid_before(now-datetime.timedelta(days=1))
            .not_valid_after(now+datetime.timedelta(days=30))
            .add_extension(x509.SubjectAlternativeName([x509.DNSName("localhost")]), critical=False)
            .sign(key, hashes.SHA256()))
    cert_file, key_file = tmp_path / "cert.pem", tmp_path / "key.pem"
    cert_file.write_bytes(cert.public_bytes(serialization.Encoding.PEM))
    key_file.write_bytes(key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8,
                                          serialization.NoEncryption()))
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(cert_file, key_file)
    async def close(reader, writer):
        writer.close()
    server = await asyncio.start_server(close, "127.0.0.1", 0, ssl=context)
    try:
        port = server.sockets[0].getsockname()[1]
        result = await asyncio.to_thread(tcp_tls, "localhost", "127.0.0.1", port, "lo", "127.0.0.1", True)
        assert result["tls"]["valid"] is False
        assert result["tls"]["san"] == ["localhost"]
        assert result["tls"]["expires_days"] in {29,30}
        assert result["tls"]["sha256"] == cert.fingerprint(hashes.SHA256()).hex()
    finally:
        server.close()
        await server.wait_closed()
