FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY VERSION ./
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/sbrw-evc/VLAN-Analizer/internal/app.Version=$(cat VERSION)" -o /vlan-analizer ./cmd/vlan-analizer

FROM debian:bookworm-slim
LABEL org.opencontainers.image.source="https://github.com/sbrw-evc/VLAN-Analizer"
RUN apt-get update && apt-get install -y --no-install-recommends chromium iproute2 traceroute ca-certificates tini && rm -rf /var/lib/apt/lists/*
COPY --from=build /vlan-analizer /usr/local/bin/vlan-analizer
ENV APP_DATA_DIR=/var/lib/vlan-analizer
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/vlan-analizer"]
CMD ["serve"]
