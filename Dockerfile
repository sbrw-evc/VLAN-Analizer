FROM python:3.12-slim-bookworm
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PLAYWRIGHT_BROWSERS_PATH=/opt/browsers
WORKDIR /opt/vlan-analizer
RUN apt-get update && apt-get install -y --no-install-recommends iproute2 curl traceroute ca-certificates tini && rm -rf /var/lib/apt/lists/*
COPY requirements.lock pyproject.toml ./
RUN pip install --no-cache-dir -r requirements.lock && playwright install --with-deps chromium && rm -rf /var/lib/apt/lists/*
COPY app ./app
COPY deploy ./deploy
RUN pip install --no-deps .
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["uvicorn", "app.main:app", "--host", "127.0.0.1", "--port", "8000", "--workers", "1", "--no-proxy-headers", "--no-access-log"]
