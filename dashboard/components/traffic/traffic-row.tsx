import { Badge } from "@/components/ui/badge";
import type { AuditEvent } from "@/types/api";

function statusVariant(status: number): "success" | "warning" | "destructive" {
  if (status >= 200 && status < 300) return "success";
  if (status === 429) return "warning";
  return "destructive";
}

function policyVariant(action: string): "success" | "destructive" | "warning" {
  if (action === "allow") return "success";
  if (action === "rate_limited") return "warning";
  return "destructive";
}

export function TrafficRow({ event }: { event: AuditEvent }) {
  return (
    <div className="flex items-center gap-3 border-b px-4 py-2.5 text-sm last:border-0">
      <span className="w-20 shrink-0 font-mono text-xs text-muted-foreground">
        {new Date(event.created_at).toLocaleTimeString()}
      </span>
      <span className="min-w-0 flex-1 truncate font-medium">{event.tool_name || "(no tool)"}</span>
      <span className="hidden shrink-0 text-xs text-muted-foreground sm:block">{event.server_name}</span>
      <Badge variant={statusVariant(event.status_code)}>{event.status_code}</Badge>
      <span className="w-14 shrink-0 text-right text-xs text-muted-foreground">{event.latency_ms}ms</span>
      <Badge variant={policyVariant(event.policy_action)}>{event.policy_action}</Badge>
    </div>
  );
}
