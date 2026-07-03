# Spec 17 — Kubernetes & Helm

> Phase: P7 | Files: `helm/`, `docker/Dockerfile`, `docker/docker-compose.yml`

---

## 1. Docker Multi-Stage Build — `docker/Dockerfile`

```dockerfile
# Stage 1: Go builder
FROM golang:1.23-alpine AS builder

RUN apk add --no-cache ca-certificates git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILT=unknown

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s \
              -X main.version=${VERSION} \
              -X main.commit=${COMMIT} \
              -X main.built=${BUILT}" \
    -o /bin/conduit ./cmd/conduit

# Stage 2: Migrator — carries migration files
FROM alpine:3.20 AS migrator
COPY --from=builder /bin/conduit /bin/conduit
COPY migrations/ /migrations/

# Stage 3: Final — distroless for minimal attack surface
FROM gcr.io/distroless/static-debian12:nonroot AS final

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /bin/conduit /bin/conduit

# Non-root user (distroless nonroot = UID 65532)
USER nonroot:nonroot

EXPOSE 8080 8081 9090

ENTRYPOINT ["/bin/conduit"]
CMD ["proxy", "start"]
```

Target image size: **<20MB**.

---

## 2. Docker Compose — `docker/docker-compose.yml`

```yaml
version: "3.9"

services:
  conduit:
    image: conduit/conduit:latest
    build:
      context: ..
      dockerfile: docker/Dockerfile
    ports:
      - "8080:8080"   # proxy
      - "8081:8081"   # management API
      - "9090:9090"   # metrics
    environment:
      DATABASE_URL: "postgres://conduit:conduit@postgres:5432/conduit?sslmode=disable"
      REDIS_URL: "redis://redis:6379/0"
      JWT_SECRET: "dev-secret-change-in-production-min-32-chars"
      CONDUIT_LOG_LEVEL: "info"
    volumes:
      - ../conduit.yaml:/etc/conduit/conduit.yaml:ro
    depends_on:
      postgres:
        condition: service_healthy
      redis:
        condition: service_healthy
    healthcheck:
      test: ["CMD", "/bin/conduit", "version"]
      interval: 10s
      timeout: 5s
      retries: 3
    restart: unless-stopped

  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: conduit
      POSTGRES_PASSWORD: conduit
      POSTGRES_DB: conduit
    ports:
      - "5432:5432"
    volumes:
      - postgres_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U conduit"]
      interval: 5s
      timeout: 3s
      retries: 5

  redis:
    image: redis:7-alpine
    command: redis-server --save "" --appendonly no
    ports:
      - "6379:6379"
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 3s
      retries: 5

volumes:
  postgres_data:
```

---

## 3. Helm Chart Structure — `helm/`

```
helm/
├── Chart.yaml
├── values.yaml
├── templates/
│   ├── _helpers.tpl
│   ├── deployment.yaml
│   ├── service.yaml
│   ├── ingress.yaml
│   ├── hpa.yaml
│   ├── pdb.yaml
│   ├── configmap.yaml
│   ├── secret.yaml
│   ├── serviceaccount.yaml
│   ├── networkpolicy.yaml
│   └── crds/
│       ├── conduit-tenant.yaml
│       └── conduit-server.yaml
```

### `helm/Chart.yaml`

```yaml
apiVersion: v2
name: conduit
description: Production MCP gateway — Kong for AI agents
type: application
version: 0.1.0
appVersion: "0.1.0"
keywords:
  - mcp
  - ai
  - gateway
  - proxy
home: https://github.com/conduit-oss/conduit
sources:
  - https://github.com/conduit-oss/conduit
maintainers:
  - name: conduit-oss
    url: https://github.com/conduit-oss
dependencies:
  - name: postgresql
    version: "15.x.x"
    repository: https://charts.bitnami.com/bitnami
    condition: postgresql.enabled
  - name: redis
    version: "19.x.x"
    repository: https://charts.bitnami.com/bitnami
    condition: redis.enabled
```

### `helm/values.yaml`

```yaml
# Default values for conduit Helm chart

replicaCount: 3

image:
  repository: ghcr.io/conduit-oss/conduit
  pullPolicy: IfNotPresent
  tag: ""  # defaults to chart appVersion

imagePullSecrets: []
nameOverride: ""
fullnameOverride: ""

serviceAccount:
  create: true
  name: ""
  annotations: {}

podAnnotations:
  prometheus.io/scrape: "true"
  prometheus.io/port: "9090"
  prometheus.io/path: "/metrics"

podSecurityContext:
  fsGroup: 65532
  runAsNonRoot: true

securityContext:
  readOnlyRootFilesystem: true
  runAsNonRoot: true
  runAsUser: 65532
  allowPrivilegeEscalation: false
  capabilities:
    drop: ["ALL"]

service:
  type: ClusterIP
  proxyPort: 8080
  managementPort: 8081
  metricsPort: 9090

ingress:
  enabled: false
  className: "nginx"
  annotations:
    nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"  # SSE keep-alive
    nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"
    nginx.ingress.kubernetes.io/proxy-buffering: "off"       # SSE: no buffering
  hosts:
    - host: conduit.example.com
      paths:
        - path: /mcp
          pathType: Prefix
          port: 8080
        - path: /api
          pathType: Prefix
          port: 8081

resources:
  requests:
    memory: "64Mi"
    cpu: "100m"
  limits:
    memory: "512Mi"
    cpu: "1000m"

autoscaling:
  enabled: true
  minReplicas: 2
  maxReplicas: 20
  targetCPUUtilizationPercentage: 60
  targetMemoryUtilizationPercentage: 70

podDisruptionBudget:
  enabled: true
  minAvailable: 1

conduit:
  config: |
    server:
      port: 8080
      management_port: 8081
      metrics_port: 9090
    audit:
      enabled: true
      buffer_size: 10000

  secrets:
    # These MUST be overridden at install time:
    # helm install conduit ./helm \
    #   --set conduit.secrets.jwtSecret=<64-char-string> \
    #   --set conduit.secrets.databaseURL=postgres://... \
    #   --set conduit.secrets.redisURL=redis://...
    jwtSecret: ""       # Required
    databaseURL: ""     # Required
    redisURL: ""        # Required

postgresql:
  enabled: true  # set false to use external PostgreSQL
  auth:
    username: conduit
    password: conduit
    database: conduit

redis:
  enabled: true  # set false to use external Redis
  architecture: standalone
  auth:
    enabled: false
```

---

## 4. Key Templates

### `helm/templates/deployment.yaml` (key sections)

```yaml
# SSE requires long-lived connections — configure terminationGracePeriodSeconds
spec:
  terminationGracePeriodSeconds: 60  # allow in-flight SSE sessions to drain
  containers:
    - name: conduit
      livenessProbe:
        httpGet:
          path: /healthz
          port: 8080
        initialDelaySeconds: 5
        periodSeconds: 10
        failureThreshold: 3
      readinessProbe:
        httpGet:
          path: /readyz
          port: 8080
        initialDelaySeconds: 5
        periodSeconds: 5
        failureThreshold: 3
      # Startup probe for slow DB migrations
      startupProbe:
        httpGet:
          path: /readyz
          port: 8080
        failureThreshold: 12   # 12 × 5s = 60s startup budget
        periodSeconds: 5
```

---

## 5. CRD Definitions

### `helm/templates/crds/conduit-tenant.yaml`

```yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: conduittenants.conduit.io
spec:
  group: conduit.io
  versions:
    - name: v1alpha1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              required: ["slug", "name"]
              properties:
                slug:
                  type: string
                  pattern: '^[a-z0-9-]{3,64}$'
                name:
                  type: string
                plan:
                  type: string
                  enum: [free, pro, enterprise]
                  default: free
  scope: Namespaced
  names:
    plural: conduittenants
    singular: conduittenant
    kind: ConduitTenant
    shortNames: ["ct"]
```

### `helm/templates/crds/conduit-server.yaml`

```yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: conduitservers.conduit.io
spec:
  group: conduit.io
  versions:
    - name: v1alpha1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              required: ["tenantRef", "name", "upstreamUrl"]
              properties:
                tenantRef:
                  type: object
                  properties:
                    name:
                      type: string
                name:
                  type: string
                upstreamUrl:
                  type: string
                  format: uri
                authType:
                  type: string
                  enum: [none, bearer, basic, api_key]
                  default: none
                weight:
                  type: integer
                  minimum: 1
                  default: 100
  scope: Namespaced
  names:
    plural: conduitservers
    singular: conduitserver
    kind: ConduitServer
    shortNames: ["cs"]
```

---

## 6. Ingress — SSE Requirements

SSE requires specific nginx annotations to prevent buffering and timeout:

```yaml
# Critical for SSE connections:
nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"    # 1 hour
nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"
nginx.ingress.kubernetes.io/proxy-buffering: "off"         # no response buffering
nginx.ingress.kubernetes.io/proxy-http-version: "1.1"      # HTTP/1.1 for chunked
# Note: HTTP/2 multiplexing may break SSE in some nginx versions — test carefully
```
