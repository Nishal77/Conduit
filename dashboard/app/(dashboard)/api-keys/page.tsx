import { APIKeyTable } from "@/components/api-keys/api-key-table";

export default function APIKeysPage() {
  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-xl font-semibold">API Keys</h1>
        <p className="text-sm text-muted-foreground">Credentials agents use to authenticate to the proxy.</p>
      </div>
      <APIKeyTable />
    </div>
  );
}
