import { apiDelete, apiGet, apiPost } from "@/services/api/request";

export type UserAPIKey = {
    id: string;
    name: string;
    prefix: string;
    expiresAt: string | null;
    lastUsedAt: string | null;
    createdAt: string;
};

export function fetchUserAPIKeys(token: string) {
    return apiGet<{ items: UserAPIKey[]; total: number }>("/api/v1/api-keys", undefined, token);
}

export function createUserAPIKey(token: string, payload: { name: string; expiresAt: string | null }) {
    return apiPost<{ apiKey: UserAPIKey; key: string }>("/api/v1/api-keys", payload, token);
}

export function deleteUserAPIKey(token: string, id: string) {
    return apiDelete<boolean>(`/api/v1/api-keys/${encodeURIComponent(id)}`, token);
}
