import { useEffect } from "react";
import { useQuery } from "@tanstack/react-query";

import { listGenerationTasks, type GenerationTask } from "@/services/api/task-center";

export function useCanvasActiveTasks(projectId: string, enabled: boolean) {
    const query = useQuery<GenerationTask[]>({
        queryKey: ["canvas-active-tasks", projectId],
        queryFn: async () => {
            const tasks = await listGenerationTasks(20, { projectId, activeOnly: true });
            return Array.isArray(tasks) ? tasks : [];
        },
        enabled: enabled && Boolean(projectId),
        refetchInterval: (current) => (Array.isArray(current.state.data) && current.state.data.length ? 2_000 : 10_000),
        refetchOnWindowFocus: true,
    });

    useEffect(() => {
        const handleTaskCreated = (event: Event) => {
            const task = (event as CustomEvent<{ task?: GenerationTask }>).detail?.task;
            if (task?.projectId === projectId) void query.refetch();
        };
        window.addEventListener("canvas:task-created", handleTaskCreated);
        return () => window.removeEventListener("canvas:task-created", handleTaskCreated);
    }, [projectId, query.refetch]);

    return {
        tasks: Array.isArray(query.data) ? query.data : [],
        loading: query.isLoading,
        refreshing: query.isFetching,
        refetch: query.refetch,
    };
}

