# syntax=docker/dockerfile:1.6

# ---- build stage --------------------------------------------------------
FROM golang:1.25-alpine AS build

WORKDIR /src

# Cache deps separately from source for faster rebuilds.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Static binary so we can ship on a scratch base. CGO is off by default in
# alpine, but make it explicit.
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64
RUN go build -trimpath -ldflags="-s -w" -o /out/box-mcp ./cmd/box-mcp

# ---- runtime stage ------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot

# Persistent box data lives here. fly.toml mounts the Volume at /data.
ENV BOX_HOME=/data
WORKDIR /data

# distroless ships /etc/passwd with a `nonroot` UID 65532; reuse it so the
# container does not run as root.
USER nonroot:nonroot

COPY --from=build --chown=nonroot:nonroot /out/box-mcp /usr/local/bin/box-mcp

EXPOSE 8080

# Streamable-HTTP listen address. Fly injects PORT, but we set --http
# explicitly so local `docker run` works the same way.
ENTRYPOINT ["/usr/local/bin/box-mcp", "--http=:8080"]
