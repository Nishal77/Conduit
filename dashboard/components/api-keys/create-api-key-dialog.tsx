"use client";

import * as React from "react";
import { toast } from "sonner";
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
import type { APIKeyWithSecret } from "@/types/api";
import { Copy, Check } from "lucide-react";

export function CreateAPIKeyDialog({ onCreated }: { onCreated: () => void }) {
  const { session, api } = useSession();
  const [open, setOpen] = React.useState(false);
  const [name, setName] = React.useState("");
  const [expiresIn, setExpiresIn] = React.useState<string>("never");
  const [created, setCreated] = React.useState<APIKeyWithSecret | null>(null);
  const [copied, setCopied] = React.useState(false);
  const [submitting, setSubmitting] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  async function handleCreate(e: React.FormEvent) {
    e.preventDefault();
    setSubmitting(true);
    setError(null);
    try {
      const key = await api.createAPIKey({
        tenant_id: session.tenantId,
        name,
        expires_in: expiresIn === "never" ? null : (expiresIn as "7d" | "30d" | "90d" | "1y"),
      });
      setCreated(key);
      onCreated();
    } catch (err) {
      setError(err instanceof APIError ? err.message : "Failed to create API key");
    } finally {
      setSubmitting(false);
    }
  }

  function handleCopy() {
    if (!created) return;
    navigator.clipboard.writeText(created.key);
    setCopied(true);
    toast.success("Copied to clipboard");
    setTimeout(() => setCopied(false), 2000);
  }

  function handleClose(next: boolean) {
    setOpen(next);
    if (!next) {
      setName("");
      setExpiresIn("never");
      setCreated(null);
      setError(null);
    }
  }

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogTrigger asChild>
        <Button>Create API Key</Button>
      </DialogTrigger>
      <DialogContent>
        {created ? (
          <>
            <DialogHeader>
              <DialogTitle>API key created</DialogTitle>
              <DialogDescription>This key will not be shown again — store it securely.</DialogDescription>
            </DialogHeader>
            <div className="flex items-center gap-2 rounded-md border bg-muted p-3 font-mono text-sm">
              <span className="flex-1 truncate">{created.key}</span>
              <Button type="button" variant="ghost" size="icon" onClick={handleCopy}>
                {copied ? <Check className="size-4" /> : <Copy className="size-4" />}
              </Button>
            </div>
            <p className="text-sm font-medium text-destructive">
              ⚠ Store this key securely — it will not be shown again.
            </p>
            <DialogFooter>
              <Button onClick={() => handleClose(false)}>Done</Button>
            </DialogFooter>
          </>
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>Create API Key</DialogTitle>
              <DialogDescription>Generates a new key for tenant &quot;{session.tenantSlug}&quot;.</DialogDescription>
            </DialogHeader>
            <form className="space-y-4" onSubmit={handleCreate}>
              <div className="space-y-1.5">
                <Label htmlFor="key-name">Name</Label>
                <Input id="key-name" required value={name} onChange={(e) => setName(e.target.value)} placeholder="my-agent-key" />
              </div>
              <div className="space-y-1.5">
                <Label>Expiry</Label>
                <Select value={expiresIn} onValueChange={setExpiresIn}>
                  <SelectTrigger><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="never">Never</SelectItem>
                    <SelectItem value="7d">7 days</SelectItem>
                    <SelectItem value="30d">30 days</SelectItem>
                    <SelectItem value="90d">90 days</SelectItem>
                    <SelectItem value="1y">1 year</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              {error && <p className="text-sm text-destructive">{error}</p>}
              <DialogFooter>
                <Button type="submit" disabled={submitting}>{submitting ? "Creating…" : "Create"}</Button>
              </DialogFooter>
            </form>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
