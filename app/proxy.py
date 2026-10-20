"""Ephemeral browser/curl proxy: DNS and every upstream socket stay on one interface."""
import asyncio
from contextlib import asynccontextmanager
from urllib.parse import urlsplit

from app.network import bound_socket, resolve


class BoundProxy:
    def __init__(self, interface, source, servers):
        self.interface, self.source, self.servers = interface, source, servers
        self.tasks = set()
        self.failures = []

    async def handle(self, reader, writer):
        task = asyncio.current_task()
        self.tasks.add(task)
        upstream = None
        try:
            header = await asyncio.wait_for(reader.readuntil(b"\r\n\r\n"), 10)
            lines = header.decode("latin1").split("\r\n")
            method, target, version = lines[0].split(" ", 2)
            if method == "CONNECT":
                host, port = target.rsplit(":", 1)
                port = int(port)
            else:
                url = urlsplit(target)
                if url.scheme != "http" or not url.hostname:
                    raise ValueError("Only HTTP and CONNECT supported")
                host, port = url.hostname, url.port or 80
            if port not in {80, 443, 8080, 8443}:
                raise ValueError("Proxy port is outside the allowed web ports")
            resolved = await asyncio.to_thread(resolve, host, self.interface, self.source, self.servers)
            last_error = None
            for ip in resolved["addresses"]:
                sock = bound_socket(self.interface, self.source)
                try:
                    sock.setblocking(False)
                    await asyncio.wait_for(asyncio.get_running_loop().sock_connect(sock, (ip, port)), 8)
                    remote_reader, upstream = await asyncio.open_connection(sock=sock)
                    break
                except asyncio.CancelledError:
                    sock.close()
                    raise
                except Exception as exc:
                    sock.close()
                    last_error = exc
            if upstream is None:
                raise RuntimeError(f"All resolved IPv4 addresses failed: {last_error}")
            if method == "CONNECT":
                writer.write(b"HTTP/1.1 200 Connection Established\r\n\r\n")
            else:
                path = url.path or "/"
                if url.query:
                    path += "?" + url.query
                filtered = [line for line in lines[1:] if line and
                            not line.lower().startswith(("proxy-", "connection:"))]
                upstream.write((f"{method} {path} {version}\r\n" + "\r\n".join(filtered) +
                                "\r\nConnection: close\r\n\r\n").encode("latin1"))
                await upstream.drain()
            await writer.drain()

            async def pipe(src, dst):
                while chunk := await src.read(65536):
                    dst.write(chunk)
                    await dst.drain()
            a = asyncio.create_task(pipe(reader, upstream))
            b = asyncio.create_task(pipe(remote_reader, writer))
            try:
                await asyncio.wait([a, b], timeout=120, return_when=asyncio.FIRST_COMPLETED)
            finally:
                a.cancel()
                b.cancel()
                await asyncio.gather(a, b, return_exceptions=True)
        except asyncio.CancelledError:
            raise
        except Exception as exc:
            self.failures.append(str(exc)[:200])
            try:
                writer.write(b"HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
                await writer.drain()
            except Exception:
                pass
        finally:
            writer.close()
            if upstream:
                upstream.close()
            self.tasks.discard(task)


@asynccontextmanager
async def proxy(interface, source, servers):
    handler = BoundProxy(interface, source, servers)
    server = await asyncio.start_server(handler.handle, "127.0.0.1", 0, limit=65536)
    try:
        yield f"http://127.0.0.1:{server.sockets[0].getsockname()[1]}", handler
    finally:
        server.close()
        await server.wait_closed()
        pending = list(handler.tasks)
        for task in pending:
            task.cancel()
        await asyncio.gather(*pending, return_exceptions=True)
