"use client";

import * as React from "react";
import { toast } from "sonner";
import { useSession } from "@/components/providers/session-provider";
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { RegisterServerDialog } from "./register-server-dialog";
import { ServerHealthBadge } from "./server-health-badge";
import { formatDate } from "@/lib/utils";
import type { MCPServer } from "@/types/api";
import { APIError } from "@/lib/api";

export function ServerTable() {
  const { session, api } = useSession();
  const [servers, setServers] = React.useState<MCPServer[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [deleting, setDeleting] = React.useState<string | null>(null);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const { items } = await api.listServers(session.tenantId);
      setServers(items);
    } catch (err) {
      toast.error(err instanceof APIError ? err.message : "Failed to load servers");
    } finally {
      setLoading(false);
    }
  }, [api, session.tenantId]);

  React.useEffect(() => {
    load();
  }, [load]);

  async function handleDelete(server: MCPServer) {
    if (!window.confirm(`Remove server "${server.name}"?`)) return;
    setDeleting(server.id);
    try {
      await api.deleteServer(server.id);
      toast.success("Server removed");
      await load();
    } catch (err) {
      toast.error(err instanceof APIError ? err.message : "Failed to remove server");
    } finally {
      setDeleting(null);
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex justify-end">
        <RegisterServerDialog onRegistered={load} />
      </div>
      <div className="rounded-lg border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Upstream URL</TableHead>
              <TableHead>Auth Type</TableHead>
              <TableHead>Weight</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Health</TableHead>
              <TableHead>Created</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {loading ? (
              <TableRow><TableCell colSpan={8} className="text-center text-muted-foreground">Loading…</TableCell></TableRow>
            ) : servers.length === 0 ? (
              <TableRow><TableCell colSpan={8} className="text-center text-muted-foreground">No servers registered</TableCell></TableRow>
            ) : (
              servers.map((s) => (
                <TableRow key={s.id}>
                  <TableCell className="font-medium">{s.name}</TableCell>
                  <TableCell className="max-w-56 truncate font-mono text-xs">{s.upstream_url}</TableCell>
                  <TableCell>{s.auth_type}</TableCell>
                  <TableCell>{s.weight}</TableCell>
                  <TableCell>
                    <Badge variant={s.enabled ? "success" : "secondary"}>{s.enabled ? "enabled" : "disabled"}</Badge>
                  </TableCell>
                  <TableCell><ServerHealthBadge serverID={s.id} /></TableCell>
                  <TableCell>{formatDate(s.created_at)}</TableCell>
                  <TableCell className="text-right">
                    <Button variant="ghost" size="sm" disabled={deleting === s.id} onClick={() => handleDelete(s)}>
                      {deleting === s.id ? "Removing…" : "Remove"}
                    </Button>
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </div>
    </div>
  );
}
