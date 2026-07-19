# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tenant-saas ./cmd/server

# The binary embeds the web dashboard; no Node runtime or separate static host is required.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/tenant-saas /tenant-saas

ENV APP_ENV=production
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/tenant-saas"]
