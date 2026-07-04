"use client";

import { Button } from "@/components/ui/button";
import { useSession } from "@/components/providers/session-provider";
import { LogOut } from "lucide-react";

export function Header() {
  const { session, logout } = useSession();

  return (
    <header className="flex h-14 shrink-0 items-center justify-between border-b px-6">
      <div className="text-sm text-muted-foreground">
        Tenant: <span className="font-medium text-foreground">{session.tenantSlug}</span>
      </div>
      <Button variant="ghost" size="sm" onClick={logout}>
        <LogOut className="size-4" />
        Sign out
      </Button>
    </header>
  );
}
