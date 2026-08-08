import { useEffect, useState, type ReactNode } from "react";
import { Navigate, useLocation } from "react-router";

import { FullScreenLoader } from "@/components/ui/aceternity/full-screen-loader";
import { consumeCanvasAgentLaunchCredentials, hasCanvasAgentLaunchCredentials } from "@/lib/canvas/canvas-agent-launch";
import { useUserStore } from "@/stores/use-user-store";

export function RequireAuth({ children }: { children: ReactNode }) {
    const location = useLocation();
    const hydrated = useUserStore((state) => state.hydrated);
    const user = useUserStore((state) => state.user);
    const launchCredentialsPending = hasCanvasAgentLaunchCredentials();
    const [launchCredentialsConsumed, setLaunchCredentialsConsumed] = useState(false);

    useEffect(() => {
        if (!hydrated || !launchCredentialsPending) return;
        consumeCanvasAgentLaunchCredentials();
        setLaunchCredentialsConsumed(true);
    }, [hydrated, launchCredentialsPending]);

    if (!hydrated) return <FullScreenLoader />;
    if (!user && launchCredentialsPending && !launchCredentialsConsumed) return <FullScreenLoader />;
    if (!user) return <Navigate to={`/login?next=${encodeURIComponent(safeNextPath(location.pathname, location.search))}`} replace />;
    return children;
}

function safeNextPath(pathname: string, search: string) {
    const params = new URLSearchParams(search);
    params.delete("agentUrl");
    params.delete("agentToken");
    const safeSearch = params.toString();
    return `${pathname}${safeSearch ? `?${safeSearch}` : ""}`;
}
