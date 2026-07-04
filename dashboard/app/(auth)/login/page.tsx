"use client";

import * as React from "react";
import { useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { setSession } from "@/lib/auth";
import { ConduitAPI, APIError } from "@/lib/api";

// This substitutes for spec/11-dashboard.md's cookie-based JWT login form
// until Phase 5 ships POST /api/v1/auth/login — see lib/auth.ts. It
// validates the pasted key by calling GET /api/v1/tenants (any valid key
// can list tenants; an invalid one gets a 401) and remembers the tenant
// slug the operator says they're managing for display purposes.
export default function LoginPage() {
  const router = useRouter();
  const [baseURL, setBaseURL] = React.useState("http://localhost:8081");
  const [apiKey, setApiKey] = React.useState("");
  const [tenantSlug, setTenantSlug] = React.useState("");
  const [error, setError] = React.useState<string | null>(null);
  const [loading, setLoading] = React.useState(false);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setLoading(true);
    try {
      const api = new ConduitAPI(baseURL, apiKey);
      const { items } = await api.listTenants();
      const tenant = items.find((t) => t.slug === tenantSlug);
      if (!tenant) {
        setError(`No tenant with slug "${tenantSlug}" is visible to this key.`);
        return;
      }
      setSession({ baseURL, apiKey, tenantId: tenant.id, tenantSlug: tenant.slug });
      router.replace("/traffic");
    } catch (err) {
      setError(err instanceof APIError ? err.message : "Could not reach the management API.");
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="flex flex-1 items-center justify-center bg-muted/30 p-4">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>Sign in to Conduit</CardTitle>
          <CardDescription>Enter an API key with access to the tenant you want to manage.</CardDescription>
        </CardHeader>
        <CardContent>
          <form className="space-y-4" onSubmit={handleSubmit}>
            <div className="space-y-1.5">
              <Label htmlFor="baseURL">Management API URL</Label>
              <Input id="baseURL" value={baseURL} onChange={(e) => setBaseURL(e.target.value)} required />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="tenantSlug">Tenant slug</Label>
              <Input
                id="tenantSlug"
                placeholder="acme"
                value={tenantSlug}
                onChange={(e) => setTenantSlug(e.target.value)}
                required
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="apiKey">API key</Label>
              <Input
                id="apiKey"
                type="password"
                placeholder="cnd_..."
                value={apiKey}
                onChange={(e) => setApiKey(e.target.value)}
                required
              />
            </div>
            {error && <p className="text-sm text-destructive">{error}</p>}
            <Button type="submit" className="w-full" disabled={loading}>
              {loading ? "Signing in…" : "Sign in"}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
