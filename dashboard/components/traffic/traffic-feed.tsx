"use client";

import * as React from "react";
import { useSession } from "@/components/providers/session-provider";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { TrafficRow } from "./traffic-row";
import type { AuditEvent } from "@/types/api";

const MAX_EVENTS = 200;

export function TrafficFeed() {
  const { session, api } = useSession();
  const [events, setEvents] = React.useState<AuditEvent[]>([]);
  const [connected, setConnected] = React.useState(false);
  const [paused, setPaused] = React.useState(false);
  const pausedRef = React.useRef(paused);
  pausedRef.current = paused;

  React.useEffect(() => {
    const controller = new AbortController();

    async function connect() {
      setConnected(false);
      try {
        setConnected(true);
        await api.streamAuditEvents(
          session.tenantId,
          (raw) => {
            if (pausedRef.current) return;
            try {
              const event = JSON.parse(raw) as AuditEvent;
              setEvents((prev) => [event, ...prev].slice(0, MAX_EVENTS));
            } catch {
              // ignore malformed chunk
            }
          },
          controller.signal,
        );
      } catch {
        if (!controller.signal.aborted) setConnected(false);
      }
    }
    connect();

    return () => controller.abort();
  }, [api, session.tenantId]);

  return (
    <div className="flex h-full flex-col rounded-lg border">
      <div className="flex items-center justify-between border-b px-4 py-3">
        <div className="flex items-center gap-2">
          <span className={`size-2 rounded-full ${connected ? "bg-success" : "bg-muted-foreground"}`} />
          <span className="text-sm font-medium">{connected ? "Live" : "Connecting…"}</span>
          <Badge variant="outline">{events.length} events</Badge>
        </div>
        <Button variant="outline" size="sm" onClick={() => setPaused((p) => !p)}>
          {paused ? "Resume" : "Pause"}
        </Button>
      </div>
      <div className="flex-1 overflow-y-auto">
        {events.length === 0 ? (
          <p className="p-6 text-center text-sm text-muted-foreground">
            Waiting for tool calls against tenant &quot;{session.tenantSlug}&quot;…
          </p>
        ) : (
          events.map((e) => <TrafficRow key={e.id} event={e} />)
        )}
      </div>
    </div>
  );
}
