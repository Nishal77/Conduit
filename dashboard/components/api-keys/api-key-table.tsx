"use client";

import * as React from "react";
import { toast } from "sonner";
import { useSession } from "@/components/providers/session-provider";
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { CreateAPIKeyDialog } from "./create-api-key-dialog";
import { formatDate, formatRelativeTime } from "@/lib/utils";
import type { APIKey } from "@/types/api";
import { APIError } from "@/lib/api";

export function APIKeyTable() {
  const { session, api } = useSession();
  const [keys, setKeys] = React.useState<APIKey[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [revoking, setRevoking] = React.useState<string | null>(null);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const { items } = await api.listAPIKeys(session.tenantId);
      setKeys(items);
    } catch (err) {
      toast.error(err instanceof APIError ? err.message : "Failed to load API keys");
    } finally {
      setLoading(false);
    }
  }, [api, session.tenantId]);

  React.useEffect(() => {
    load();
  }, [load]);

  async function handleRevoke(key: APIKey) {
    if (!window.confirm(`Revoke key "${key.name}" (${key.key_prefix})?`)) return;
    setRevoking(key.id);
    try {
      await api.revokeAPIKey(key.id);
      toast.success("API key revoked");
      await load();
    } catch (err) {
      toast.error(err instanceof APIError ? err.message : "Failed to revoke API key");
    } finally {
      setRevoking(null);
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex justify-end">
        <CreateAPIKeyDialog onCreated={load} />
      </div>
      <div className="rounded-lg border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Key Prefix</TableHead>
              <TableHead>Scopes</TableHead>
              <TableHead>Created</TableHead>
              <TableHead>Last Used</TableHead>
              <TableHead>Expires</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {loading ? (
              <TableRow><TableCell colSpan={7} className="text-center text-muted-foreground">Loading…</TableCell></TableRow>
            ) : keys.length === 0 ? (
              <TableRow><TableCell colSpan={7} className="text-center text-muted-foreground">No API keys yet</TableCell></TableRow>
            ) : (
              keys.map((k) => (
                <TableRow key={k.id}>
                  <TableCell className="font-medium">{k.name}</TableCell>
                  <TableCell className="font-mono text-xs">{k.key_prefix}</TableCell>
                  <TableCell className="text-xs text-muted-foreground">{k.scopes.join(", ")}</TableCell>
                  <TableCell>{formatDate(k.created_at)}</TableCell>
                  <TableCell>{formatRelativeTime(k.last_used_at)}</TableCell>
                  <TableCell>{k.expires_at ? formatDate(k.expires_at) : "never"}</TableCell>
                  <TableCell className="text-right">
                    <Button variant="ghost" size="sm" disabled={revoking === k.id} onClick={() => handleRevoke(k)}>
                      {revoking === k.id ? "Revoking…" : "Revoke"}
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
