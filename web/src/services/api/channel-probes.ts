import axios from "axios";

import type { ModelProtocol } from "@/lib/model-protocols";

const api = axios.create({ baseURL: import.meta.env.VITE_CANVAS_BACKEND_URL || "/api", withCredentials: true });

type BackendEnvelope<T> = { code: number; data: T; msg: string };

export type ChannelProbeRequest = {
    requestKey: string;
    kind?: "text" | "tool";
    channelId?: string;
    baseUrl?: string;
    apiKey?: string;
    apiFormat?: "openai" | "gemini";
    interfaceType?: ModelProtocol;
    model: string;
};

export type ChannelProbeStatus = {
    id: string;
    reused?: boolean;
    kind: "text" | "tool" | string;
    status: "queued" | "running" | "succeeded" | "failed" | "cancelled";
    stage: string;
    progress: number;
    model: string;
    protocol: ModelProtocol | string;
    error?: string;
    result?: {
        ok: boolean;
        transport: "stream" | "non-stream-compatible" | "non-stream-fallback" | string;
        durationMs: number;
        firstByteMs?: number;
        deliverySpanMs?: number;
        longestChunkWaitMs?: number;
        totalChunkWaitMs?: number;
        streamReadCount?: number;
        progressive?: boolean;
        responsePreview?: string;
        toolCalling?: "supported" | string;
        toolName?: string;
        checkedAt: string;
        verifierVersion?: string;
    };
    startedAt?: string;
    completedAt?: string;
    createdAt: string;
    updatedAt: string;
};

async function request<T>(promise: Promise<{ data: BackendEnvelope<T> }>) {
    try {
        const response = await promise;
        if (response.data.code !== 0) throw new Error(response.data.msg || "请求失败");
        return response.data.data;
    } catch (error) {
        if (axios.isAxiosError<BackendEnvelope<unknown>>(error)) throw new Error(error.response?.data?.msg || error.message || "请求失败");
        throw error;
    }
}

export function createChannelProbe(input: ChannelProbeRequest) {
    return request<{ probe: ChannelProbeStatus }>(api.post("/channel-probes", input));
}

export function getChannelProbe(id: string) {
    return request<{ probe: ChannelProbeStatus }>(api.get(`/channel-probes/${encodeURIComponent(id)}`));
}
