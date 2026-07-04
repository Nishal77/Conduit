"use client";

// Session storage for the dashboard's credentials.
//
// spec/11-dashboard.md §1 calls for a JWT in an httpOnly cookie set by
// POST /api/v1/auth/login — that endpoint doesn't exist until Phase 5
// issues real JWTs with a login flow behind it (see
// internal/api/middleware/auth.go's doc comment for why the management API
// only validates Conduit API keys today). Until then, the dashboard's
// "session" is just an API key the operator pastes in once, kept in
// localStorage. It's the same trust boundary either way — this dashboard
// is an internal operator tool the spec's own /api/v1 design keeps off the
// public agent-facing network — but it's plain text in the browser, so
// don't point this dashboard at a production key you wouldn't want a
// browser extension to read.
const STORAGE_KEY = "conduit.session";

export interface Session {
  baseURL: string;
  apiKey: string;
  tenantId: string;
  tenantSlug: string;
}

export function getSession(): Session | null {
  if (typeof window === "undefined") return null;
  const raw = window.localStorage.getItem(STORAGE_KEY);
  if (!raw) return null;
  try {
    return JSON.parse(raw) as Session;
  } catch {
    return null;
  }
}

export function setSession(session: Session): void {
  window.localStorage.setItem(STORAGE_KEY, JSON.stringify(session));
}

export function clearSession(): void {
  window.localStorage.removeItem(STORAGE_KEY);
}
