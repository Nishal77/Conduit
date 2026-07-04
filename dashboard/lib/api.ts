import type {
  APIKey,
  APIKeyWithSecret,
  AuditQueryParams,
  AuditQueryResult,
  CreateAPIKeyInput,
  CreateTenantInput,
  ListResponse,
  MCPServer,
  RateLimitConfig,
  RegisterServerInput,
  ServerHealth,
  Tenant,
  UpsertRateLimitInput,
} from "@/types/api";

export class APIError extends Error {
  code: string;
  status: number;

  constructor(message: string, code: string, status: number) {
    super(message);
    this.code = code;
    this.status = status;
  }
}

// ConduitAPI is a typed wrapper around fetch for every management API call
// the dashboard makes (spec/11-dashboard.md §5). Authentication is a
// Conduit API key, not a JWT: the management API's own auth middleware
// only validates API keys until Phase 5's OAuth server exists (see
// internal/api/middleware/auth.go's doc comment) — so unlike the spec's
// original sketch, there's no /api/v1/auth/login endpoint to set an
// httpOnly cookie yet. lib/auth.ts stores the key client-side instead.
export class ConduitAPI {
  constructor(
    private baseURL: string,
    private token: string,
  ) {}

  private async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const res = await fetch(`${this.baseURL}${path}`, {
      method,
      headers: {
        Authorization: `Bearer ${this.token}`,
        "Content-Type": "application/json",
      },
      body: body ? JSON.stringify(body) : undefined,
      cache: "no-store",
    });

    if (!res.ok) {
      const err = await res.json().catch(() => ({ error: res.statusText, code: "UNKNOWN" }));
      throw new APIError(err.error ?? "request failed", err.code ?? "UNKNOWN", res.status);
    }

    if (res.status === 204) {
      return undefined as T;
    }
    return res.json();
  }

  // Tenants
  listTenants(): Promise<ListResponse<Tenant>> {
    return this.request("GET", "/api/v1/tenants");
  }
  createTenant(input: CreateTenantInput): Promise<Tenant> {
    return this.request("POST", "/api/v1/tenants", input);
  }
  getTenant(id: string): Promise<Tenant> {
    return this.request("GET", `/api/v1/tenants/${id}`);
  }
  deleteTenant(id: string): Promise<void> {
    return this.request("DELETE", `/api/v1/tenants/${id}`);
  }

  // API Keys
  listAPIKeys(tenantID: string): Promise<ListResponse<APIKey>> {
    return this.request("GET", `/api/v1/api-keys?tenant_id=${tenantID}`);
  }
  createAPIKey(input: CreateAPIKeyInput): Promise<APIKeyWithSecret> {
    return this.request("POST", "/api/v1/api-keys", input);
  }
  revokeAPIKey(keyID: string): Promise<void> {
    return this.request("DELETE", `/api/v1/api-keys/${keyID}`);
  }

  // Servers
  listServers(tenantID: string): Promise<ListResponse<MCPServer>> {
    return this.request("GET", `/api/v1/servers?tenant_id=${tenantID}`);
  }
  registerServer(input: RegisterServerInput): Promise<MCPServer> {
    return this.request("POST", "/api/v1/servers", input);
  }
  updateServer(id: string, input: Partial<RegisterServerInput> & { enabled?: boolean }): Promise<MCPServer> {
    return this.request("PATCH", `/api/v1/servers/${id}`, input);
  }
  deleteServer(serverID: string): Promise<void> {
    return this.request("DELETE", `/api/v1/servers/${serverID}`);
  }
  checkServerHealth(serverID: string): Promise<ServerHealth> {
    return this.request("GET", `/api/v1/servers/${serverID}/health`);
  }

  // Rate limits
  listRateLimits(tenantID: string): Promise<ListResponse<RateLimitConfig>> {
    return this.request("GET", `/api/v1/rate-limits?tenant_id=${tenantID}`);
  }
  upsertRateLimit(input: UpsertRateLimitInput): Promise<RateLimitConfig> {
    return this.request("PUT", "/api/v1/rate-limits", input);
  }
  deleteRateLimit(id: string): Promise<void> {
    return this.request("DELETE", `/api/v1/rate-limits/${id}`);
  }

  // Audit
  queryAudit(params: AuditQueryParams): Promise<AuditQueryResult> {
    const qs = new URLSearchParams(
      Object.entries(params).reduce<Record<string, string>>((acc, [k, v]) => {
        if (v !== undefined && v !== "") acc[k] = String(v);
        return acc;
      }, {}),
    ).toString();
    return this.request("GET", `/api/v1/audit/events?${qs}`);
  }

  auditExportURL(params: AuditQueryParams & { format?: "csv" | "json" }): string {
    const qs = new URLSearchParams(
      Object.entries(params).reduce<Record<string, string>>((acc, [k, v]) => {
        if (v !== undefined && v !== "") acc[k] = String(v);
        return acc;
      }, {}),
    ).toString();
    return `${this.baseURL}/api/v1/audit/export?${qs}`;
  }

  auditStreamURL(tenantID: string): string {
    return `${this.baseURL}/api/v1/audit/stream?tenant_id=${tenantID}`;
  }

  // streamAuditEvents consumes the SSE feed via fetch + ReadableStream
  // rather than the browser's native EventSource. EventSource can't send
  // an Authorization header (spec/11-dashboard.md §4's original sketch
  // assumed a cookie-based JWT session via `withCredentials: true`, which
  // doesn't apply here — see this file's class doc comment), but fetch can,
  // and a text/event-stream response is just as readable incrementally
  // through its body reader.
  async streamAuditEvents(
    tenantID: string,
    onEvent: (raw: string) => void,
    signal: AbortSignal,
  ): Promise<void> {
    const res = await fetch(this.auditStreamURL(tenantID), {
      headers: { Authorization: `Bearer ${this.token}` },
      signal,
    });
    if (!res.ok || !res.body) {
      throw new APIError("failed to open audit stream", "STREAM_ERROR", res.status);
    }

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";

    while (true) {
      const { done, value } = await reader.read();
      if (done) return;

      buffer += decoder.decode(value, { stream: true });
      const events = buffer.split("\n\n");
      buffer = events.pop() ?? "";

      for (const chunk of events) {
        const line = chunk.split("\n").find((l) => l.startsWith("data: "));
        if (line) onEvent(line.slice("data: ".length));
      }
    }
  }
}
