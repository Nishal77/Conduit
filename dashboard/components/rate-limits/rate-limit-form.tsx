"use client";

import * as React from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectTrigger, SelectValue, SelectContent, SelectItem } from "@/components/ui/select";
import { useSession } from "@/components/providers/session-provider";
import { APIError } from "@/lib/api";
import type { RateLimitScope } from "@/types/api";

const WINDOW_PRESETS = [
  { label: "60 seconds", value: 60 },
  { label: "5 minutes", value: 300 },
  { label: "1 hour", value: 3600 },
];

export function RateLimitForm({ onSaved }: { onSaved: () => void }) {
  const { session, api } = useSession();
  const [scope, setScope] = React.useState<RateLimitScope>("tenant");
  const [target, setTarget] = React.useState("");
  const [requests, setRequests] = React.useState("1000");
  const [windowSec, setWindowSec] = React.useState("60");
  const [submitting, setSubmitting] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setSubmitting(true);
    setError(null);
    try {
      await api.upsertRateLimit({
        tenant_id: session.tenantId,
        scope,
        target: scope === "tenant" ? null : target || null,
        requests: Number(requests),
        window_sec: Number(windowSec),
      });
      onSaved();
      setTarget("");
    } catch (err) {
      setError(err instanceof APIError ? err.message : "Failed to save rate limit");
    } finally {
      setSubmitting(false);
    }
  }

  const requestsPerMin = Math.round((Number(requests) / Number(windowSec)) * 60) || 0;

  return (
    <form className="space-y-4 rounded-lg border p-4" onSubmit={handleSubmit}>
      <div className="grid grid-cols-2 gap-3">
        <div className="space-y-1.5">
          <Label>Scope</Label>
          <Select value={scope} onValueChange={(v) => setScope(v as RateLimitScope)}>
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="tenant">Tenant</SelectItem>
              <SelectItem value="server">Server</SelectItem>
              <SelectItem value="tool">Tool</SelectItem>
              <SelectItem value="agent">Agent</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="target">Target {scope === "tenant" && "(all)"}</Label>
          <Input
            id="target"
            disabled={scope === "tenant"}
            placeholder={scope === "tenant" ? "all" : "github/delete_repo"}
            value={target}
            onChange={(e) => setTarget(e.target.value)}
          />
        </div>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <div className="space-y-1.5">
          <Label htmlFor="requests">Requests</Label>
          <Input id="requests" type="number" min={1} value={requests} onChange={(e) => setRequests(e.target.value)} />
        </div>
        <div className="space-y-1.5">
          <Label>Window</Label>
          <Select value={windowSec} onValueChange={setWindowSec}>
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              {WINDOW_PRESETS.map((p) => (
                <SelectItem key={p.value} value={String(p.value)}>{p.label}</SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>
      <p className="text-sm text-muted-foreground">
        {requests} requests per {windowSec}s ≈ {requestsPerMin} requests/minute (with 1.5x burst headroom).
      </p>
      {error && <p className="text-sm text-destructive">{error}</p>}
      <Button type="submit" disabled={submitting}>{submitting ? "Saving…" : "Save rate limit"}</Button>
    </form>
  );
}
