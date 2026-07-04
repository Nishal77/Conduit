import { AuditTable } from "@/components/audit/audit-table";

export default function AuditPage() {
  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-xl font-semibold">Audit Log</h1>
        <p className="text-sm text-muted-foreground">Every tool call Conduit has processed for this tenant.</p>
      </div>
      <AuditTable />
    </div>
  );
}
