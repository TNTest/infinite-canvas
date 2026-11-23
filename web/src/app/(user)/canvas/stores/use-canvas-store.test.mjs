import assert from "node:assert/strict";
import { mock, test } from "bun:test";

const projects = new Map();
const stored = new Map();
let conflictOnSave;
let revision = 0;
const nextVersion = () => new Date(Date.UTC(2026, 10, 23) + ++revision).toISOString();

mock.module("@/lib/localforage-storage", () => ({
    localForageStorage: {
        getItem: async (key) => stored.get(key) ?? null,
        setItem: async (key, value) => { stored.set(key, value); },
        removeItem: async (key) => { stored.delete(key); },
    },
}));
mock.module("@/services/image-storage", () => ({ deleteImagePreviews: async () => {} }));
mock.module("@/services/api/user-config", () => ({ fetchUserConfig: async () => ({ syncCapabilities: { userData: true } }) }));
mock.module("@/stores/use-user-store", () => ({
    useUserStore: {
        getState: () => ({ token: "browser-session" }),
        persist: { hasHydrated: () => true },
    },
}));
mock.module("@/services/api/canvas-tasks", () => ({
    listCanvasProjects: async () => [...projects.values()],
    syncCanvasProjects: async () => [...projects.values()],
    readCanvasProject: async (_token, id, knownUpdatedAt) => {
        const project = projects.get(id);
        return project?.updatedAt === knownUpdatedAt && project ? null : project;
    },
    saveCanvasProject: async (_token, { cloudUpdatedAt, ...project }) => {
        if (conflictOnSave === project.id) {
            conflictOnSave = undefined;
            const previous = projects.get(project.id);
            projects.set(project.id, { ...previous, title: "更晚的智能体版本", updatedAt: nextVersion() });
        }
        if (projects.get(project.id)?.updatedAt !== cloudUpdatedAt) {
            throw Object.assign(new Error("画布版本冲突"), { status: 409 });
        }
        const saved = { ...project, updatedAt: nextVersion() };
        projects.set(project.id, saved);
        return saved;
    },
}));

const { useCanvasStore } = await import("./use-canvas-store");
await useCanvasStore.persist.rehydrate();
const store = useCanvasStore.getState;
const node = { id: "S29", type: "video", title: "六款齐聚", metadata: { status: "idle" } };
const useCloud = async (id) => {
    const base = { id, title: "标准版分镜", updatedAt: nextVersion(), nodes: [node] };
    projects.set(id, base);
    await store().syncWithRemote("browser-session", true);
    return base;
};

test("an idle browser loads the agent's completed video without writing the empty node back", async () => {
    const base = await useCloud("idle-browser");
    const completed = { ...node, metadata: { status: "success", content: "https://example.invalid/clip.mp4" } };
    projects.set(base.id, { ...base, nodes: [completed], updatedAt: nextVersion() });
    await store().refreshCloudProject(base.id, () => true);
    const current = store().openProject(base.id);
    assert.equal(store().cloudReloads[base.id], 1);
    assert.equal(current.cloudUpdatedAt, current.updatedAt);
    assert.deepEqual(current.nodes, [completed]);
    assert.equal(store().cloudConflicts[base.id], undefined);
});

test("a rejected browser save preserves local edits, then copies them before opening the latest cloud version", async () => {
    const base = await useCloud("editing-browser");
    conflictOnSave = base.id;
    store().updateProject(base.id, { nodes: [{ ...node, title: "网页上的修改" }] });
    for (let attempt = 0; attempt < 30 && !store().cloudConflicts[base.id]; attempt++) await Bun.sleep(25);
    assert.ok(store().cloudConflicts[base.id], "browser found the conflicting save");
    assert.equal(store().openProject(base.id).nodes[0].title, "网页上的修改");
    const cloud = { ...projects.get(base.id), title: "最新云端版", updatedAt: nextVersion() };
    projects.set(base.id, cloud);
    await store().keepDraftAndLoadCloud(base.id);
    assert.equal(store().openProject(base.id).title, cloud.title);
    assert.equal(store().cloudConflicts[base.id], undefined);
    const draft = store().projects.find((project) => project.title.endsWith("（本地修改副本）"));
    assert.ok(draft && draft.id !== base.id);
    assert.equal(draft.nodes[0].title, "网页上的修改");
    assert.equal(projects.get(base.id).title, cloud.title);
});
