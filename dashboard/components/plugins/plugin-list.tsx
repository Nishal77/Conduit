"use client";

import * as React from "react";
import { toast } from "sonner";
import { useSession } from "@/components/providers/session-provider";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { APIError } from "@/lib/api";
import type { Plugin, TenantPlugin } from "@/types/api";

// One row per catalog plugin: whether this tenant has it configured, and a
// form to enable/disable, reprioritize, and edit its JSON config. Built-in
// plugins that take no config (pii-redactor, cost-tracker, logger) still
// get the same generic {} editor as circuit-breaker/transform/http_callback
// rather than five bespoke forms — spec/14-plugins.md's config_schema field
// exists precisely so a future version could render a typed form from it,
// but that's speculative extra work with no plugin actually shipping a
// schema today.
export function PluginList() {
  const { session, api } = useSession();
  const [catalog, setCatalog] = React.useState<Plugin[]>([]);
  const [configured, setConfigured] = React.useState<TenantPlugin[]>([]);
  const [loading, setLoading] = React.useState(true);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const [catalogRes, configuredRes] = await Promise.all([
        api.listPlugins(),
        api.listTenantPlugins(session.tenantId),
      ]);
      setCatalog(catalogRes.items);
      setConfigured(configuredRes.items);
    } catch (err) {
      toast.error(err instanceof APIError ? err.message : "Failed to load plugins");
    } finally {
      setLoading(false);
    }
  }, [api, session.tenantId]);

  React.useEffect(() => {
    load();
  }, [load]);

  if (loading) {
    return <p className="text-sm text-muted-foreground">Loading…</p>;
  }

  return (
    <div className="grid gap-4 md:grid-cols-2">
      {catalog.map((plugin) => (
        <PluginCard
          key={plugin.id}
          plugin={plugin}
          tenantPlugin={configured.find((tp) => tp.plugin_id === plugin.id)}
          onSaved={load}
        />
      ))}
    </div>
  );
}

function PluginCard({
  plugin,
  tenantPlugin,
  onSaved,
}: {
  plugin: Plugin;
  tenantPlugin?: TenantPlugin;
  onSaved: () => void;
}) {
  const { session, api } = useSession();
  const [enabled, setEnabled] = React.useState(tenantPlugin?.enabled ?? false);
  const [priority, setPriority] = React.useState(String(tenantPlugin?.priority ?? 100));
  const [configText, setConfigText] = React.useState(JSON.stringify(tenantPlugin?.config ?? {}, null, 2));
  const [saving, setSaving] = React.useState(false);
  const [configError, setConfigError] = React.useState<string | null>(null);

  async function handleSave() {
    let config: Record<string, unknown>;
    try {
      config = JSON.parse(configText);
    } catch {
      setConfigError("Config must be valid JSON");
      return;
    }
    setConfigError(null);
    setSaving(true);
    try {
      await api.upsertTenantPlugin(session.tenantId, plugin.id, { enabled, config, priority: Number(priority) || 100 });
      toast.success(`${plugin.name} saved`);
      onSaved();
    } catch (err) {
      toast.error(err instanceof APIError ? err.message : "Failed to save plugin config");
    } finally {
      setSaving(false);
    }
  }

  async function handleRemove() {
    if (!tenantPlugin) return;
    if (!window.confirm(`Remove ${plugin.name}'s configuration for this tenant?`)) return;
    try {
      await api.deleteTenantPlugin(session.tenantId, tenantPlugin.id);
      toast.success(`${plugin.name} configuration removed`);
      onSaved();
    } catch (err) {
      toast.error(err instanceof APIError ? err.message : "Failed to remove plugin config");
    }
  }

  return (
    <Card>
      <CardHeader>
        <div className="flex items-start justify-between gap-2">
          <div>
            <CardTitle className="flex items-center gap-2">
              {plugin.name}
              <Badge variant="outline">{plugin.plugin_type}</Badge>
              <Badge variant="secondary">v{plugin.version}</Badge>
            </CardTitle>
            {plugin.description && <CardDescription>{plugin.description}</CardDescription>}
          </div>
          <div className="flex items-center gap-2">
            <Label htmlFor={`enabled-${plugin.id}`} className="text-xs text-muted-foreground">
              Enabled
            </Label>
            <Switch id={`enabled-${plugin.id}`} checked={enabled} onCheckedChange={setEnabled} />
          </div>
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="flex items-center gap-2">
          <Label htmlFor={`priority-${plugin.id}`} className="w-16 text-xs text-muted-foreground">
            Priority
          </Label>
          <Input
            id={`priority-${plugin.id}`}
            type="number"
            className="w-24"
            value={priority}
            onChange={(e) => setPriority(e.target.value)}
          />
          <span className="text-xs text-muted-foreground">lower runs first</span>
        </div>
        <div className="space-y-1.5">
          <Label htmlFor={`config-${plugin.id}`} className="text-xs text-muted-foreground">
            Config (JSON)
          </Label>
          <Textarea
            id={`config-${plugin.id}`}
            rows={4}
            value={configText}
            onChange={(e) => setConfigText(e.target.value)}
          />
          {configError && <p className="text-xs text-destructive">{configError}</p>}
        </div>
        <div className="flex gap-2">
          <Button size="sm" onClick={handleSave} disabled={saving}>
            {saving ? "Saving…" : "Save"}
          </Button>
          {tenantPlugin && (
            <Button size="sm" variant="ghost" onClick={handleRemove}>
              Remove
            </Button>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
