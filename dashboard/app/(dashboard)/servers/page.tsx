import { ServerTable } from "@/components/servers/server-table";

export default function ServersPage() {
  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-xl font-semibold">MCP Servers</h1>
        <p className="text-sm text-muted-foreground">Upstream servers Conduit routes tool calls to.</p>
      </div>
      <ServerTable />
    </div>
  );
}
