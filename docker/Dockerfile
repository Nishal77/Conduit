# syntax=docker/dockerfile:1

# ---- builder ---------------------------------------------------------
# Compiles a static, CGO-free binary so the final stage can be a
# distroless image with no shell, package manager, or libc to patch.
FROM golang:1.23-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILT=unknown

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.built=${BUILT}" \
    -o /out/conduit \
    ./cmd/conduit

# ---- migrator ----------------------------------------------------------
# A separate stage (not image) so `docker build --target migrator` can run
# `conduit migrate` against a database without shipping the full runtime
# image's distroless base — useful in CI before the proxy container starts.
FROM builder AS migrator
ENTRYPOINT ["/out/conduit", "migrate"]

# ---- final ---------------------------------------------------------
# distroless/static: no shell, no package manager — just the binary and CA
# certificates (needed for TLS to upstream MCP servers and OTLP exporters).
FROM gcr.io/distroless/static-debian12:nonroot AS final

COPY --from=builder /out/conduit /usr/local/bin/conduit

USER nonroot:nonroot

EXPOSE 8080 8081 9090

ENTRYPOINT ["/usr/local/bin/conduit"]
CMD ["proxy", "start"]
