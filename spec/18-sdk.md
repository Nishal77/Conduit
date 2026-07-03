# Spec 18 — SDKs

> Phase: P7 | Files: `sdk/typescript/src/`, `pkg/sdk/`

---

## 1. TypeScript SDK — `@conduit/sdk`

### Package.json

```json
{
  "name": "@conduit/sdk",
  "version": "0.1.0",
  "description": "TypeScript SDK for the Conduit MCP Gateway",
  "main": "dist/index.js",
  "types": "dist/index.d.ts",
  "exports": {
    ".": {
      "import": "./dist/index.mjs",
      "require": "./dist/index.js",
      "types": "./dist/index.d.ts"
    }
  },
  "scripts": {
    "build": "tsup src/index.ts --format cjs,esm --dts",
    "test": "vitest",
    "publish": "npm publish --access public"
  }
}
```

### Client — `sdk/typescript/src/client.ts`

```typescript
import type {
  Tenant, CreateTenantInput,
  APIKey, CreateAPIKeyInput, APIKeyWithSecret,
  MCPServer, RegisterServerInput,
  RateLimitConfig, UpsertRateLimitInput,
  AuditEvent, AuditQuery, AuditQueryResult,
  WebhookConfig, CreateWebhookInput,
  Plugin, TenantPlugin,
  OAuthApplication, CreateOAuthAppInput, OAuthAppWithSecret,
} from './types'

export interface ConduitClientOptions {
  baseURL: string             // e.g. "https://conduit.example.com"
  apiKey?: string             // management API key
  accessToken?: string        // OAuth access token
  onTokenExpired?: () => Promise<string>  // auto-refresh callback
}

export class ConduitClient {
  readonly tenants: TenantsAPI
  readonly apiKeys: APIKeysAPI
  readonly servers: ServersAPI
  readonly rateLimits: RateLimitsAPI
  readonly audit: AuditAPI
  readonly webhooks: WebhooksAPI
  readonly plugins: PluginsAPI
  readonly oauth: OAuthAPI

  constructor(opts: ConduitClientOptions)

  // Low-level request method
  private request<T>(method: string, path: string, body?: unknown): Promise<T>
}

export class TenantsAPI {
  list(): Promise<{ items: Tenant[] }>
  create(input: CreateTenantInput): Promise<Tenant>
  get(id: string): Promise<Tenant>
  update(id: string, input: Partial<CreateTenantInput>): Promise<Tenant>
  delete(id: string): Promise<void>
}

export class APIKeysAPI {
  list(tenantID: string): Promise<{ items: APIKey[] }>
  create(input: CreateAPIKeyInput): Promise<APIKeyWithSecret>
  revoke(id: string): Promise<void>
}

export class ServersAPI {
  list(tenantID: string): Promise<{ items: MCPServer[] }>
  register(input: RegisterServerInput): Promise<MCPServer>
  get(id: string): Promise<MCPServer>
  update(id: string, input: Partial<RegisterServerInput>): Promise<MCPServer>
  delete(id: string): Promise<void>
  health(id: string): Promise<{ status: 'ok' | 'error'; latency_ms: number }>
}

export class RateLimitsAPI {
  list(tenantID: string): Promise<{ items: RateLimitConfig[] }>
  upsert(input: UpsertRateLimitInput): Promise<RateLimitConfig>
  delete(id: string): Promise<void>
}

export class AuditAPI {
  query(params: AuditQuery): Promise<AuditQueryResult>

  // Returns an AsyncIterator that yields AuditEvent objects in real-time.
  // Connects to the SSE stream endpoint.
  // Usage:
  //   for await (const event of client.audit.stream({ tenantID: "..." })) {
  //     console.log(event.tool_name, event.latency_ms)
  //   }
  stream(params: { tenantID: string; toolName?: string }): AsyncIterable<AuditEvent>

  exportCSV(params: AuditQuery, signal?: AbortSignal): Promise<ReadableStream>
}

export class WebhooksAPI {
  list(tenantID: string): Promise<{ items: WebhookConfig[] }>
  create(input: CreateWebhookInput): Promise<WebhookConfig>
  update(id: string, input: Partial<CreateWebhookInput>): Promise<WebhookConfig>
  delete(id: string): Promise<void>
  test(id: string): Promise<{ delivered: boolean; response_code: number }>
}

export class PluginsAPI {
  list(): Promise<{ items: Plugin[] }>
  listForTenant(tenantID: string): Promise<{ items: TenantPlugin[] }>
  enable(tenantID: string, pluginID: string, config?: Record<string, unknown>): Promise<TenantPlugin>
  disable(tenantPlugin: string): Promise<void>
}

export class OAuthAPI {
  listApplications(tenantID: string): Promise<{ items: OAuthApplication[] }>
  createApplication(input: CreateOAuthAppInput): Promise<OAuthAppWithSecret>
  rotateSecret(appID: string): Promise<{ client_secret: string }>
  deleteApplication(appID: string): Promise<void>
}
```

### Audit Stream — AsyncIterator Implementation

```typescript
// sdk/typescript/src/audit-stream.ts
async function* streamAuditEvents(
  url: string,
  headers: Record<string, string>,
  signal?: AbortSignal,
): AsyncGenerator<AuditEvent> {
  const response = await fetch(url, {
    headers: { ...headers, Accept: 'text/event-stream' },
    signal,
  })

  const reader = response.body!.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  while (true) {
    const { done, value } = await reader.read()
    if (done) break

    buffer += decoder.decode(value, { stream: true })
    const lines = buffer.split('\n')
    buffer = lines.pop() ?? ''

    for (const line of lines) {
      if (line.startsWith('data: ')) {
        const data = line.slice(6).trim()
        if (data) {
          yield JSON.parse(data) as AuditEvent
        }
      }
    }
  }
}
```

---

## 2. Go SDK — `pkg/sdk/`

```go
// pkg/sdk/client.go
package sdk

import (
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "time"
)

// Config configures the Conduit SDK client.
type Config struct {
    BaseURL     string        // e.g. "https://conduit.example.com"
    APIKey      string        // management API key; or use AccessToken
    AccessToken string        // OAuth access token
    HTTPClient  *http.Client  // optional; defaults to 30s timeout
}

// Client is the Conduit SDK client.
type Client struct {
    cfg    Config
    http   *http.Client

    Tenants    *TenantsClient
    APIKeys    *APIKeysClient
    Servers    *ServersClient
    RateLimits *RateLimitsClient
    Audit      *AuditClient
    Webhooks   *WebhooksClient
}

// New creates a new SDK client.
func New(cfg Config) *Client {
    if cfg.HTTPClient == nil {
        cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
    }
    c := &Client{cfg: cfg, http: cfg.HTTPClient}
    c.Tenants    = &TenantsClient{c}
    c.APIKeys    = &APIKeysClient{c}
    c.Servers    = &ServersClient{c}
    c.RateLimits = &RateLimitsClient{c}
    c.Audit      = &AuditClient{c}
    c.Webhooks   = &WebhooksClient{c}
    return c
}

// do executes an HTTP request and decodes the JSON response.
func (c *Client) do(ctx context.Context, method, path string, body, result any) error

// TenantsClient provides tenant management operations.
type TenantsClient struct{ c *Client }
func (t *TenantsClient) List(ctx context.Context) ([]*Tenant, error)
func (t *TenantsClient) Create(ctx context.Context, input CreateTenantInput) (*Tenant, error)
func (t *TenantsClient) Get(ctx context.Context, id string) (*Tenant, error)
func (t *TenantsClient) Delete(ctx context.Context, id string) error

// ServersClient provides MCP server management.
type ServersClient struct{ c *Client }
func (s *ServersClient) Register(ctx context.Context, input RegisterServerInput) (*MCPServer, error)
func (s *ServersClient) List(ctx context.Context, tenantID string) ([]*MCPServer, error)
func (s *ServersClient) Delete(ctx context.Context, id string) error

// AuditClient provides audit log access.
type AuditClient struct{ c *Client }
func (a *AuditClient) Query(ctx context.Context, q AuditQuery) (*AuditQueryResult, error)

// Stream returns a channel of audit events from the SSE stream.
// The channel is closed when ctx is cancelled.
func (a *AuditClient) Stream(ctx context.Context, tenantID string, filter AuditStreamFilter) (<-chan *AuditEvent, error)
```

---

## 3. SDK Types — Shared

```go
// pkg/sdk/types.go

type Tenant struct {
    ID        string            `json:"id"`
    Slug      string            `json:"slug"`
    Name      string            `json:"name"`
    Plan      string            `json:"plan"`
    Settings  map[string]any    `json:"settings"`
    CreatedAt time.Time         `json:"created_at"`
}

type CreateTenantInput struct {
    Slug string `json:"slug"`
    Name string `json:"name"`
    Plan string `json:"plan,omitempty"`
}

type MCPServer struct {
    ID          string    `json:"id"`
    TenantID    string    `json:"tenant_id"`
    Name        string    `json:"name"`
    UpstreamURL string    `json:"upstream_url"`
    AuthType    string    `json:"auth_type"`
    Weight      int       `json:"weight"`
    Enabled     bool      `json:"enabled"`
    CreatedAt   time.Time `json:"created_at"`
}

type RegisterServerInput struct {
    TenantID       string         `json:"tenant_id"`
    Name           string         `json:"name"`
    UpstreamURL    string         `json:"upstream_url"`
    AuthType       string         `json:"auth_type"`
    AuthConfig     map[string]any `json:"auth_config,omitempty"`
    HealthCheckURL string         `json:"health_check_url,omitempty"`
    Weight         int            `json:"weight,omitempty"`
}

type AuditEvent struct {
    ID           string    `json:"id"`
    TenantID     string    `json:"tenant_id"`
    ServerName   string    `json:"server_name"`
    ToolName     string    `json:"tool_name"`
    StatusCode   int       `json:"status_code"`
    LatencyMs    int       `json:"latency_ms"`
    AuthMethod   string    `json:"auth_method"`
    PolicyAction string    `json:"policy_action"`
    CostUSD      string    `json:"cost_usd"`
    TraceID      string    `json:"trace_id"`
    CreatedAt    time.Time `json:"created_at"`
}

type AuditQuery struct {
    TenantID     string
    FromTime     *time.Time
    ToTime       *time.Time
    ToolName     string
    PolicyAction string
    Limit        int
    Offset       int
}

type AuditQueryResult struct {
    Events  []*AuditEvent `json:"events"`
    Total   int64         `json:"total"`
    Limit   int           `json:"limit"`
    Offset  int           `json:"offset"`
}
```

---

## 4. Publishing

### TypeScript SDK

```bash
# CI/CD (GitHub Actions release.yml):
cd sdk/typescript
npm install
npm run build
npm publish --access public
# Published as: @conduit/sdk on npmjs.com
```

### Go SDK

```bash
# The Go SDK is part of the main module at pkg/sdk/
# Also published as a separate module at go.conduit.io/sdk (Phase 7+):
# GOPATH/pkg/mod/go.conduit.io/sdk@v0.1.0

# go.mod for standalone SDK:
module go.conduit.io/sdk
go 1.23
require github.com/conduit-oss/conduit/pkg/sdk v0.1.0
```
