import { apiDelete, apiGet, apiPost } from "@/services/api/request";

export type UserAPIKeyScope = "read" | "generate" | "write" | "delete";

export type UserAPIKey = {
    id: string;
    name: string;
    prefix: string;
    expiresAt: string | null;
    lastUsedAt: string | null;
    createdAt: string;
    scopes: UserAPIKeyScope[];
    rateLimit: number;
    requestLimit: number | null;
    requestCount: number;
};

export type UserAPIKeyLog = {
    id: string;
    keyId: string;
    userId?: string;
    method: string;
    path: string;
    status: number;
    durationMs: number;
    createdAt: string;
};

export function fetchUserAPIKeys(token: string) {
    return apiGet<{ items: UserAPIKey[]; total: number }>("/api/v1/api-keys", undefined, token);
}

export function createUserAPIKey(token: string, payload: { name: string; expiresAt: string | null; scopes: UserAPIKeyScope[]; rateLimit: number; requestLimit: number | null }) {
    return apiPost<{ apiKey: UserAPIKey; key: string }>("/api/v1/api-keys", payload, token);
}

export function deleteUserAPIKey(token: string, id: string) {
    return apiDelete<boolean>(`/api/v1/api-keys/${encodeURIComponent(id)}`, token);
}

export function fetchUserAPIKeyLogs(token: string, id: string) {
    return apiGet<{ items: UserAPIKeyLog[]; total: number }>(`/api/v1/api-keys/${encodeURIComponent(id)}/logs`, undefined, token);
}
