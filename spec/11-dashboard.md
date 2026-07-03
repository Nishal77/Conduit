# Spec 11 — TypeScript Dashboard

> Phase: P4 | Directory: `dashboard/`

---

## 1. Stack

- **Framework**: Next.js 15 (App Router)
- **Language**: TypeScript (strict mode)
- **UI Library**: shadcn/ui + Tailwind CSS
- **Charts**: recharts
- **Data Tables**: TanStack Table v8
- **HTTP Client**: fetch (native), with a typed API client in `lib/api.ts`
- **Real-time**: Native EventSource (SSE) for live streams
- **Auth**: JWT stored in httpOnly cookie (set by management API `/api/v1/auth/login`)

---

## 2. Directory Structure

```
dashboard/
├── app/
│   ├── layout.tsx              # Root layout: sidebar + header
│   ├── page.tsx                # Redirect to /traffic
│   ├── (auth)/
│   │   └── login/
│   │       └── page.tsx        # Login form
│   └── (dashboard)/
│       ├── layout.tsx          # Dashboard layout (sidebar nav)
│       ├── traffic/
│       │   └── page.tsx        # Real-time tool call stream
│       ├── audit/
│       │   └── page.tsx        # Audit log viewer + filters
│       ├── api-keys/
│       │   └── page.tsx        # API key management
│       ├── servers/
│       │   └── page.tsx        # MCP server registration + health
│       ├── rate-limits/
│       │   └── page.tsx        # Rate limit configuration
│       └── plugins/
│           └── page.tsx        # Plugin enable/disable per tenant
├── components/
│   ├── ui/                     # shadcn/ui primitives (auto-generated)
│   ├── layout/
│   │   ├── Sidebar.tsx
│   │   └── Header.tsx
│   ├── traffic/
│   │   ├── TrafficFeed.tsx     # Real-time SSE event list
│   │   └── TrafficRow.tsx
│   ├── audit/
│   │   ├── AuditTable.tsx
│   │   ├── AuditFilters.tsx
│   │   └── AuditExport.tsx
│   ├── api-keys/
│   │   ├── APIKeyTable.tsx
│   │   ├── CreateAPIKeyDialog.tsx
│   │   └── RevokeAPIKeyDialog.tsx
│   ├── servers/
│   │   ├── ServerTable.tsx
│   │   ├── RegisterServerDialog.tsx
│   │   └── ServerHealthBadge.tsx
│   └── charts/
│       ├── RPSChart.tsx
│       └── LatencyChart.tsx
├── lib/
│   ├── api.ts                  # Typed API client
│   ├── auth.ts                 # JWT helpers
│   └── utils.ts                # cn(), formatDate(), etc.
├── types/
│   └── api.ts                  # TypeScript interfaces for all API types
├── next.config.ts
├── tailwind.config.ts
└── package.json
```

---

## 3. Sidebar Navigation

```tsx
// components/layout/Sidebar.tsx
const navItems = [
  { href: "/traffic",     label: "Live Traffic",  icon: ActivityIcon },
  { href: "/audit",       label: "Audit Log",     icon: FileTextIcon },
  { href: "/api-keys",    label: "API Keys",      icon: KeyIcon      },
  { href: "/servers",     label: "MCP Servers",   icon: ServerIcon   },
  { href: "/rate-limits", label: "Rate Limits",   icon: GaugeIcon    },
  { href: "/plugins",     label: "Plugins",       icon: PuzzleIcon   },
]
```

Active state: highlight with `bg-muted` and `text-primary`.

---

## 4. Pages

### `/traffic` — Live Traffic

```tsx
// Real-time SSE stream of tool calls
// Component: TrafficFeed.tsx

// State:
const [events, setEvents] = useState<AuditEvent[]>([])
const [connected, setConnected] = useState(false)

// SSE connection:
useEffect(() => {
    const es = new EventSource(`/api/v1/audit/stream?tenant_id=${tenantID}`, {
        withCredentials: true,
    })
    es.onmessage = (e) => {
        const event = JSON.parse(e.data) as AuditEvent
        setEvents(prev => [event, ...prev].slice(0, 200))  // keep last 200
    }
    es.onopen = () => setConnected(true)
    es.onerror = () => setConnected(false)
    return () => es.close()
}, [tenantID])

// Display: scrollable list of TrafficRow cards, newest at top
// Each row shows: time, tool name, status badge, latency, policy badge
// Auto-scroll toggle button
// Pause/resume button
```

### `/audit` — Audit Log

```tsx
// Paginated table with filters
// Component: AuditTable.tsx

// Filters:
// - Tenant selector (dropdown)
// - Date range picker (from/to)
// - Tool name text input
// - Policy action select (all/allow/deny/rate_limited)
// - Server name text input

// Table columns:
// Timestamp | Tenant | Tool Name | Server | Status | Latency | Policy | Trace ID

// Pagination: previous/next with page size selector (10/25/50/100)
// Export button: downloads CSV via GET /api/v1/audit/export
```

### `/api-keys` — API Key Management

```tsx
// List + create + revoke
// Component: APIKeyTable.tsx

// Table columns:
// Name | Key Prefix | Scopes | Created | Last Used | Expires | Actions (revoke)

// "Create API Key" button → dialog:
// - Name input (required)
// - Expiry select (never/7d/30d/90d/1y)
// - On success: show key ONE TIME with copy-to-clipboard
// - Warning banner: "This key will not be shown again"

// Revoke: confirmation dialog before action
```

### `/servers` — MCP Servers

```tsx
// List + register + health check
// Component: ServerTable.tsx

// Table columns:
// Name | Upstream URL | Auth Type | Weight | Status | Health | Created | Actions

// Status badge: enabled (green) / disabled (gray)
// Health badge: refreshes every 30s via GET /api/v1/servers/{id}/health
//   - OK (green) / Error (red) / Unknown (gray)

// "Register Server" button → dialog:
// - Name, Upstream URL, Auth Type, Auth Config (dynamic form), Health Check URL, Weight
```

### `/rate-limits` — Rate Limits

```tsx
// CRUD for rate limit configs
// Grouped by scope (tenant / server / tool / agent)

// Create form:
// - Scope select
// - Target text input (shows "all" placeholder when empty)
// - Requests + Window (slider with preset options: 60s/5m/1h)
// - Visual preview: "1000 requests per minute with 1.5x burst"
```

### `/plugins` — Plugins

```tsx
// List available plugins + per-tenant toggle
// Two sections: Built-in Plugins | HTTP Callback Plugins

// Each plugin card:
// - Name, version, description
// - Toggle switch (enabled/disabled for current tenant)
// - "Configure" button → opens config editor (JSON form)
// - Priority field (lower = runs first)
```

---

## 5. Typed API Client — `lib/api.ts`

```typescript
// Typed wrapper around fetch for all management API calls

export class ConduitAPI {
    private baseURL: string
    private token: string

    constructor(baseURL: string, token: string) {
        this.baseURL = baseURL
        this.token = token
    }

    private async request<T>(method: string, path: string, body?: unknown): Promise<T> {
        const res = await fetch(`${this.baseURL}${path}`, {
            method,
            headers: {
                'Authorization': `Bearer ${this.token}`,
                'Content-Type': 'application/json',
            },
            body: body ? JSON.stringify(body) : undefined,
        })
        if (!res.ok) {
            const err = await res.json()
            throw new APIError(err.error, err.code, res.status)
        }
        return res.json()
    }

    // Tenants
    listTenants(): Promise<{ items: Tenant[] }> { return this.request('GET', '/api/v1/tenants') }
    createTenant(input: CreateTenantInput): Promise<Tenant> { return this.request('POST', '/api/v1/tenants', input) }

    // API Keys
    listAPIKeys(tenantID: string): Promise<{ items: APIKey[] }> {
        return this.request('GET', `/api/v1/api-keys?tenant_id=${tenantID}`)
    }
    createAPIKey(input: CreateAPIKeyInput): Promise<APIKeyWithSecret> {
        return this.request('POST', '/api/v1/api-keys', input)
    }
    revokeAPIKey(keyID: string): Promise<void> { return this.request('DELETE', `/api/v1/api-keys/${keyID}`) }

    // Servers
    listServers(tenantID: string): Promise<{ items: MCPServer[] }> {
        return this.request('GET', `/api/v1/servers?tenant_id=${tenantID}`)
    }
    registerServer(input: RegisterServerInput): Promise<MCPServer> { return this.request('POST', '/api/v1/servers', input) }
    deleteServer(serverID: string): Promise<void> { return this.request('DELETE', `/api/v1/servers/${serverID}`) }

    // Audit
    queryAudit(params: AuditQueryParams): Promise<AuditQueryResult> {
        const qs = new URLSearchParams(params as any).toString()
        return this.request('GET', `/api/v1/audit/events?${qs}`)
    }
}

export class APIError extends Error {
    constructor(public message: string, public code: string, public status: number) {
        super(message)
    }
}
```

---

## 6. TypeScript Types — `types/api.ts`

```typescript
export interface Tenant {
    id: string
    slug: string
    name: string
    plan: 'free' | 'pro' | 'enterprise'
    settings: Record<string, unknown>
    created_at: string
}

export interface APIKey {
    id: string
    tenant_id: string
    name: string
    key_prefix: string
    scopes: string[]
    expires_at: string | null
    last_used_at: string | null
    created_at: string
}

// Only returned on creation
export interface APIKeyWithSecret extends APIKey {
    key: string
}

export interface MCPServer {
    id: string
    tenant_id: string
    name: string
    upstream_url: string
    auth_type: 'none' | 'bearer' | 'basic' | 'api_key'
    health_check_url: string | null
    weight: number
    enabled: boolean
    created_at: string
}

export interface AuditEvent {
    id: string
    tenant_id: string
    agent_id: string
    session_id: string
    server_name: string
    tool_name: string
    status_code: number
    latency_ms: number
    auth_method: 'api_key' | 'jwt'
    policy_action: 'allow' | 'deny' | 'rate_limited'
    cost_usd: string | null
    trace_id: string | null
    created_at: string
}
```

---

## 7. Playwright E2E Tests

File: `dashboard/tests/e2e/`

Required test scenarios:
```typescript
// test: create-api-key.spec.ts
// 1. Navigate to /api-keys
// 2. Click "Create API Key"
// 3. Fill name = "e2e-test-key", expiry = "30d"
// 4. Click "Create"
// 5. Assert: modal shows key starting with "cnd_"
// 6. Click copy button, assert clipboard contains the key
// 7. Close modal
// 8. Assert: table contains "e2e-test-key" row

// test: view-audit-log.spec.ts
// 1. Navigate to /audit
// 2. Assert: table renders with columns
// 3. Set "Tool Name" filter to "github/"
// 4. Assert: all visible rows have tool_name starting with "github/"
// 5. Click "Export CSV"
// 6. Assert: download starts (check for download event)

// test: register-server.spec.ts
// 1. Navigate to /servers
// 2. Click "Register Server"
// 3. Fill name, upstream_url, auth_type=none
// 4. Click "Register"
// 5. Assert: new row appears in table with name
// 6. Click health check button on that row
// 7. Assert: health badge shows green "OK" or red "Error" (not "Unknown")
```
