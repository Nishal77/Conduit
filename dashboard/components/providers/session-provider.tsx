"use client";

import * as React from "react";
import { useRouter } from "next/navigation";
import { ConduitAPI } from "@/lib/api";
import { clearSession, getSession, type Session } from "@/lib/auth";

interface SessionContextValue {
  session: Session;
  api: ConduitAPI;
  logout: () => void;
}

const SessionContext = React.createContext<SessionContextValue | null>(null);

// SessionProvider gates every (dashboard) route behind a loaded session,
// redirecting to /login if none is found — the client-side equivalent of
// the server-side auth check spec/11-dashboard.md's cookie-based design
// would use (see lib/auth.ts for why this is client-side for now).
export function SessionProvider({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const [session, setSessionState] = React.useState<Session | null | undefined>(undefined);

  React.useEffect(() => {
    const s = getSession();
    if (!s) {
      router.replace("/login");
      return;
    }
    setSessionState(s);
  }, [router]);

  const logout = React.useCallback(() => {
    clearSession();
    router.replace("/login");
  }, [router]);

  if (session === undefined) {
    return <div className="flex h-screen items-center justify-center text-sm text-muted-foreground">Loading…</div>;
  }
  if (session === null) {
    return null; // redirect effect above is already navigating away
  }

  const api = new ConduitAPI(session.baseURL, session.apiKey);

  return <SessionContext.Provider value={{ session, api, logout }}>{children}</SessionContext.Provider>;
}

export function useSession(): SessionContextValue {
  const ctx = React.useContext(SessionContext);
  if (!ctx) {
    throw new Error("useSession must be used within a SessionProvider");
  }
  return ctx;
}
