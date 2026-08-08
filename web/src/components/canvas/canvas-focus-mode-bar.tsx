import { motion, useReducedMotion } from "motion/react";
import { Bot, ListTodo, PanelBottom, X, ZoomIn, ZoomOut } from "lucide-react";
import { Tooltip } from "antd";
import { Link } from "react-router";

import { aceternityMotion } from "@/lib/aceternity-motion";
import { canvasThemes } from "@/lib/canvas-theme";
import { useThemeStore } from "@/stores/use-theme-store";

type CanvasFocusModeBarProps = {
    dockRevealed: boolean;
    agentOpen: boolean;
    activeTaskCount?: number;
    zoomPercent: number;
    onToggleDock: () => void;
    onToggleAgent: () => void;
    onExit: () => void;
    onZoomIn: () => void;
    onZoomOut: () => void;
    onFit: () => void;
};

export function CanvasFocusModeBar({ dockRevealed, agentOpen, activeTaskCount = 0, zoomPercent, onToggleDock, onToggleAgent, onExit, onZoomIn, onZoomOut, onFit }: CanvasFocusModeBarProps) {
    const theme = canvasThemes[useThemeStore((state) => state.theme)];
    const reducedMotion = useReducedMotion();

    return (
        <motion.div
            initial={reducedMotion ? { opacity: 0 } : { opacity: 0, y: -12 }}
            animate={{ opacity: 1, y: 0 }}
            transition={reducedMotion ? { duration: 0 } : aceternityMotion.spring.panel}
            className="pointer-events-auto absolute left-1/2 top-2 z-50 -translate-x-1/2"
            role="toolbar"
            aria-label="专注模式工具栏"
        >
            <div className="flex items-center gap-0.5 rounded-full p-1 backdrop-blur-2xl" style={{ background: theme.spatial.elevated, color: theme.node.text, boxShadow: "0 20px 64px rgba(28,25,23,.18)" }}>
                <Tooltip title="退出专注模式（Esc）">
                    <button type="button" onClick={onExit} className="grid size-8 place-items-center rounded-full transition hover:bg-black/5 dark:hover:bg-white/10" style={{ color: theme.node.text }} aria-label="退出专注模式">
                        <X className="size-4" />
                    </button>
                </Tooltip>
                <span className="mx-0.5 h-4 w-px" style={{ background: theme.toolbar.border }} />
                <Tooltip title={dockRevealed ? "收起工具" : "工具"}>
                    <button type="button" onClick={onToggleDock} className="grid size-8 place-items-center rounded-full transition hover:bg-black/5 dark:hover:bg-white/10" style={{ color: theme.node.text, background: dockRevealed ? theme.toolbar.itemHover : undefined }} aria-label="工具" aria-pressed={dockRevealed}>
                        <PanelBottom className="size-4" />
                    </button>
                </Tooltip>
                <Tooltip title={agentOpen ? "收起智能体" : "智能体"}>
                    <button type="button" onClick={onToggleAgent} className="grid size-8 place-items-center rounded-full transition hover:bg-black/5 dark:hover:bg-white/10" style={{ color: theme.node.text, background: agentOpen ? theme.toolbar.itemHover : undefined }} aria-label="智能体" aria-pressed={agentOpen}>
                        <Bot className="size-4" />
                    </button>
                </Tooltip>
                {activeTaskCount > 0 ? (
                    <Tooltip title={`打开任务中心（${activeTaskCount} 个进行中）`}>
                        <Link to="/tasks" className="relative grid size-8 place-items-center rounded-full transition hover:bg-black/5 dark:hover:bg-white/10" style={{ color: theme.node.text }} aria-label={`打开任务中心，${activeTaskCount} 个任务进行中`}>
                            <ListTodo className="size-4" />
                            <span className="absolute -right-0.5 -top-0.5 grid min-w-3.5 place-items-center rounded-full px-1 text-[9px] font-semibold leading-3" style={{ background: theme.accent.primary, color: "#fff" }}>{activeTaskCount > 9 ? "9+" : activeTaskCount}</span>
                        </Link>
                    </Tooltip>
                ) : null}
                <span className="mx-0.5 h-4 w-px" style={{ background: theme.toolbar.border }} />
                <Tooltip title="缩小">
                    <button type="button" onClick={onZoomOut} className="grid size-8 place-items-center rounded-full transition hover:bg-black/5 dark:hover:bg-white/10" style={{ color: theme.node.text }} aria-label="缩小"><ZoomOut className="size-4" /></button>
                </Tooltip>
                <button type="button" onClick={onFit} className="grid h-8 min-w-14 place-items-center rounded-full px-2 text-xs font-medium tabular-nums transition hover:bg-black/5 dark:hover:bg-white/10" style={{ color: theme.node.text }} title="适应全部内容">
                    {Math.round(zoomPercent * 100)}%
                </button>
                <Tooltip title="放大">
                    <button type="button" onClick={onZoomIn} className="grid size-8 place-items-center rounded-full transition hover:bg-black/5 dark:hover:bg-white/10" style={{ color: theme.node.text }} aria-label="放大"><ZoomIn className="size-4" /></button>
                </Tooltip>
            </div>
        </motion.div>
    );
}
