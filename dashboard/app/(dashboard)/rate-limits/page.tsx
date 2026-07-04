import { RateLimitList } from "@/components/rate-limits/rate-limit-list";

export default function RateLimitsPage() {
  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-xl font-semibold">Rate Limits</h1>
        <p className="text-sm text-muted-foreground">Per-tenant, per-server, per-tool, and per-agent request limits.</p>
      </div>
      <RateLimitList />
    </div>
  );
}
