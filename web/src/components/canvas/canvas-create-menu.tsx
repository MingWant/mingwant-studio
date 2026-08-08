import type { CSSProperties, ReactNode } from "react";

import { canvasThemes } from "@/lib/canvas-theme";
import { CanvasCreateCommandGrid } from "@/components/canvas/canvas-create-command-grid";
import { useThemeStore } from "@/stores/use-theme-store";

export type CanvasCreateCommand = {
    id: string;
    label: string;
    icon: ReactNode;
    badge?: string;
    section: "node" | "project" | "resource";
    onClick: () => void;
};

// 上游创建菜单将“创作节点”和“导入资源”分层，避免所有操作挤在同一组小按钮里。
export function CanvasCreateMenu({ commands }: { commands: CanvasCreateCommand[] }) {
    const theme = canvasThemes[useThemeStore((state) => state.theme)];
    const projectCommands = commands.filter((command) => command.section === "project");
    const nodeCommands = commands.filter((command) => command.section === "node");
    const resourceCommands = commands.filter((command) => command.section === "resource");

    return (
        <div>
            <header className="flex min-h-7 items-center justify-between gap-2 border-b pb-2" style={{ borderColor: theme.toolbar.border }}>
                <h2 className="font-semibold leading-none" style={{ fontSize: "10px" }}>添加节点</h2>
                {projectCommands.map((command) => (
                    <button key={command.id} type="button" className="inline-flex h-6 min-w-0 items-center gap-1 rounded-lg px-1.5 text-[9px] font-medium outline-none transition-colors hover:bg-black/5 focus-visible:ring-2 dark:hover:bg-white/8" style={{ color: theme.node.muted }} title={command.label} onMouseDown={(event) => event.stopPropagation()} onClick={command.onClick}>
                        {command.icon}<span className="whitespace-nowrap">{command.label}</span>
                    </button>
                ))}
            </header>
            <MenuSection title="创作节点" color={theme.node.muted} />
            <CanvasCreateCommandGrid commands={nodeCommands} variant="node" />
            <MenuSection title="导入资源" color={theme.node.muted} spaced />
            <CanvasCreateCommandGrid commands={resourceCommands} variant="resource" />
        </div>
    );
}

function MenuSection({ title, color, spaced = false }: { title: string; color: string; spaced?: boolean }) {
    return <h3 className="mb-1 px-1 text-[9px] font-medium leading-none" style={{ color, marginTop: spaced ? "14px" : "8px" } as CSSProperties}>{title}</h3>;
}
