"use client";

import * as React from "react";
import { Badge } from "@/components/ui/badge";
import { useSession } from "@/components/providers/session-provider";
import type { ServerHealth } from "@/types/api";

const POLL_INTERVAL_MS = 30_000;

export function ServerHealthBadge({ serverID }: { serverID: string }) {
  const { api } = useSession();
  const [health, setHealth] = React.useState<ServerHealth | null>(null);

  const check = React.useCallback(async () => {
    try {
      const result = await api.checkServerHealth(serverID);
      setHealth(result);
    } catch {
      setHealth({ status: "error" });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [serverID]);

  React.useEffect(() => {
    check();
    const id = setInterval(check, POLL_INTERVAL_MS);
    return () => clearInterval(id);
  }, [check]);

  if (!health) return <Badge variant="outline">Checking…</Badge>;
  if (health.status === "ok") return <Badge variant="success">OK</Badge>;
  if (health.status === "unknown") return <Badge variant="outline">Unknown</Badge>;
  return <Badge variant="destructive">Error</Badge>;
}
