# ============================================================
# Minimal runtime image - download pre-built binary
# ============================================================
FROM alpine:3.23

ARG VERSION=v0.2.3

RUN apk add --no-cache ca-certificates tzdata curl

# Determine architecture and download binary
RUN ARCH=$(uname -m) && \
    case "$ARCH" in \
      x86_64) GOARCH="x86_64" ;; \
      aarch64) GOARCH="arm64" ;; \
      armv7l) GOARCH="armv7" ;; \
      armv6l) GOARCH="armv6" ;; \
      riscv64) GOARCH="riscv64" ;; \
      loongarch64) GOARCH="loong64" ;; \
      *) echo "Unsupported architecture: $ARCH"; exit 1 ;; \
    esac && \
    curl -L -o /tmp/picoclaw.tar.gz \
      "https://github.com/j0904/picoclaw/releases/download/${VERSION}/picoclaw_Linux_${GOARCH}.tar.gz" && \
    tar -xzf /tmp/picoclaw.tar.gz -C /usr/local/bin picoclaw && \
    rm /tmp/picoclaw.tar.gz && \
    chmod +x /usr/local/bin/picoclaw

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q --spider http://localhost:18790/health || exit 1

# Create non-root user and group
RUN addgroup -g 1000 picoclaw && \
    adduser -D -u 1000 -G picoclaw picoclaw

# Switch to non-root user
USER picoclaw

# Run onboard to create initial directories and config
RUN /usr/local/bin/picoclaw onboard

ENTRYPOINT ["picoclaw"]
CMD ["gateway"]
