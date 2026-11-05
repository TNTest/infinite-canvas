"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { Alert, App, Button, Checkbox, DatePicker, Form, Input, InputNumber, Modal, Popconfirm, Radio, Table, Tag, Typography } from "antd";
import dayjs, { type Dayjs } from "dayjs";
import { Copy, Plus, RefreshCw } from "lucide-react";

import { useCopyText } from "@/hooks/use-copy-text";
import { createUserAPIKey, deleteUserAPIKey, fetchUserAPIKeyLogs, fetchUserAPIKeys, type UserAPIKey, type UserAPIKeyLog, type UserAPIKeyScope } from "@/services/api/api-keys";
import { useUserStore } from "@/stores/use-user-store";

const MAX_API_KEYS = 100;
const SCOPE_LABELS: Record<UserAPIKeyScope, string> = { read: "只读", generate: "生成", write: "写入", delete: "删除" };

type APIKeyForm = {
    name: string;
    expiresAt?: Dayjs;
    scopes: UserAPIKeyScope[];
    rateLimit: number;
    requestLimit?: number | null;
};

function formatTime(value: string | null, fallback: string) {
    return value ? dayjs(value).format("YYYY-MM-DD HH:mm:ss") : fallback;
}

export function APIKeyModal({ onClose }: { onClose: () => void }) {
    const token = useUserStore((state) => state.token);
    const { message } = App.useApp();
    const copyText = useCopyText();
    const [form] = Form.useForm<APIKeyForm>();
    const active = useRef(true);
    const loadRequest = useRef(0);
    const logRequest = useRef(0);
    const [items, setItems] = useState<UserAPIKey[]>([]);
    const [total, setTotal] = useState(0);
    const [loading, setLoading] = useState(true);
    const [loadError, setLoadError] = useState("");
    const [creating, setCreating] = useState(false);
    const [deleting, setDeleting] = useState("");
    const [expiryMode, setExpiryMode] = useState("never");
    const [newKey, setNewKey] = useState<{ id: string; value: string } | null>(null);
    const [logKey, setLogKey] = useState<UserAPIKey | null>(null);
    const [logs, setLogs] = useState<UserAPIKeyLog[]>([]);
    const [logsLoading, setLogsLoading] = useState(false);
    const [logsError, setLogsError] = useState("");

    const loadKeys = useCallback(async () => {
        const request = ++loadRequest.current;
        setLoading(true);
        setLoadError("");
        try {
            const result = await fetchUserAPIKeys(token);
            if (!active.current || request !== loadRequest.current) return;
            setItems(result.items);
            setTotal(result.total);
        } catch (error) {
            if (active.current && request === loadRequest.current) setLoadError(error instanceof Error ? error.message : "加载 API Key 失败");
        } finally {
            if (active.current && request === loadRequest.current) setLoading(false);
        }
    }, [token]);

    useEffect(() => {
        active.current = true;
        void loadKeys();
        return () => {
            active.current = false;
        };
    }, [loadKeys]);

    async function createKey(values: APIKeyForm) {
        if (loading || creating || deleting || loadError || total >= MAX_API_KEYS || newKey) return;
        setCreating(true);
        try {
            const result = await createUserAPIKey(token, {
                name: values.name.trim(),
                expiresAt: expiryMode === "never" ? null : values.expiresAt!.toISOString(),
                scopes: values.scopes,
                rateLimit: values.rateLimit,
                requestLimit: values.requestLimit ?? null,
            });
            if (!active.current) return;
            setItems((current) => [result.apiKey, ...current]);
            setTotal((current) => current + 1);
            setNewKey({ id: result.apiKey.id, value: result.key });
            form.resetFields();
            setExpiryMode("never");
        } catch (error) {
            if (active.current) message.error(error instanceof Error ? error.message : "创建 API Key 失败");
        } finally {
            if (active.current) setCreating(false);
        }
    }

    async function deleteKey(id: string) {
        if (loading || creating || deleting) return;
        setDeleting(id);
        try {
            await deleteUserAPIKey(token, id);
            if (!active.current) return;
            setItems((current) => current.filter((item) => item.id !== id));
            setTotal((current) => Math.max(0, current - 1));
            if (newKey?.id === id) setNewKey(null);
            if (logKey?.id === id) closeLogs();
            message.success("API Key 已撤销");
        } catch (error) {
            if (active.current) message.error(error instanceof Error ? error.message : "撤销 API Key 失败");
        } finally {
            if (active.current) setDeleting("");
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
            if (active.current && request === logRequest.current) setLogs(result.items);
        } catch (error) {
            if (active.current && request === logRequest.current) setLogsError(error instanceof Error ? error.message : "加载调用记录失败");
        } finally {
            if (active.current && request === logRequest.current) setLogsLoading(false);
        }
    }

    function closeLogs() {
        ++logRequest.current;
        setLogKey(null);
        setLogs([]);
        setLogsError("");
        setLogsLoading(false);
    }

    return (
        <Modal title="API Key" open width={960} footer={null} onCancel={onClose} destroyOnHidden>
            <div className="space-y-4">
                <Typography.Paragraph type="secondary" className="!mb-0">
                    API Key 按所选权限，以你的身份调用业务 API，算力点仍从你的账号扣除。即使账号是管理员，Key 也不能访问管理后台、密钥管理、个人模型与存储配置或 Bridge 管理。请妥善保管，不要分享给他人。
                </Typography.Paragraph>
                {newKey ? (
                    <Alert
                        type="success"
                        showIcon
                        title="创建成功，请立即复制保存"
                        description={
                            <div className="mt-2 space-y-2">
                                <div>完整 Key 仅显示这一次，关闭后无法再次查看。</div>
                                <Input.TextArea value={newKey.value} readOnly autoSize={{ minRows: 2, maxRows: 3 }} className="font-mono" aria-label="新创建的 API Key" />
                                <div className="flex flex-wrap gap-2">
                                    <Button icon={<Copy className="size-3.5" />} onClick={() => copyText(newKey.value, "API Key 已复制")}>
                                        复制 Key
                                    </Button>
                                    <Button onClick={() => setNewKey(null)}>我已保存</Button>
                                </div>
                            </div>
                        }
                    />
                ) : null}
                <Form
                    form={form}
                    layout="vertical"
                    initialValues={{ scopes: ["read", "generate"], rateLimit: 60, requestLimit: null }}
                    onFinish={createKey}
                    disabled={loading || creating || Boolean(deleting) || Boolean(loadError) || total >= MAX_API_KEYS || Boolean(newKey)}
                >
                    <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true, message: "请输入 Key 名称" }]}>
                        <Input placeholder="例如：自动化脚本" maxLength={100} autoComplete="off" />
                    </Form.Item>
                    <Form.Item name="scopes" label="权限" rules={[{ type: "array", required: true, min: 1, message: "请至少选择一项权限" }]} extra="保存已有数据需要同时勾选只读和写入；写入不含删除，查询生成结果需要只读权限。">
                        <Checkbox.Group options={Object.entries(SCOPE_LABELS).map(([value, label]) => ({ value, label }))} />
                    </Form.Item>
                    <div className="grid grid-cols-1 gap-x-4 sm:grid-cols-2">
                        <Form.Item name="rateLimit" label="每分钟请求上限（RPM）" rules={[{ required: true, type: "integer", min: 1, max: 6000, message: "请输入 1 至 6000 之间的整数" }]}>
                            <InputNumber min={1} max={6000} precision={0} className="!w-full" />
                        </Form.Item>
                        <Form.Item name="requestLimit" label="累计请求次数上限" rules={[{ type: "integer", min: 1, max: Number.MAX_SAFE_INTEGER, message: "请输入正整数，或留空表示不限" }]} extra="已认证请求尝试计次，权限拒绝和业务失败也计次。">
                            <InputNumber min={1} max={Number.MAX_SAFE_INTEGER} precision={0} placeholder="留空表示不限" className="!w-full" />
                        </Form.Item>
                    </div>
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
                            <DatePicker showTime format="YYYY-MM-DD HH:mm:ss" disabledDate={(date) => date.isBefore(dayjs(), "day")} className="w-full" />
                        </Form.Item>
                    ) : null}
                    <div className="flex flex-wrap items-center justify-between gap-2">
                        <Button type="primary" htmlType="submit" icon={<Plus className="size-3.5" />} loading={creating}>
                            创建 Key
                        </Button>
                        <Typography.Text type="secondary">
                            已创建 {total} / {MAX_API_KEYS}，过期 Key 仍占名额，撤销后可重新创建。
                        </Typography.Text>
                    </div>
                </Form>
                {loadError ? (
                    <Alert
                        type="error"
                        showIcon
                        title={loadError}
                        action={
                            <Button size="small" onClick={() => void loadKeys()}>
                                重试
                            </Button>
                        }
                    />
                ) : null}
                <div className="flex items-center justify-between">
                    <Typography.Text strong>我的 API Key</Typography.Text>
                    <Button size="small" icon={<RefreshCw className="size-3.5" />} loading={loading} disabled={creating || Boolean(deleting)} onClick={() => void loadKeys()}>
                        刷新
                    </Button>
                </div>
                <Table<UserAPIKey>
                    size="small"
                    rowKey="id"
                    loading={loading}
                    dataSource={items}
                    scroll={{ x: 1200 }}
                    pagination={{ pageSize: 10, showSizeChanger: false }}
                    locale={{ emptyText: loadError ? "加载失败，请重试" : "还没有 API Key，创建后即可调用 API" }}
                    columns={[
                        { title: "名称", dataIndex: "name", width: 130, ellipsis: true },
                        { title: "Key", dataIndex: "prefix", width: 150, render: (value: string) => <span className="font-mono text-xs">{value}…</span> },
                        {
                            title: "权限",
                            dataIndex: "scopes",
                            width: 160,
                            render: (scopes: UserAPIKeyScope[]) => (
                                <div className="flex flex-wrap gap-y-1">
                                    {scopes.map((scope) => (
                                        <Tag key={scope}>{SCOPE_LABELS[scope]}</Tag>
                                    ))}
                                </div>
                            ),
                        },
                        {
                            title: "请求用量 / 限制",
                            width: 165,
                            render: (_, item) => (
                                <div>
                                    <div>
                                        {item.requestCount.toLocaleString()} / {item.requestLimit === null ? "不限" : item.requestLimit.toLocaleString()} 次
                                    </div>
                                    <Typography.Text type="secondary">{item.rateLimit} RPM</Typography.Text>
                                </div>
                            ),
                        },
                        {
                            title: "有效期",
                            width: 180,
                            render: (_, item) => (
                                <div>
                                    <div>{formatTime(item.expiresAt, "永久有效")}</div>
                                    {item.expiresAt && !dayjs(item.expiresAt).isAfter(dayjs()) ? <Tag color="error">已过期</Tag> : null}
                                </div>
                            ),
                        },
                        { title: "最近使用", dataIndex: "lastUsedAt", width: 170, render: (value: string | null) => formatTime(value, "尚未使用") },
                        {
                            title: "操作",
                            width: 145,
                            fixed: "right",
                            render: (_, item) => (
                                <div className="flex items-center">
                                    <Button type="link" size="small" onClick={() => void loadLogs(item)}>
                                        调用记录
                                    </Button>
                                    <Popconfirm
                                        title={`撤销「${item.name}」？`}
                                        description="撤销后该 Key 将立即失效。"
                                        okText="撤销"
                                        cancelText="取消"
                                        disabled={loading || creating || Boolean(deleting)}
                                        okButtonProps={{ danger: true }}
                                        onConfirm={() => deleteKey(item.id)}
                                    >
                                        <Button type="link" danger size="small" loading={deleting === item.id} disabled={loading || creating || Boolean(deleting)}>
                                            撤销
                                        </Button>
                                    </Popconfirm>
                                </div>
                            ),
                        },
                    ]}
                />
                <Typography.Paragraph type="secondary" className="!mb-0 text-xs">
                    调用时添加请求头：<Typography.Text code>Authorization: Bearer &lt;你的 API Key&gt;</Typography.Text>
                </Typography.Paragraph>
                <Modal title={`调用记录${logKey ? ` · ${logKey.name}` : ""}`} open={Boolean(logKey)} width={900} footer={null} onCancel={closeLogs} destroyOnHidden>
                    <div className="space-y-3">
                        <div className="flex flex-wrap items-center justify-between gap-2">
                            <Typography.Text type="secondary">最近 100 条记录；HTTP 状态为 200 不代表业务处理成功。</Typography.Text>
                            <Button size="small" loading={logsLoading} onClick={() => logKey && void loadLogs(logKey)}>
                                刷新
                            </Button>
                        </div>
                        {logsError ? <Alert type="error" showIcon title={logsError} /> : null}
                        <Table<UserAPIKeyLog>
                            size="small"
                            rowKey="id"
                            loading={logsLoading}
                            dataSource={logs}
                            scroll={{ x: 720 }}
                            pagination={{ pageSize: 10, showSizeChanger: false }}
                            locale={{ emptyText: logsError ? "加载失败，请刷新重试" : "暂无调用记录" }}
                            columns={[
                                { title: "时间", dataIndex: "createdAt", width: 175, render: (value: string) => formatTime(value, "—") },
                                { title: "方法", dataIndex: "method", width: 80 },
                                { title: "路径", dataIndex: "path", width: 270, ellipsis: true },
                                { title: "HTTP 状态", dataIndex: "status", width: 105 },
                                { title: "耗时", dataIndex: "durationMs", width: 90, render: (value: number) => `${value} ms` },
                            ]}
                        />
                    </div>
                </Modal>
            </div>
        </Modal>
    );
}
