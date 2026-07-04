"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { Activity, FileText, Key, Server, Gauge, Puzzle } from "lucide-react";
import { cn } from "@/lib/utils";

const navItems = [
  { href: "/traffic", label: "Live Traffic", icon: Activity },
  { href: "/audit", label: "Audit Log", icon: FileText },
  { href: "/api-keys", label: "API Keys", icon: Key },
  { href: "/servers", label: "MCP Servers", icon: Server },
  { href: "/rate-limits", label: "Rate Limits", icon: Gauge },
  { href: "/plugins", label: "Plugins", icon: Puzzle },
];

export function Sidebar() {
  const pathname = usePathname();

  return (
    <aside className="flex h-screen w-56 shrink-0 flex-col border-r bg-background">
      <div className="flex h-14 items-center border-b px-4">
        <span className="text-sm font-semibold tracking-tight">Conduit</span>
      </div>
      <nav className="flex-1 space-y-1 p-2">
        {navItems.map((item) => {
          const active = pathname?.startsWith(item.href);
          const Icon = item.icon;
          return (
            <Link
              key={item.href}
              href={item.href}
              className={cn(
                "flex items-center gap-2 rounded-md px-3 py-2 text-sm font-medium transition-colors",
                active ? "bg-muted text-primary" : "text-muted-foreground hover:bg-muted hover:text-foreground",
              )}
            >
              <Icon className="size-4" />
              {item.label}
            </Link>
          );
        })}
      </nav>
    </aside>
  );
}
