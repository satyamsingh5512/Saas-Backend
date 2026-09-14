# syntax=docker/dockerfile:1
#
# Production image for the Tenant SaaS backend, tuned for a small VM
# (Oracle Cloud Always Free: 2 OCPU / 12 GB RAM).
#
# Shape of the image (why it looks like this):
#   - Multi-stage: the Go toolchain never ships; only a static binary does.
#   - Distroless static runtime: no shell, no package manager, no wget/curl --
#     the container cannot be used as a general-purpose host even if an
#     attacker gained exec. Liveness probing uses the binary itself
#     (`/tenant-saas -healthcheck`) instead of curl for the same reason.
#   - Non-root from first boot: USER nonroot + chowned upload seed, so a
#     container-escape bug does not hand over host root.
#   - Secrets never baked in: .dockerignore excludes every .env variant;
#     all configuration arrives as environment at run time.
#   - Read-only root filesystem is enforced in docker-compose.production.yml
#     (read_only: true + tmpfs /tmp); the only writable path is the
#     /data/uploads named volume. Nothing in the binary writes elsewhere:
#     uploads stage temp files inside STORAGE_ROOT itself.

FROM golang:1.26-alpine AS build
WORKDIR /src

# CA bundle + timezone data for the runtime stage (Resend/S3/OAuth all speak
# TLS; time formatting needs zoneinfo outside UTC-only usage).
RUN apk add --no-cache ca-certificates tzdata

COPY go.mod go.sum ./
RUN go mod download

COPY . ./
# Static binary: CGO off, symbol tables stripped. -trimpath keeps host paths
# out of the binary (reproducibility + smaller attack surface for info leak).
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tenant-saas ./cmd/server \
 && mkdir -p /out/data/uploads

# Seed the local-upload directory so a fresh named volume inherits nonroot
# ownership on first mount. Without this, /data/uploads is created root-owned
# and the nonroot runtime cannot write uploads when STORAGE_DRIVER=local.
FROM gcr.io/distroless/static-debian12:nonroot AS runtime
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /out/tenant-saas /tenant-saas
COPY --from=build --chown=nonroot:nonroot /out/data/uploads /data/uploads

LABEL org.opencontainers.image.title="tenant-saas-backend" \
      org.opencontainers.image.description="Multi-tenant SaaS backend (Go/Gin/GORM/PostgreSQL RLS)" \
      org.opencontainers.image.source="https://github.com/satym-in/tenant-saas-backend"

ENV APP_ENV=production \
    TZ=UTC
EXPOSE 8080
USER nonroot:nonroot

# Exec-form probe: no shell exists in this image, so SHELL-form (wget/curl)
# healthchecks cannot work. Interval/start-period give migrations + cold
# Postgres time to finish before the first probe counts.
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 \
  CMD ["/tenant-saas", "-healthcheck"]

ENTRYPOINT ["/tenant-saas"]
