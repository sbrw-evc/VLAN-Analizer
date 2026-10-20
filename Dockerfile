# 1. The web app (React + Vite) → web/dist, the same files for every platform.
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# 2. The Go binary embeds web/dist and is cross-compiled for the target platform.
FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY VERSION ./
COPY cmd ./cmd
COPY internal ./internal
COPY web/embed.go ./web/
COPY --from=web /src/web/dist ./web/dist
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X github.com/sbrw-evc/VLAN-Analizer/internal/app.Version=$(cat VERSION)" -o /vlan-analizer ./cmd/vlan-analizer

# 3. Runtime: Chromium and network tools for probes; nsenter (util-linux) lets netagent use the host's own tools.
FROM debian:bookworm-slim
LABEL org.opencontainers.image.source="https://github.com/sbrw-evc/VLAN-Analizer"
RUN apt-get update && apt-get install -y --no-install-recommends chromium iproute2 traceroute ca-certificates tini && rm -rf /var/lib/apt/lists/*
COPY --from=build /vlan-analizer /usr/local/bin/vlan-analizer
ENV APP_DATA_DIR=/var/lib/vlan-analizer
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/vlan-analizer"]
CMD ["serve"]
