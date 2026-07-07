// TypeScript interfaces mirroring the management API's JSON responses
// exactly (spec/11-dashboard.md §6), field-for-field with the Go handlers
// in internal/api/handlers.

export interface Tenant {
  id: string;
  slug: string;
  name: string;
  plan: "free" | "pro" | "enterprise";
  settings: Record<string, unknown>;
  created_at: string;
}

export interface CreateTenantInput {
  slug: string;
  name: string;
  plan?: "free" | "pro" | "enterprise";
}

export interface APIKey {
  id: string;
  tenant_id: string;
  name: string;
  key_prefix: string;
  scopes: string[];
  expires_at: string | null;
  last_used_at: string | null;
  created_at: string;
}

// Only returned on creation.
export interface APIKeyWithSecret extends APIKey {
  key: string;
}

export interface CreateAPIKeyInput {
  tenant_id: string;
  name: string;
  scopes?: string[];
  expires_in?: "7d" | "30d" | "90d" | "1y" | null;
}

export interface MCPServer {
  id: string;
  tenant_id: string;
  name: string;
  upstream_url: string;
  auth_type: "none" | "bearer" | "basic" | "api_key";
  health_check_url?: string;
  weight: number;
  enabled: boolean;
  created_at: string;
}

export interface RegisterServerInput {
  tenant_id: string;
  name: string;
  upstream_url: string;
  auth_type?: "none" | "bearer" | "basic" | "api_key";
  auth_config?: Record<string, unknown>;
  health_check_url?: string;
  weight?: number;
}

export interface ServerHealth {
  status: "ok" | "error" | "unknown";
  reason?: string;
  status_code?: number;
}

export type RateLimitScope = "tenant" | "server" | "tool" | "agent";

export interface RateLimitConfig {
  id: string;
  tenant_id: string;
  scope: RateLimitScope;
  target: string | null;
  requests: number;
  window_sec: number;
  burst: number | null;
  created_at: string;
  updated_at: string;
}

export interface UpsertRateLimitInput {
  tenant_id: string;
  scope: RateLimitScope;
  target?: string | null;
  requests: number;
  window_sec: number;
  burst?: number | null;
}

export interface AuditEvent {
  id: string;
  tenant_id: string;
  agent_id: string;
  session_id: string;
  server_name: string;
  tool_name: string;
  request_args?: Record<string, unknown>;
  response_meta?: Record<string, unknown>;
  status_code: number;
  latency_ms: number;
  auth_method: "api_key" | "jwt";
  policy_action: "allow" | "deny" | "rate_limited";
  cost_usd: string | null;
  trace_id: string | null;
  created_at: string;
}

export interface AuditQueryParams {
  tenant_id: string;
  from?: string;
  to?: string;
  tool_name?: string;
  server_name?: string;
  policy_action?: string;
  limit?: number;
  offset?: number;
}

export interface AuditQueryResult {
  events: AuditEvent[];
  total: number;
  limit: number;
  offset: number;
}

export interface ListResponse<T> {
  items: T[];
  total: number;
  limit: number;
  offset: number;
  has_more: boolean;
}

export interface Plugin {
  id: string;
  name: string;
  version: string;
  plugin_type: "builtin" | "http_callback";
  description?: string;
  config_schema: Record<string, unknown>;
  created_at: string;
}

export interface TenantPlugin {
  id: string;
  tenant_id: string;
  plugin_id: string;
  enabled: boolean;
  config: Record<string, unknown>;
  priority: number;
  created_at: string;
  updated_at: string;
}

export interface UpsertTenantPluginInput {
  enabled: boolean;
  config: Record<string, unknown>;
  priority: number;
}

export type WebhookEvent =
  | "ratelimit.exceeded"
  | "policy.violation"
  | "tool.call.success"
  | "tool.call.error"
  | "apikey.revoked"
  | "server.health.down"
  | "server.health.up"
  | "audit.budget.exceeded";

export interface Webhook {
  id: string;
  tenant_id: string;
  name: string;
  url: string;
  events: WebhookEvent[];
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

export interface CreateWebhookInput {
  tenant_id: string;
  name: string;
  url: string;
  secret: string;
  events: WebhookEvent[];
}
