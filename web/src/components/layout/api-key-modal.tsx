"use client";

import { useCallback, useEffect, useRef, useState, type CSSProperties } from "react";
import { Alert, App, Button, Checkbox, Collapse, DatePicker, Empty, Form, Input, InputNumber, Modal, Pagination, Popconfirm, Radio, Spin, Table, Tag, theme, Tooltip, Typography } from "antd";
import dayjs, { type Dayjs } from "dayjs";
import { Check, Copy, Plus, RefreshCw } from "lucide-react";

import { useCopyText } from "@/hooks/use-copy-text";
import { createUserAPIKey, deleteUserAPIKey, fetchUserAPIKeyLogs, fetchUserAPIKeys, revealUserAPIKey, type UserAPIKey, type UserAPIKeyLog, type UserAPIKeyScope } from "@/services/api/api-keys";
import { useUserStore } from "@/stores/use-user-store";
import styles from "./api-key-modal.module.css";

const MAX_API_KEYS = 100;
const PAGE_SIZE = 6;
const SCOPE_LABELS: Record<UserAPIKeyScope, string> = { read: "只读", generate: "生成", write: "写入", delete: "删除" };
const LEGACY_KEY_HINT = "旧版 Key 未保存可恢复密文，需重新创建";

type APIKeyForm = { name: string; expiresAt?: Dayjs; scopes: UserAPIKeyScope[]; rateLimit: number; requestLimit?: number | null };

function formatTime(value: string | null, fallback: string) {
    return value ? dayjs(value).format("YYYY-MM-DD HH:mm:ss") : fallback;
}

export function APIKeyModal({ onClose }: { onClose: () => void }) {
    const token = useUserStore((state) => state.token);
    const userID = useUserStore((state) => state.user?.id);
    const { token: themeToken } = theme.useToken();
    const { message } = App.useApp();
    const copyText = useCopyText();
    const [form] = Form.useForm<APIKeyForm>();
    const active = useRef(true);
    const loadRequest = useRef(0);
    const logRequest = useRef(0);
    const copyRequest = useRef(0);
    const [items, setItems] = useState<UserAPIKey[]>([]);
    const [total, setTotal] = useState(0);
    const [page, setPage] = useState(1);
    const [loading, setLoading] = useState(true);
    const [loadError, setLoadError] = useState("");
    const [creating, setCreating] = useState(false);
    const [createOpen, setCreateOpen] = useState(false);
    const [createError, setCreateError] = useState("");
    const [deleting, setDeleting] = useState("");
    const [copying, setCopying] = useState("");
    const [copyFeedback, setCopyFeedback] = useState<{ id: string; state: "success" | "error" } | null>(null);
    const [manualKey, setManualKey] = useState<{ id: string; name: string; key: string } | null>(null);
    const [expiryMode, setExpiryMode] = useState("never");
    const [logKey, setLogKey] = useState<UserAPIKey | null>(null);
    const [logs, setLogs] = useState<UserAPIKeyLog[]>([]);
    const [logsLoading, setLogsLoading] = useState(false);
    const [logsError, setLogsError] = useState("");
    const busy = loading || creating || Boolean(deleting) || Boolean(copying);
    const modalStyle = {
        maxWidth: "calc(100vw - 24px)",
        "--api-key-focus": themeToken.colorPrimary,
        "--api-key-surface": themeToken.colorBgContainer,
        "--api-key-hover": themeToken.colorFillTertiary,
        "--api-key-border": themeToken.colorBorderSecondary,
        "--api-key-success": themeToken.colorSuccess,
        "--api-key-error": themeToken.colorError,
        "--api-key-radius": `${themeToken.borderRadiusLG}px`,
    } as CSSProperties;
    const bodyStyle = { maxHeight: "calc(100dvh - 200px)", overflowY: "auto" as const, overscrollBehavior: "contain" as const, padding: "2px 4px" };

    const isCurrentSession = useCallback(() => {
        const current = useUserStore.getState();
        return active.current && current.token === token && current.user?.id === userID;
    }, [token, userID]);

    const loadKeys = useCallback(async () => {
        const request = ++loadRequest.current;
        setLoading(true);
        setLoadError("");
        try {
            const result = await fetchUserAPIKeys(token);
            if (!isCurrentSession() || request !== loadRequest.current) return;
            setItems(result.items);
            setTotal(result.total);
            setPage(1);
        } catch (error) {
            if (isCurrentSession() && request === loadRequest.current) setLoadError(error instanceof Error ? error.message : "加载失败，请刷新重试");
        } finally {
            if (isCurrentSession() && request === loadRequest.current) setLoading(false);
        }
    }, [token, isCurrentSession]);

    useEffect(() => {
        active.current = true;
        void loadKeys();
        return () => {
            active.current = false;
        };
    }, [loadKeys]);

    useEffect(() => {
        if (!copyFeedback || copyFeedback.state !== "success") return;
        const timer = setTimeout(() => setCopyFeedback(null), 2400);
        return () => clearTimeout(timer);
    }, [copyFeedback]);

    function close() {
        active.current = false;
        ++copyRequest.current;
        setManualKey(null);
        onClose();
    }

    function openCreate() {
        if (busy || loadError || total >= MAX_API_KEYS) return;
        form.resetFields();
        setExpiryMode("never");
        setCreateError("");
        setCreateOpen(true);
    }

    async function createKey(values: APIKeyForm) {
        if (busy || loadError || total >= MAX_API_KEYS) return;
        setCreating(true);
        setCreateError("");
        try {
            const result = await createUserAPIKey(token, {
                name: values.name.trim(),
                expiresAt: expiryMode === "never" ? null : values.expiresAt!.toISOString(),
                scopes: values.scopes,
                rateLimit: values.rateLimit,
                requestLimit: values.requestLimit ?? null,
            });
            if (!isCurrentSession()) return;
            setItems((current) => [result.apiKey, ...current]);
            setTotal((current) => current + 1);
            setPage(1);
            setCreateOpen(false);
            message.success("创建成功，可在列表再次复制");
        } catch (error) {
            if (isCurrentSession()) setCreateError(error instanceof Error ? error.message : "创建失败，请稍后重试");
        } finally {
            if (isCurrentSession()) setCreating(false);
        }
    }

    async function copyKey(item: UserAPIKey) {
        if (busy || !item.canCopy) return;
        const request = ++copyRequest.current;
        const isCurrent = () => isCurrentSession() && request === copyRequest.current;
        setCopying(item.id);
        setCopyFeedback(null);
        try {
            const result = await revealUserAPIKey(token, item.id);
            if (!isCurrent()) return;
            const copied = await copyText(result.key, "完整 API Key 已复制", isCurrent);
            if (!isCurrent()) return;
            setCopyFeedback({ id: item.id, state: copied ? "success" : "error" });
            if (!copied) setManualKey({ id: item.id, name: item.name, key: result.key });
        } catch (error) {
            if (!isCurrent()) return;
            setCopyFeedback({ id: item.id, state: "error" });
            message.error(error instanceof Error ? error.message : "取回失败，请刷新后重试");
        } finally {
            if (isCurrent()) setCopying("");
        }
    }

    function closeManualKey() {
        ++copyRequest.current;
        setManualKey(null);
        setCopying("");
    }

    async function copyManualKey() {
        if (!manualKey || copying) return;
        const request = ++copyRequest.current;
        const isCurrent = () => isCurrentSession() && request === copyRequest.current;
        setCopying(manualKey.id);
        const copied = await copyText(manualKey.key, "完整 API Key 已复制", isCurrent);
        if (!isCurrent()) return;
        setCopyFeedback({ id: manualKey.id, state: copied ? "success" : "error" });
        setCopying("");
        if (copied) setManualKey(null);
    }

    async function deleteKey(id: string) {
        if (busy) return;
        setDeleting(id);
        try {
            await deleteUserAPIKey(token, id);
            if (!isCurrentSession()) return;
            setItems((current) => current.filter((item) => item.id !== id));
            setTotal((current) => current - 1);
            setPage((current) => Math.max(1, Math.min(current, Math.ceil((total - 1) / PAGE_SIZE))));
            if (logKey?.id === id) closeLogs();
            message.success("API Key 已撤销");
        } catch (error) {
            if (isCurrentSession()) message.error(error instanceof Error ? error.message : "撤销失败，请刷新后重试");
        } finally {
            if (isCurrentSession()) setDeleting("");
        }
    }

    async function loadLogs(key: UserAPIKey) {
        const request = ++logRequest.current;
        setLogKey(key);
        setLogs([]);
        setLogsError("");
        setLogsLoading(true);
        try {
            const result = await fetchUserAPIKeyLogs(token, key.id);
            if (isCurrentSession() && request === logRequest.current) setLogs(result.items);
        } catch (error) {
            if (isCurrentSession() && request === logRequest.current) setLogsError(error instanceof Error ? error.message : "加载失败，请刷新重试");
        } finally {
            if (isCurrentSession() && request === logRequest.current) setLogsLoading(false);
        }
    }

    function closeLogs() {
        ++logRequest.current;
        setLogKey(null);
        setLogs([]);
        setLogsError("");
        setLogsLoading(false);
    }

    function renderCopy(item: UserAPIKey) {
        const feedback = copyFeedback?.id === item.id ? copyFeedback.state : null;
        return (
            <Tooltip title={!item.canCopy ? LEGACY_KEY_HINT : "复制完整 Key"} trigger={["hover", "focus"]}>
                <span className={styles.copyTarget} tabIndex={!item.canCopy ? 0 : undefined} aria-label={!item.canCopy ? LEGACY_KEY_HINT : undefined}>
                    <Button
                        className={`${styles.action} ${styles.copyButton} ${feedback === "success" ? styles.copySuccess : feedback === "error" ? styles.copyError : ""}`}
                        icon={feedback === "success" ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
                        loading={copying === item.id}
                        disabled={!item.canCopy || (busy && copying !== item.id)}
                        aria-label={`复制 ${item.name} 的完整 Key`}
                        onClick={() => void copyKey(item)}
                    >
                        {feedback === "success" ? "已复制" : feedback === "error" ? "重试" : "复制"}
                    </Button>
                </span>
            </Tooltip>
        );
    }

    function renderActions(item: UserAPIKey) {
        return (
            <div className={styles.actions}>
                <Button type="text" className={styles.action} disabled={busy} onClick={() => void loadLogs(item)}>
                    调用记录
                </Button>
                <Popconfirm
                    title={`撤销「${item.name}」？`}
                    description="撤销后该 Key 立即失效，无法恢复。"
                    okText="撤销"
                    cancelText="取消"
                    disabled={busy}
                    okButtonProps={{ danger: true, className: styles.action }}
                    cancelButtonProps={{ className: styles.action }}
                    onConfirm={() => deleteKey(item.id)}
                >
                    <Button type="text" danger className={styles.action} loading={deleting === item.id} disabled={busy}>
                        撤销
                    </Button>
                </Popconfirm>
            </div>
        );
    }

    function renderPermissions(item: UserAPIKey) {
        return (
            <div className="space-y-1.5">
                <div className="flex flex-wrap gap-y-1">
                    {item.scopes.map((scope) => (
                        <Tag key={scope}>{SCOPE_LABELS[scope]}</Tag>
                    ))}
                </div>
                <Typography.Text type="secondary" className="text-xs">
                    {item.requestCount.toLocaleString()} / {item.requestLimit === null ? "不限" : item.requestLimit.toLocaleString()} 次 · {item.rateLimit} RPM
                </Typography.Text>
            </div>
        );
    }

    const empty = <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="还没有 API Key，创建后即可调用业务 API" />;

    return (
        <Modal
            className={styles.surface}
            title={
                <div className="flex items-center gap-3">
                    API Key{" "}
                    <Typography.Text type="secondary" className="text-sm font-normal">
                        {total} / {MAX_API_KEYS}
                    </Typography.Text>
                </div>
            }
            open
            centered
            width={1040}
            style={modalStyle}
            styles={{ body: bodyStyle }}
            footer={null}
            onCancel={close}
            destroyOnHidden
        >
            <div className={`${styles.surface} space-y-4`}>
                <div className={styles.toolbar}>
                    <Typography.Text type="secondary">以你的身份调用业务 API，按所选权限使用。</Typography.Text>
                    <div className={styles.actions}>
                        <Button className={styles.action} icon={<RefreshCw className="size-3.5" />} loading={loading} disabled={busy} onClick={() => void loadKeys()}>
                            刷新
                        </Button>
                        <Tooltip title={total >= MAX_API_KEYS ? "已达到 100 个上限，请先撤销不用的 Key" : undefined}>
                            <span>
                                <Button type="primary" className={styles.action} icon={<Plus className="size-4" />} disabled={busy || Boolean(loadError) || total >= MAX_API_KEYS} onClick={openCreate}>
                                    创建 Key
                                </Button>
                            </span>
                        </Tooltip>
                    </div>
                </div>
                {loadError ? (
                    <Alert
                        type="error"
                        showIcon
                        title="API Key 加载失败"
                        description={loadError}
                        action={
                            <Button className={styles.action} onClick={() => void loadKeys()}>
                                重试
                            </Button>
                        }
                    />
                ) : null}
                <div className={styles.desktopList}>
                    <Table<UserAPIKey>
                        size="small"
                        rowKey="id"
                        loading={loading}
                        dataSource={items}
                        scroll={{ x: 900 }}
                        pagination={{ current: page, onChange: setPage, pageSize: PAGE_SIZE, showSizeChanger: false, hideOnSinglePage: true }}
                        locale={{ emptyText: loadError ? "请刷新重试" : empty }}
                        columns={[
                            {
                                title: "名称 / Key",
                                width: 255,
                                render: (_, item) => (
                                    <div className="min-w-0 space-y-2">
                                        <Typography.Text strong ellipsis={{ tooltip: item.name }} className="max-w-56">
                                            {item.name}
                                        </Typography.Text>
                                        <div className="flex items-center gap-2">
                                            <code className="text-xs">{item.prefix}…</code>
                                            {renderCopy(item)}
                                        </div>
                                    </div>
                                ),
                            },
                            { title: "权限与用量", width: 235, render: (_, item) => renderPermissions(item) },
                            {
                                title: "有效期",
                                width: 170,
                                render: (_, item) => (
                                    <div className="space-y-1">
                                        <div>{formatTime(item.expiresAt, "永久有效")}</div>
                                        {item.expiresAt && !dayjs(item.expiresAt).isAfter(dayjs()) ? <Tag color="error">已过期</Tag> : null}
                                        <Typography.Text type="secondary" className="text-xs">
                                            最近使用：{formatTime(item.lastUsedAt, "尚未使用")}
                                        </Typography.Text>
                                    </div>
                                ),
                            },
                            { title: "操作", width: 160, fixed: "right", render: (_, item) => renderActions(item) },
                        ]}
                    />
                </div>
                <div className={styles.mobileList}>
                    <Spin spinning={loading}>
                        {items.length ? (
                            <ul className="m-0 list-none space-y-3 p-0">
                                {items.slice((page - 1) * PAGE_SIZE, page * PAGE_SIZE).map((item) => (
                                    <li key={item.id} className={styles.keyCard}>
                                        <div className="flex min-w-0 items-start justify-between gap-2">
                                            <Typography.Text strong className="min-w-0 break-all">
                                                {item.name}
                                            </Typography.Text>
                                            {item.expiresAt && !dayjs(item.expiresAt).isAfter(dayjs()) ? <Tag color="error">已过期</Tag> : null}
                                        </div>
                                        <code className="block text-xs">{item.prefix}…</code>
                                        {renderPermissions(item)}
                                        <Typography.Text type="secondary" className="block text-xs">
                                            有效期：{formatTime(item.expiresAt, "永久有效")}
                                        </Typography.Text>
                                        <div className={styles.actions}>
                                            {renderCopy(item)}
                                            {renderActions(item)}
                                        </div>
                                    </li>
                                ))}
                            </ul>
                        ) : (
                            empty
                        )}
                    </Spin>
                    {items.length > PAGE_SIZE ? <Pagination simple current={page} pageSize={PAGE_SIZE} total={total} onChange={setPage} className="mt-4" /> : null}
                </div>
                <Collapse
                    ghost
                    size="small"
                    items={[
                        {
                            key: "security",
                            label: "使用与安全说明",
                            children: (
                                <div className="space-y-2 text-xs">
                                    <div>
                                        请求头：<Typography.Text code>Authorization: Bearer &lt;你的 API Key&gt;</Typography.Text>
                                    </div>
                                    <div>仅登录后可再次复制完整 Key；旧版 Key 需重新创建，现有 Key 不会自动撤销。</div>
                                    <div>Key 不能访问管理后台、密钥管理、个人模型与存储配置或 Bridge 管理，即使账号是管理员。算力点仍从你的账号扣除。</div>
                                    <div>过期 Key 仍占名额，撤销后可重新创建。请妥善保管，不要分享给他人。</div>
                                </div>
                            ),
                        },
                    ]}
                />
            </div>
            <Modal
                title="创建 API Key"
                className={styles.surface}
                forceRender
                open={createOpen}
                centered
                width={620}
                style={modalStyle}
                styles={{ body: { ...bodyStyle, maxHeight: "calc(100dvh - 240px)" } }}
                onCancel={() => !creating && setCreateOpen(false)}
                onOk={() => form.submit()}
                okText="创建 Key"
                cancelText="取消"
                confirmLoading={creating}
                closable={!creating}
                mask={{ closable: !creating }}
                keyboard={!creating}
                okButtonProps={{ className: styles.action }}
                cancelButtonProps={{ disabled: creating, className: styles.action }}
                destroyOnHidden
            >
                <Form form={form} layout="vertical" initialValues={{ scopes: ["read", "generate"], rateLimit: 60, requestLimit: null }} onFinish={createKey} disabled={creating} className={styles.surface}>
                    {createError ? <Alert type="error" showIcon title="创建失败，请检查后重试" description={createError} className="mb-4" /> : null}
                    <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true, message: "请输入名称，方便区分 Key 的用途" }]}>
                        <Input placeholder="例如：自动化脚本" maxLength={100} autoComplete="off" size="large" />
                    </Form.Item>
                    <Form.Item name="scopes" label="权限" rules={[{ type: "array", required: true, min: 1, message: "请至少选择一项权限" }]} extra="保存已有数据需同时勾选只读和写入；写入不含删除，查询生成结果需要只读权限。">
                        <Checkbox.Group options={Object.entries(SCOPE_LABELS).map(([value, label]) => ({ value, label }))} />
                    </Form.Item>
                    <div className="grid grid-cols-1 gap-x-4 sm:grid-cols-2">
                        <Form.Item name="rateLimit" label="每分钟请求上限（RPM）" rules={[{ required: true, type: "integer", min: 1, max: 6000, message: "请输入 1 至 6000 之间的整数" }]}>
                            <InputNumber min={1} max={6000} precision={0} size="large" className="!w-full" />
                        </Form.Item>
                        <Form.Item name="requestLimit" label="累计请求次数上限" rules={[{ type: "integer", min: 1, max: Number.MAX_SAFE_INTEGER, message: "请输入正整数，或留空表示不限" }]}>
                            <InputNumber min={1} max={Number.MAX_SAFE_INTEGER} precision={0} size="large" placeholder="留空表示不限" className="!w-full" />
                        </Form.Item>
                    </div>
                    <Typography.Paragraph type="secondary" className="text-xs">
                        已认证请求尝试计次，权限拒绝和业务失败也计次。
                    </Typography.Paragraph>
                    <Form.Item label="有效期">
                        <Radio.Group
                            value={expiryMode}
                            onChange={(event) => setExpiryMode(event.target.value)}
                            options={[
                                { label: "永久有效", value: "never" },
                                { label: "指定失效时间", value: "custom" },
                            ]}
                        />
                    </Form.Item>
                    {expiryMode === "custom" ? (
                        <Form.Item
                            name="expiresAt"
                            label="失效时间（本地时间）"
                            preserve={false}
                            rules={[{ required: true, message: "请选择失效时间" }, { validator: (_, value: Dayjs | undefined) => (!value || value.isAfter(dayjs()) ? Promise.resolve() : Promise.reject(new Error("失效时间必须晚于当前时间"))) }]}
                        >
                            <DatePicker showTime format="YYYY-MM-DD HH:mm:ss" size="large" disabledDate={(date) => date.isBefore(dayjs(), "day")} className="w-full" />
                        </Form.Item>
                    ) : null}
                </Form>
            </Modal>
            <Modal title={`调用记录${logKey ? ` · ${logKey.name}` : ""}`} className={styles.surface} open={Boolean(logKey)} centered width={900} style={modalStyle} styles={{ body: bodyStyle }} footer={null} onCancel={closeLogs} destroyOnHidden>
                <div className={`${styles.surface} space-y-3`}>
                    <div className={styles.toolbar}>
                        <Typography.Text type="secondary">最近 100 条；HTTP 200 不代表业务成功。</Typography.Text>
                        <Button className={styles.action} loading={logsLoading} onClick={() => logKey && void loadLogs(logKey)}>
                            刷新
                        </Button>
                    </div>
                    {logsError ? <Alert type="error" showIcon title="调用记录加载失败" description={logsError} /> : null}
                    <Table<UserAPIKeyLog>
                        size="small"
                        rowKey="id"
                        loading={logsLoading}
                        dataSource={logs}
                        scroll={{ x: 720 }}
                        pagination={{ pageSize: 10, showSizeChanger: false }}
                        locale={{ emptyText: logsError ? "请刷新重试" : "暂无调用记录" }}
                        columns={[
                            { title: "时间", dataIndex: "createdAt", width: 175, render: (value: string) => formatTime(value, "尚未记录") },
                            { title: "方法", dataIndex: "method", width: 80 },
                            { title: "路径", dataIndex: "path", width: 270, ellipsis: true },
                            { title: "HTTP 状态", dataIndex: "status", width: 105 },
                            { title: "耗时", dataIndex: "durationMs", width: 90, render: (value: number) => `${value} ms` },
                        ]}
                    />
                </div>
            </Modal>
            <Modal
                title={`手动复制${manualKey ? ` · ${manualKey.name}` : ""}`}
                className={styles.surface}
                open={Boolean(manualKey)}
                centered
                width={620}
                style={modalStyle}
                styles={{ body: bodyStyle }}
                onCancel={closeManualKey}
                footer={
                    <Button className={styles.action} icon={<Copy className="size-3.5" />} loading={Boolean(copying)} onClick={() => void copyManualKey()}>
                        再次复制
                    </Button>
                }
                destroyOnHidden
            >
                <div className="space-y-3">
                    <Typography.Paragraph>浏览器未允许自动复制，请选中下方完整 Key 后手动复制。关闭此窗口将清除这里的明文。</Typography.Paragraph>
                    <Input.TextArea aria-label="手动复制完整 API Key" value={manualKey?.key || ""} readOnly autoSize={{ minRows: 3, maxRows: 5 }} onFocus={(event) => event.target.select()} className="font-mono" />
                </div>
            </Modal>
        </Modal>
    );
}
