import assert from "node:assert/strict";
import test from "node:test";
import axios from "axios";
import type { AdminPublicModelChannelSettings } from "@/services/api/admin";
import { createVideoGenerationTask } from "@/services/api/video";
import { useChannelTranslationStore } from "./use-channel-translation-store";
import { defaultConfig, resolveEffectiveConfig, useConfigStore } from "./use-config-store";

const model = "MiniMax-H3";
const channel = {
    id: "h3",
    protocol: "minimax" as const,
    name: "MiniMax",
    baseUrl: "https://minimax.example",
    models: [model],
};
const localConfig = {
    ...defaultConfig,
    model,
    videoModel: model,
    videoChannelId: channel.id,
    localChannels: [{ ...channel, apiKey: "test-key" }],
};
const publicSetting: AdminPublicModelChannelSettings = {
    availableModels: [model],
    availableWorkflows: [],
    modelCosts: [],
    channels: [{ ...channel, enabled: true, weight: 1, timeout: 0, remark: "" }],
    defaultModel: "",
    defaultImageModel: "",
    defaultVideoModel: model,
    defaultTextModel: "",
    systemPrompt: "",
    systemPrompts: defaultConfig.systemPrompts,
    allowCustomChannel: false,
    allowUserRemoteChannel: true,
};

test("MiniMax-H3 uses five seconds when it inherits the old common default", () => {
    assert.equal(resolveEffectiveConfig(defaultConfig, publicSetting, true).videoModel, model);
    assert.equal(resolveEffectiveConfig(defaultConfig, publicSetting, true).videoSeconds, "5");
    assert.equal(resolveEffectiveConfig(localConfig, null, false).videoSeconds, "5");
    assert.equal(resolveEffectiveConfig({ ...localConfig, videoSeconds: "9" }, null, false).videoSeconds, "9");
    assert.equal(resolveEffectiveConfig(defaultConfig, null, false).videoSeconds, "6");
    const otherProtocol = { ...publicSetting, channels: [{ ...publicSetting.channels[0], protocol: "apimart" as const }] };
    assert.equal(resolveEffectiveConfig(defaultConfig, otherProtocol, true).videoSeconds, "6");
});

test("H3 submits the five-second default and preserves a manual choice of six seconds", async () => {
    const priorConfig = useConfigStore.getState().config;
    const priorAdapter = axios.defaults.adapter;
    const priorLoad = useChannelTranslationStore.getState().load;
    const durations: number[] = [];
    useChannelTranslationStore.setState({ load: async () => ({}) });
    axios.defaults.adapter = async (request) => {
        assert.equal(request.url, channel.baseUrl + "/v2/video_generation");
        const payload = JSON.parse(request.data);
        assert.equal(payload.model, model);
        durations.push(payload.duration);
        return { data: { task_id: "example-task" }, status: 200, statusText: "OK", headers: {}, config: request };
    };
    try {
        const config = resolveEffectiveConfig(localConfig, null, false);
        await createVideoGenerationTask(config, "A short test");
        useConfigStore.setState({ config: localConfig });
        useConfigStore.getState().updateConfig("videoSeconds", "6");
        const saved = JSON.parse(JSON.stringify(useConfigStore.getState().config));
        await createVideoGenerationTask(resolveEffectiveConfig(saved, null, false), "A short test");
        assert.deepEqual(durations, [5, 6]);
    } finally {
        axios.defaults.adapter = priorAdapter;
        useConfigStore.setState({ config: priorConfig });
        useChannelTranslationStore.setState({ load: priorLoad });
    }
});
