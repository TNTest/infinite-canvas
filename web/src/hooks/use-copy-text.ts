"use client";

import { App } from "antd";
import copy from "copy-to-clipboard";

export function useCopyText() {
    const { message } = App.useApp();

    return async (value: string, successText = "已复制", isCurrent = () => true): Promise<boolean> => {
        if (!isCurrent()) return false;
        try {
            if (!navigator.clipboard?.writeText) throw new Error("Clipboard API unavailable");
            await navigator.clipboard.writeText(value);
            if (!isCurrent()) return false;
            message.success(successText);
            return true;
        } catch {
            if (!isCurrent()) return false;
            try {
                const copied = await copy(value, { fallbackToPrompt: false });
                if (!isCurrent()) return false;
                if (copied) {
                    message.success(successText);
                    return true;
                }
            } catch {
                // The browser may also reject the synchronous fallback.
            }
            if (!isCurrent()) return false;
            message.error("复制失败，请手动选择并复制");
            return false;
        }
    };
}
