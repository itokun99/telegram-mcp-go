# syntax=docker/dockerfile:1

# =============================================================================
# Build stage: compile the static binary
# =============================================================================
FROM golang:1.27 AS builder

# Stamped into internal/mcpserver.Version; override with
# --build-arg VERSION=$(git describe --tags).
ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH

WORKDIR /src

# Modules first so source-only edits keep the cached download layer.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

# CGO_ENABLED=0 is what makes the distroless runtime viable: the binary links
# no libc. -trimpath keeps local filesystem paths out of the built artifact.
RUN CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" go build \
        -trimpath \
        -ldflags "-s -w -X github.com/itokun99/telegram-mcp-go/internal/mcpserver.Version=${VERSION}" \
        -o /out/telegram-mcp-go \
        ./cmd/telegram-mcp-go

# No shell in the runtime stage, so the writable dirs and their ownership
# (distroless nonroot = 65532:65532) have to be baked in here.
RUN mkdir -p /out/data/transcripts /out/telegram_sessions && \
    chown -R 65532:65532 /out

# =============================================================================
# Production stage: distroless static, non-root
# =============================================================================
# distroless/static, not scratch: scratch ships no CA bundle and gogram would
# fail TLS verification against Telegram.
FROM gcr.io/distroless/static-debian12:nonroot AS production

COPY --from=builder /out/telegram-mcp-go /usr/local/bin/telegram-mcp-go

WORKDIR /app

COPY --from=builder --chown=65532:65532 /out/data /app/data
COPY --from=builder --chown=65532:65532 /out/telegram_sessions /app/telegram_sessions

# Runtime environment variables (to be supplied at run/container start)
ENV TELEGRAM_API_ID="" \
    TELEGRAM_API_HASH="" \
    TELEGRAM_SESSION_NAME="telegram_mcp_session" \
    TELEGRAM_SESSION_STRING="" \
    MCP_TRANSPORT="stdio" \
    MCP_HOST="127.0.0.1" \
    MCP_PORT="8765" \
    TELEGRAM_TRANSCRIPT_CACHE_DIR="/app/data/transcripts"

EXPOSE 8765

USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/telegram-mcp-go"]