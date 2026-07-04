import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Puzzle } from "lucide-react";

// The plugins page's backing tables (plugins, tenant_plugins) and the
// plugin registry itself land in Phase 6 — see internal/api/handlers's
// package doc comment for the same dependency-ordering reason webhooks and
// OAuth applications aren't wired up yet either. This page exists (spec
// requires it in the nav) but honestly reflects that there's nothing to
// manage yet, rather than wiring up a form that would fail every request.
export default function PluginsPage() {
  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-xl font-semibold">Plugins</h1>
        <p className="text-sm text-muted-foreground">
          Built-in and HTTP callback plugins for PII redaction, cost tracking, and more.
        </p>
      </div>
      <Card>
        <CardHeader className="items-center text-center">
          <Puzzle className="mb-2 size-8 text-muted-foreground" />
          <CardTitle>Plugin management arrives in Phase 6</CardTitle>
          <CardDescription>
            The plugin registry, per-tenant enable/configure UI, and the underlying database tables aren&apos;t built
            yet. Conduit&apos;s plugin interface (<code>internal/plugin</code>) already exists and runs an empty
            registry today — this page will list and toggle real plugins once Phase 6 lands.
          </CardDescription>
        </CardHeader>
      </Card>
    </div>
  );
}
