"use client";

import * as React from "react";
import { toast } from "sonner";
import { useSession } from "@/components/providers/session-provider";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { RateLimitForm } from "./rate-limit-form";
import type { RateLimitConfig, RateLimitScope } from "@/types/api";
import { APIError } from "@/lib/api";

const SCOPE_ORDER: RateLimitScope[] = ["tenant", "server", "tool", "agent"];

export function RateLimitList() {
  const { session, api } = useSession();
  const [configs, setConfigs] = React.useState<RateLimitConfig[]>([]);
  const [loading, setLoading] = React.useState(true);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const { items } = await api.listRateLimits(session.tenantId);
      setConfigs(items);
    } catch (err) {
      toast.error(err instanceof APIError ? err.message : "Failed to load rate limits");
    } finally {
      setLoading(false);
    }
  }, [api, session.tenantId]);

  React.useEffect(() => {
    load();
  }, [load]);

  async function handleDelete(id: string) {
    if (!window.confirm("Delete this rate limit config?")) return;
    try {
      await api.deleteRateLimit(id);
      toast.success("Rate limit deleted");
      await load();
    } catch (err) {
      toast.error(err instanceof APIError ? err.message : "Failed to delete rate limit");
    }
  }

  return (
    <div className="grid gap-6 lg:grid-cols-2">
      <RateLimitForm onSaved={load} />

      <div className="space-y-4">
        {loading ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : configs.length === 0 ? (
          <p className="text-sm text-muted-foreground">No rate limit overrides configured — defaults apply.</p>
        ) : (
          SCOPE_ORDER.map((scope) => {
            const scoped = configs.filter((c) => c.scope === scope);
            if (scoped.length === 0) return null;
            return (
              <div key={scope} className="space-y-2">
                <h3 className="text-sm font-semibold capitalize">{scope} scope</h3>
                {scoped.map((c) => (
                  <div key={c.id} className="flex items-center justify-between rounded-md border p-3 text-sm">
                    <div>
                      <Badge variant="outline" className="mr-2">{c.target ?? "all"}</Badge>
                      {c.requests} req / {c.window_sec}s
                    </div>
                    <Button variant="ghost" size="sm" onClick={() => handleDelete(c.id)}>Delete</Button>
                  </div>
                ))}
              </div>
            );
          })
        )}
      </div>
    </div>
  );
}
