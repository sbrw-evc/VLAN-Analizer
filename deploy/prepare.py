"""Create only the ClickHouse bootstrap secret before the first Compose start."""
import os
import secrets
from pathlib import Path

path = Path("secrets")
path.mkdir(exist_ok=True, mode=0o700)
file = path / "clickhouse_password"
fd = os.open(file, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(fd, "w") as stream:
    stream.write(secrets.token_urlsafe(48))
print("ClickHouse bootstrap credential prepared.")
