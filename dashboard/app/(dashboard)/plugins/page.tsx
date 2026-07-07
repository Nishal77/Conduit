import { PluginList } from "@/components/plugins/plugin-list";

export default function PluginsPage() {
  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-xl font-semibold">Plugins</h1>
        <p className="text-sm text-muted-foreground">
          Built-in and HTTP callback plugins for PII redaction, cost tracking, and more.
        </p>
      </div>
      <PluginList />
    </div>
  );
}
