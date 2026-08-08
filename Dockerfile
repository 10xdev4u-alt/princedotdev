# Build stage: static Go binary (CGO off → fully static, no libc deps).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/draftdeck ./cmd/draftdeck

# Runtime stage: alpine (small, includes wget for the healthcheck).
FROM alpine:3.20
RUN apk add --no-cache ca-certificates wget \
 && addgroup -S draftdeck && adduser -S -G draftdeck draftdeck
COPY --from=build /out/draftdeck /usr/local/bin/draftdeck
# Pre-create and own /data so a fresh named volume inherits writable
# ownership for the non-root user (Docker copies image dir metadata into
# new volumes).
RUN mkdir -p /data && chown -R draftdeck:draftdeck /data
USER draftdeck
ENV DATA_DIR=/data \
    PORT=8080 \
    PUBLIC_BASE_URL=http://localhost:8080 \
    STORAGE_BUDGET_BYTES=5368709120
VOLUME ["/data"]
EXPOSE 8080
# Self-describing healthcheck: the image alone reports liveness.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://localhost:8080/healthz || exit 1
ENTRYPOINT ["draftdeck"]
