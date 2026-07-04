"use client";

import * as React from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectTrigger, SelectValue, SelectContent, SelectItem } from "@/components/ui/select";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
  DialogTrigger,
} from "@/components/ui/dialog";
import { useSession } from "@/components/providers/session-provider";
import { APIError } from "@/lib/api";

export function RegisterServerDialog({ onRegistered }: { onRegistered: () => void }) {
  const { session, api } = useSession();
  const [open, setOpen] = React.useState(false);
  const [name, setName] = React.useState("");
  const [upstreamURL, setUpstreamURL] = React.useState("");
  const [authType, setAuthType] = React.useState<"none" | "bearer" | "basic" | "api_key">("none");
  const [authToken, setAuthToken] = React.useState("");
  const [healthCheckURL, setHealthCheckURL] = React.useState("");
  const [weight, setWeight] = React.useState("100");
  const [submitting, setSubmitting] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  function reset() {
    setName("");
    setUpstreamURL("");
    setAuthType("none");
    setAuthToken("");
    setHealthCheckURL("");
    setWeight("100");
    setError(null);
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setSubmitting(true);
    setError(null);
    try {
      await api.registerServer({
        tenant_id: session.tenantId,
        name,
        upstream_url: upstreamURL,
        auth_type: authType,
        auth_config: authType !== "none" && authToken ? { token: authToken } : undefined,
        health_check_url: healthCheckURL || undefined,
        weight: Number(weight) || 100,
      });
      onRegistered();
      setOpen(false);
      reset();
    } catch (err) {
      setError(err instanceof APIError ? err.message : "Failed to register server");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={(next) => { setOpen(next); if (!next) reset(); }}>
      <DialogTrigger asChild>
        <Button>Register Server</Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Register MCP Server</DialogTitle>
          <DialogDescription>Adds an upstream MCP server route for tenant &quot;{session.tenantSlug}&quot;.</DialogDescription>
        </DialogHeader>
        <form className="space-y-4" onSubmit={handleSubmit}>
          <div className="space-y-1.5">
            <Label htmlFor="server-name">Name</Label>
            <Input id="server-name" required value={name} onChange={(e) => setName(e.target.value)} placeholder="github-mcp" />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="upstream-url">Upstream URL</Label>
            <Input
              id="upstream-url"
              required
              type="url"
              value={upstreamURL}
              onChange={(e) => setUpstreamURL(e.target.value)}
              placeholder="http://github-mcp-server:3001"
            />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <Label>Auth Type</Label>
              <Select value={authType} onValueChange={(v) => setAuthType(v as typeof authType)}>
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">None</SelectItem>
                  <SelectItem value="bearer">Bearer</SelectItem>
                  <SelectItem value="basic">Basic</SelectItem>
                  <SelectItem value="api_key">API Key</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="weight">Weight</Label>
              <Input id="weight" type="number" min={1} value={weight} onChange={(e) => setWeight(e.target.value)} />
            </div>
          </div>
          {authType !== "none" && (
            <div className="space-y-1.5">
              <Label htmlFor="auth-token">Credential</Label>
              <Input id="auth-token" type="password" value={authToken} onChange={(e) => setAuthToken(e.target.value)} />
            </div>
          )}
          <div className="space-y-1.5">
            <Label htmlFor="health-url">Health Check URL (optional)</Label>
            <Input id="health-url" type="url" value={healthCheckURL} onChange={(e) => setHealthCheckURL(e.target.value)} />
          </div>
          {error && <p className="text-sm text-destructive">{error}</p>}
          <DialogFooter>
            <Button type="submit" disabled={submitting}>{submitting ? "Registering…" : "Register"}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
