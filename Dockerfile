# ── Stage 1: builder ─────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder

# git is needed by go mod download for some VCS dependencies
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /src

# Download dependencies first — cached unless go.mod / go.sum change
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build a statically-linked, stripped binary.
# CGO_ENABLED=0  → pure Go, no libc dependency → safe for scratch
# -trimpath      → remove local build paths from stack traces
# -s -w          → strip symbol table and DWARF debug info
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/dns-edge \
    ./cmd/dns-edge

# ── Stage 2: minimal runtime image ───────────────────────────────────────────
FROM scratch

# CA bundle — required for TLS connections to PostgreSQL and Nacos
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Time zone database — required if the PG DSN includes a timezone-aware column
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# The binary
COPY --from=builder /out/dns-edge /dns-edge

# DNS (UDP + TCP) and HTTP API
EXPOSE 53/udp
EXPOSE 53/tcp
EXPOSE 8080/tcp

ENTRYPOINT ["/dns-edge"]
# Corefile is mounted at runtime:
#   docker run -v /host/Corefile:/etc/dns-edge/Corefile dns-edge -config /etc/dns-edge/Corefile
CMD ["-config", "/etc/dns-edge/Corefile"]
