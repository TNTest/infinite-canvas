import { create } from "zustand";
import { persist, type PersistStorage, type StorageValue } from "zustand/middleware";

import { nanoid } from "nanoid";
import { localForageStorage } from "@/lib/localforage-storage";
import { deleteImagePreviews } from "@/services/image-storage";
import { listCanvasProjects, readCanvasProject, saveCanvasProject, syncCanvasProjects } from "@/services/api/canvas-tasks";
import { fetchUserConfig } from "@/services/api/user-config";
import { useUserStore } from "@/stores/use-user-store";
import type { CanvasBackgroundMode } from "@/lib/canvas-theme";
import type { CanvasAgentConfig, CanvasAssistantSession, CanvasConnection, CanvasNodeData, CanvasPendingAgentRequest, ViewportTransform } from "../types";

export type CanvasSidePanelState = {
    open: boolean;
    width: number;
};

export const DEFAULT_CANVAS_SIDE_PANEL: CanvasSidePanelState = { open: true, width: 280 };
export const DEFAULT_CANVAS_AGENT_PANEL: CanvasSidePanelState = { open: false, width: 390 };

export type CanvasProject = {
    id: string;
    title: string;
    createdAt: string;
    updatedAt: string;
    cloudUpdatedAt?: string;
    nodes: CanvasNodeData[];
    connections: CanvasConnection[];
    chatSessions: CanvasAssistantSession[];
    activeChatId: string | null;
    agentConfig: CanvasAgentConfig | null;
    autoTitlePending: boolean;
    pendingAgentRequest?: CanvasPendingAgentRequest;
    backgroundMode: CanvasBackgroundMode;
    showImageInfo: boolean;
    viewport: ViewportTransform;
    sidePanel: CanvasSidePanelState;
    agentPanel: CanvasSidePanelState;
};

type CanvasStore = {
    hydrated: boolean;
    projects: CanvasProject[];
    createProject: (title?: string, options?: { agentConfig?: CanvasAgentConfig; pendingAgentRequest?: CanvasPendingAgentRequest }) => string;
    importProject: (project: Partial<CanvasProject>) => string;
    openProject: (id: string) => CanvasProject | null;
    renameProject: (id: string, title: string) => void;
    deleteProjects: (ids: string[]) => void;
    updateProject: (id: string, patch: Partial<Pick<CanvasProject, "nodes" | "connections" | "chatSessions" | "activeChatId" | "agentConfig" | "autoTitlePending" | "backgroundMode" | "showImageInfo" | "viewport" | "sidePanel" | "agentPanel" | "pendingAgentRequest">>) => void;
    syncWithRemote: (token: string, syncEnabled: boolean) => Promise<void>;
    setSyncEnabled: (enabled: boolean) => void;
    cloudConflicts: Record<string, CanvasProject | null | undefined>;
    cloudReloads: Record<string, number | undefined>;
    refreshCloudProject: (id: string, canApply: () => boolean) => Promise<void>;
    keepDraftAndLoadCloud: (id: string) => Promise<void>;
};

const initialViewport: ViewportTransform = { x: 0, y: 0, k: 1 };
const CANVAS_STORE_KEY = "infinite-canvas:canvas_store";
type PersistedCanvasState = Pick<CanvasStore, "projects">;
let saveTimer: ReturnType<typeof setTimeout> | null = null;
let queuedPersistState: PersistedCanvasState | null = null;
let accountCanvasSyncEnabled = false;
const projectSaveTimers = new Map<string, ReturnType<typeof setTimeout>>();
const pendingProjectSaves = new Map<string, { token: string; project: CanvasProject }>();
const savingProjects = new Set<string>();
const refreshingProjects = new Set<string>();
const fromCloud = (project: CanvasProject): CanvasProject => ({ ...project, cloudUpdatedAt: project.updatedAt });
const hasLocalChanges = (project: CanvasProject) => project.updatedAt !== project.cloudUpdatedAt;

function nextLocalTimestamp(project: CanvasProject) {
    return new Date(Math.max(Date.now(), (Date.parse(project.updatedAt) || 0) + 1)).toISOString();
}

function waitForUserStoreHydration() {
    if (useUserStore.persist.hasHydrated()) return Promise.resolve();

    return new Promise<void>((resolve) => {
        let unsubscribe = () => { };
        unsubscribe = useUserStore.persist.onFinishHydration(() => {
            unsubscribe();
            resolve();
        });
        if (useUserStore.persist.hasHydrated()) {
            unsubscribe();
            resolve();
        }
    });
}

async function flushProjectSave(id: string) {
    if (savingProjects.has(id) || projectSaveTimers.has(id)) return;
    const pending = pendingProjectSaves.get(id);
    pendingProjectSaves.delete(id);
    if (!pending || !accountCanvasSyncEnabled || useUserStore.getState().token !== pending.token ||
        useCanvasStore.getState().cloudConflicts[id] !== undefined) return;

    savingProjects.add(id);
    try {
        const saved = await saveCanvasProject(pending.token, pending.project);
        if (useUserStore.getState().token !== pending.token) return;
        const current = useCanvasStore.getState().openProject(id);
        if (!current) return;
        const next = current === pending.project
            ? { ...current, updatedAt: saved.updatedAt, cloudUpdatedAt: saved.updatedAt }
            : { ...current, cloudUpdatedAt: saved.updatedAt, updatedAt: nextLocalTimestamp(saved) };
        useCanvasStore.setState((state) => ({ projects: state.projects.map((project) => project.id === id ? next : project) }));
        if (current !== pending.project) queueProjectSave(next);
    } catch (error) {
        if (error instanceof Error && "status" in error && error.status === 409) {
            const remote = await readCanvasProject(pending.token, id).then((project) => project ?? null).catch(() => undefined);
            if (remote !== undefined && useUserStore.getState().token === pending.token && useCanvasStore.getState().openProject(id)) {
                cancelProjectSaves([id]);
                useCanvasStore.setState((state) => ({ cloudConflicts: { ...state.cloudConflicts, [id]: remote } }));
            }
        }
    } finally {
        savingProjects.delete(id);
        void flushProjectSave(id);
    }
}

function queueProjectSave(project: CanvasProject) {
    const token = useUserStore.getState().token;
    const previous = projectSaveTimers.get(project.id);
    if (previous) clearTimeout(previous);
    projectSaveTimers.delete(project.id);
    if (!token || !accountCanvasSyncEnabled || useCanvasStore.getState().cloudConflicts[project.id] !== undefined) {
        pendingProjectSaves.delete(project.id);
        return;
    }
    pendingProjectSaves.set(project.id, { token, project });
    projectSaveTimers.set(
        project.id,
        setTimeout(() => {
            projectSaveTimers.delete(project.id);
            void flushProjectSave(project.id);
        }, 400),
    );
}

function cancelProjectSaves(ids: string[]) {
    ids.forEach((id) => {
        pendingProjectSaves.delete(id);
        const timer = projectSaveTimers.get(id);
        if (!timer) return;
        clearTimeout(timer);
        projectSaveTimers.delete(id);
    });
}

async function reconcileCanvasProjects(
    token: string,
    remoteProjects: CanvasProject[],
    localProjects: CanvasProject[],
) {
    const remoteIds = new Set(remoteProjects.map((project) => project.id));
    const newProjects = localProjects.filter((project) => !project.cloudUpdatedAt && !remoteIds.has(project.id));
    if (newProjects.length) {
        remoteProjects = await syncCanvasProjects(token, newProjects).catch(() => remoteProjects);
    }
    return mergeCanvasProjects(remoteProjects, localProjects);
}

const canvasStorage: PersistStorage<CanvasStore> = {
    getItem: async (name) => {
        await waitForUserStoreHydration();
        const localValue = await localForageStorage.getItem(name);
        const token = useUserStore.getState().token;
        const localParsed = localValue
            ? (JSON.parse(localValue) as StorageValue<CanvasStore>)
            : null;
        const localProjects =
            (localParsed?.state as PersistedCanvasState)?.projects || [];
        const localHasData =
            Array.isArray(localProjects) && localProjects.length > 0;

        if (token) {
            try {
                const [userConfig, remoteProjects] = await Promise.all([
                    fetchUserConfig(token),
                    listCanvasProjects(token),
                ]);
                accountCanvasSyncEnabled =
                    userConfig.syncCapabilities?.userData === true;

                if (accountCanvasSyncEnabled && localHasData) {
                    const projects = await reconcileCanvasProjects(
                        token,
                        remoteProjects,
                        localProjects,
                    );

                    const nextState = { projects };
                    const parsed = {
                        state: nextState,
                        version: 0,
                    } as StorageValue<CanvasStore>;
                    queuedPersistState = nextState;
                    await localForageStorage.setItem(
                        name,
                        JSON.stringify(parsed),
                    );
                    return parsed;
                }

                if (
                    remoteProjects.length > 0 &&
                    (accountCanvasSyncEnabled || !localHasData)
                ) {
                    const nextState = { projects: remoteProjects.map(fromCloud) };
                    const parsed = {
                        state: nextState,
                        version: 0,
                    } as StorageValue<CanvasStore>;
                    queuedPersistState = nextState;
                    await localForageStorage.setItem(
                        name,
                        JSON.stringify(parsed),
                    );
                    return parsed;
                }
            } catch (error) {
                console.error(
                    "Failed to hydrate canvas projects from remote",
                    error,
                );
            }
        }

        if (!localParsed) return null;
        queuedPersistState = localParsed.state as PersistedCanvasState;
        return localParsed;
    },

    setItem: (name, value) => {
        const nextState = value.state as PersistedCanvasState;
        if (
            queuedPersistState &&
            queuedPersistState.projects === nextState.projects
        ) {
            return;
        }
        queuedPersistState = nextState;
        if (saveTimer) clearTimeout(saveTimer);
        saveTimer = setTimeout(() => {
            saveTimer = null;
            void localForageStorage.setItem(name, JSON.stringify(value));
        }, 400);
    },
    removeItem: (name) => localForageStorage.removeItem(name),
};

export const useCanvasStore = create<CanvasStore>()(
    persist(
        (set, get) => ({
            hydrated: false,
            projects: [],
            cloudConflicts: {},
            cloudReloads: {},
            createProject: (title = "未命名画布", options) => {
                const now = new Date().toISOString();
                const id = nanoid();
                const project: CanvasProject = {
                    id,
                    title,
                    createdAt: now,
                    updatedAt: now,
                    nodes: [],
                    connections: [],
                    chatSessions: [],
                    activeChatId: null,
                    agentConfig: options?.agentConfig || null,
                    autoTitlePending: true,
                    pendingAgentRequest: options?.pendingAgentRequest,
                    backgroundMode: "lines",
                    showImageInfo: false,
                    viewport: initialViewport,
                    sidePanel: DEFAULT_CANVAS_SIDE_PANEL,
                    agentPanel: options?.pendingAgentRequest ? { ...DEFAULT_CANVAS_AGENT_PANEL, open: true } : DEFAULT_CANVAS_AGENT_PANEL,
                };
                set((state) => ({
                    projects: [project, ...state.projects],
                }));
                queueProjectSave(project);
                return id;
            },
            importProject: (source) => {
                const now = new Date().toISOString();
                const project: CanvasProject = {
                    id: nanoid(),
                    title: source.title || "导入画布",
                    createdAt: source.createdAt || now,
                    updatedAt: now,
                    nodes: source.nodes || [],
                    connections: source.connections || [],
                    chatSessions: (source.chatSessions || []).map((session) => ({ ...session, codexThreadId: undefined, codexServiceId: undefined })),
                    activeChatId: source.activeChatId || null,
                    agentConfig: source.agentConfig || null,
                    autoTitlePending: false,
                    backgroundMode: source.backgroundMode || "lines",
                    showImageInfo: source.showImageInfo || false,
                    viewport: source.viewport || initialViewport,
                    sidePanel: source.sidePanel || DEFAULT_CANVAS_SIDE_PANEL,
                    agentPanel: source.agentPanel || DEFAULT_CANVAS_AGENT_PANEL,
                };
                set((state) => ({
                    projects: [project, ...state.projects],
                }));
                queueProjectSave(project);
                return project.id;
            },
            openProject: (id) =>
                get().projects.find((item) => item.id === id) || null,
            renameProject: (id, title) => {
                const project = get().projects.find(
                    (item) => item.id === id,
                );
                if (!project) return;
                const nextProject = {
                    ...project,
                    title: title.trim() || project.title,
                    autoTitlePending: false,
                    updatedAt: nextLocalTimestamp(project),
                };
                set((state) => ({
                    projects: state.projects.map((item) =>
                        item.id === id ? nextProject : item,
                    ),
                }));
                queueProjectSave(nextProject);
            },
            deleteProjects: (ids) => {
                cancelProjectSaves(ids);
                void deleteImagePreviews(
                    get().projects
                        .filter((project) => ids.includes(project.id))
                        .flatMap((project) => project.nodes.map((node) => `canvas-node:${project.id}:${node.id}`)),
                );
                set((state) => ({
                    projects: state.projects.filter(
                        (project) => !ids.includes(project.id),
                    ),
                }));
            },
            updateProject: (id, patch) => {
                const project = get().projects.find(
                    (item) => item.id === id,
                );
                if (!project || Object.entries(patch).every(([key, value]) => {
                    const previous = project[key as keyof CanvasProject];
                    return previous === value || JSON.stringify(previous) === JSON.stringify(value);
                })) return;
                const nextProject = {
                    ...project,
                    ...patch,
                    updatedAt: nextLocalTimestamp(project),
                };
                set((state) => ({
                    projects: state.projects.map((item) =>
                        item.id === id ? nextProject : item,
                    ),
                }));
                queueProjectSave(nextProject);
            },
            syncWithRemote: async (token, syncEnabled) => {
                accountCanvasSyncEnabled = syncEnabled;
                if (!syncEnabled) return;
                const remoteProjects = await listCanvasProjects(token).catch(
                    () => null,
                );
                if (!remoteProjects || useUserStore.getState().token !== token) return;
                const localProjects = get().projects;
                const projects = await reconcileCanvasProjects(
                    token,
                    remoteProjects,
                    localProjects,
                );
                if (get().projects !== localProjects || useUserStore.getState().token !== token) return;
                if (saveTimer) {
                    clearTimeout(saveTimer);
                    saveTimer = null;
                }
                const nextState = { projects };
                queuedPersistState = nextState;
                const cloudReloads = { ...get().cloudReloads };
                for (const project of projects) {
                    const previous = get().openProject(project.id);
                    if (previous && previous.updatedAt !== project.updatedAt && !hasLocalChanges(project)) {
                        cloudReloads[project.id] = (cloudReloads[project.id] ?? 0) + 1;
                    }
                }
                set({ ...nextState, cloudReloads });
                await localForageStorage.setItem(
                    CANVAS_STORE_KEY,
                    JSON.stringify({ state: nextState, version: 0 }),
                );
            },
            setSyncEnabled: (enabled) => {
                accountCanvasSyncEnabled = enabled;
            },
            refreshCloudProject: async (id, canApply) => {
                const token = useUserStore.getState().token;
                const current = get().openProject(id);
                if (!token || !accountCanvasSyncEnabled || !current || refreshingProjects.has(id) ||
                    savingProjects.has(id) || get().cloudConflicts[id] !== undefined || !canApply()) return;
                refreshingProjects.add(id);
                try {
                    const remote = await readCanvasProject(token, id, current.cloudUpdatedAt);
                    const latest = get().openProject(id);
                    if (latest !== current || savingProjects.has(id) || useUserStore.getState().token !== token || !canApply()) return;
                    if (remote === null || (remote === undefined && !current.cloudUpdatedAt)) {
                        if (hasLocalChanges(current) && !pendingProjectSaves.has(id)) queueProjectSave(current);
                        return;
                    }
                    if (hasLocalChanges(current)) {
                        cancelProjectSaves([id]);
                        set((state) => ({ cloudConflicts: { ...state.cloudConflicts, [id]: remote ?? null } }));
                    } else {
                        set((state) => ({
                            projects: state.projects.flatMap((project) => project.id !== id ? [project] : remote ? [fromCloud(remote)] : []),
                            cloudReloads: { ...state.cloudReloads, [id]: (state.cloudReloads[id] ?? 0) + 1 },
                        }));
                    }
                } catch {
                    // Retain the local project while the network is unavailable.
                } finally {
                    refreshingProjects.delete(id);
                }
            },
            keepDraftAndLoadCloud: async (id) => {
                const token = useUserStore.getState().token;
                const conflict = get().cloudConflicts[id];
                if (!token || conflict === undefined) return;
                const remote = await readCanvasProject(token, id).catch(() => conflict);
                const draft = get().openProject(id);
                if (!draft || useUserStore.getState().token !== token || get().cloudConflicts[id] !== conflict) return;
                get().importProject({ ...draft, title: draft.title + "（本地修改副本）" });
                set((state) => ({
                    projects: state.projects.flatMap((project) => project.id !== id ? [project] : remote ? [fromCloud(remote)] : []),
                    cloudConflicts: { ...state.cloudConflicts, [id]: undefined },
                    cloudReloads: { ...state.cloudReloads, [id]: (state.cloudReloads[id] ?? 0) + 1 },
                }));
            },
        }),
        {
            name: CANVAS_STORE_KEY,
            storage: canvasStorage,
            partialize: (state) =>
                ({
                    projects: state.projects,
                }) as StorageValue<CanvasStore>["state"],
            onRehydrateStorage: () => () => {
                useCanvasStore.setState({ hydrated: true });
            },
        },
    ),
);

export function mergeCanvasProjects(
    remoteProjects: CanvasProject[],
    localProjects: CanvasProject[],
): CanvasProject[] {
    const projects = new Map(localProjects.map((project) => [project.id, project]));
    for (const project of remoteProjects) {
        const local = projects.get(project.id);
        const keepDraft = local && (local.cloudUpdatedAt
            ? hasLocalChanges(local)
            : Date.parse(local.updatedAt) > Date.parse(project.updatedAt));
        if (!keepDraft) projects.set(project.id, fromCloud(project));
    }
    return Array.from(projects.values()).sort(
        (a, b) =>
            Date.parse(b.updatedAt || "") -
            Date.parse(a.updatedAt || ""),
    );
}
