# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tenant-saas ./cmd/server
# Seed the local-upload directory so a fresh named volume inherits nonroot
# ownership on first mount. Without this, /data/uploads is created root-owned
# and the nonroot runtime cannot write uploads when STORAGE_DRIVER=local.
RUN mkdir -p /out/data/uploads

# The binary embeds the web dashboard; no Node runtime or separate static host is required.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/tenant-saas /tenant-saas
COPY --from=build --chown=nonroot:nonroot /out/data/uploads /data/uploads

ENV APP_ENV=production
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/tenant-saas"]
