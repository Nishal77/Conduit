"use client";

import * as React from "react";
import { useSession } from "@/components/providers/session-provider";
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
import { Select, SelectTrigger, SelectValue, SelectContent, SelectItem } from "@/components/ui/select";
import { formatDate } from "@/lib/utils";
import type { AuditEvent } from "@/types/api";
import { APIError } from "@/lib/api";

const PAGE_SIZES = [10, 25, 50, 100];

function policyVariant(action: string): "success" | "destructive" | "warning" {
  if (action === "allow") return "success";
  if (action === "rate_limited") return "warning";
  return "destructive";
}

export function AuditTable() {
  const { session, api } = useSession();
  const [events, setEvents] = React.useState<AuditEvent[]>([]);
  const [total, setTotal] = React.useState(0);
  const [toolName, setToolName] = React.useState("");
  const [serverName, setServerName] = React.useState("");
  const [policyAction, setPolicyAction] = React.useState<string>("all");
  const [from, setFrom] = React.useState("");
  const [to, setTo] = React.useState("");
  const [limit, setLimit] = React.useState(25);
  const [offset, setOffset] = React.useState(0);
  const [loading, setLoading] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  const load = React.useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const result = await api.queryAudit({
        tenant_id: session.tenantId,
        tool_name: toolName || undefined,
        server_name: serverName || undefined,
        policy_action: policyAction === "all" ? undefined : policyAction,
        from: from || undefined,
        to: to || undefined,
        limit,
        offset,
      });
      setEvents(result.events);
      setTotal(result.total);
    } catch (err) {
      setError(err instanceof APIError ? err.message : "Failed to load audit events");
    } finally {
      setLoading(false);
    }
  }, [api, session.tenantId, toolName, serverName, policyAction, from, to, limit, offset]);

  React.useEffect(() => {
    load();
  }, [load]);

  function applyFilters(e: React.FormEvent) {
    e.preventDefault();
    setOffset(0);
    load();
  }

  const exportURL = api.auditExportURL({
    tenant_id: session.tenantId,
    tool_name: toolName || undefined,
    server_name: serverName || undefined,
    policy_action: policyAction === "all" ? undefined : policyAction,
    from: from || undefined,
    to: to || undefined,
    format: "csv",
  });

  return (
    <div className="space-y-4">
      <form className="flex flex-wrap items-end gap-3 rounded-lg border p-4" onSubmit={applyFilters}>
        <div className="space-y-1.5">
          <Label htmlFor="tool">Tool name</Label>
          <Input id="tool" placeholder="github/*" value={toolName} onChange={(e) => setToolName(e.target.value)} />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="server">Server</Label>
          <Input id="server" value={serverName} onChange={(e) => setServerName(e.target.value)} />
        </div>
        <div className="space-y-1.5">
          <Label>Policy</Label>
          <Select value={policyAction} onValueChange={setPolicyAction}>
            <SelectTrigger className="w-36"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All</SelectItem>
              <SelectItem value="allow">Allow</SelectItem>
              <SelectItem value="deny">Deny</SelectItem>
              <SelectItem value="rate_limited">Rate limited</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="from">From</Label>
          <Input id="from" placeholder="24h" value={from} onChange={(e) => setFrom(e.target.value)} className="w-28" />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="to">To</Label>
          <Input id="to" placeholder="now" value={to} onChange={(e) => setTo(e.target.value)} className="w-28" />
        </div>
        <Button type="submit">Apply filters</Button>
        <a href={exportURL} target="_blank" rel="noreferrer">
          <Button type="button" variant="outline">Export CSV</Button>
        </a>
      </form>

      {error && <p className="text-sm text-destructive">{error}</p>}

      <div className="rounded-lg border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Timestamp</TableHead>
              <TableHead>Tool Name</TableHead>
              <TableHead>Server</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Latency</TableHead>
              <TableHead>Policy</TableHead>
              <TableHead>Trace ID</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {loading ? (
              <TableRow><TableCell colSpan={7} className="text-center text-muted-foreground">Loading…</TableCell></TableRow>
            ) : events.length === 0 ? (
              <TableRow><TableCell colSpan={7} className="text-center text-muted-foreground">No events found</TableCell></TableRow>
            ) : (
              events.map((e) => (
                <TableRow key={e.id}>
                  <TableCell>{formatDate(e.created_at)}</TableCell>
                  <TableCell className="font-medium">{e.tool_name || "—"}</TableCell>
                  <TableCell>{e.server_name}</TableCell>
                  <TableCell>{e.status_code}</TableCell>
                  <TableCell>{e.latency_ms}ms</TableCell>
                  <TableCell><Badge variant={policyVariant(e.policy_action)}>{e.policy_action}</Badge></TableCell>
                  <TableCell className="max-w-32 truncate font-mono text-xs">{e.trace_id || "—"}</TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </div>

      <div className="flex items-center justify-between text-sm text-muted-foreground">
        <div className="flex items-center gap-2">
          <span>Rows per page</span>
          <Select value={String(limit)} onValueChange={(v) => { setLimit(Number(v)); setOffset(0); }}>
            <SelectTrigger className="w-20"><SelectValue /></SelectTrigger>
            <SelectContent>
              {PAGE_SIZES.map((size) => (
                <SelectItem key={size} value={String(size)}>{size}</SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex items-center gap-3">
          <span>{total === 0 ? 0 : offset + 1}–{Math.min(offset + limit, total)} of {total}</span>
          <Button variant="outline" size="sm" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - limit))}>
            Previous
          </Button>
          <Button variant="outline" size="sm" disabled={offset + limit >= total} onClick={() => setOffset(offset + limit)}>
            Next
          </Button>
        </div>
      </div>
    </div>
  );
}
