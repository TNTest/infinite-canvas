import type { CanvasProject } from "@/app/(user)/canvas/stores/use-canvas-store";
import { apiGet, apiPost } from "@/services/api/request";
import { useUserStore } from "@/stores/use-user-store";

export async function listCanvasProjects(token: string) {
    return apiGet<CanvasProject[]>("/api/v1/canvas/projects", undefined, token);
}

export async function readCanvasProject(token: string, id: string, knownUpdatedAt?: string) {
    try {
        return await apiGet<CanvasProject | null>(`/api/v1/canvas/projects/${encodeURIComponent(id)}`, { knownUpdatedAt }, token);
    } catch (error) {
        if (error instanceof Error && "status" in error && error.status === 404) return undefined;
        throw error;
    }
}

export async function saveCanvasProject(
    token: string,
    project: CanvasProject,
) {
    const { cloudUpdatedAt, ...data } = project;
    return apiPost<CanvasProject>(
        "/api/v1/canvas/projects",
        { data, expectedUpdatedAt: cloudUpdatedAt },
        token,
    );
}

export async function syncCanvasProjects(
    token: string,
    projects: CanvasProject[],
) {
    return apiPost<CanvasProject[]>(
        "/api/v1/canvas/projects/sync",
        { projects: projects.map(({ cloudUpdatedAt: _cloudUpdatedAt, ...project }) => project) },
        token,
    );
}

export async function stopCanvasTask(kind: "image" | "video" | "audio", id: string, workflow: boolean, source: "canvas" | "video-workbench" = "canvas") {
    const token = useUserStore.getState().token;
    if (!token) throw new Error("请先登录后再停止云端任务");
    return apiPost<{ stopped: boolean }>("/api/v1/canvas/tasks/stop", { kind, id, workflow, source }, token);
}

export async function deleteCanvasProjects(ids: string[]) {
    const token = useUserStore.getState().token;
    const projectIds = Array.from(
        new Set(ids.map((id) => id.trim()).filter(Boolean)),
    );
    if (!token || !projectIds.length) return;
    return apiPost<{ deleted: boolean }>(
        "/api/v1/canvas/projects/delete",
        { ids: projectIds },
        token,
    );
}
