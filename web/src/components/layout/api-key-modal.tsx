"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { Alert, App, Button, DatePicker, Form, Input, Modal, Popconfirm, Radio, Table, Tag, Typography } from "antd";
import dayjs, { type Dayjs } from "dayjs";
import { Copy, Plus, RefreshCw } from "lucide-react";

import { useCopyText } from "@/hooks/use-copy-text";
import { createUserAPIKey, deleteUserAPIKey, fetchUserAPIKeys, type UserAPIKey } from "@/services/api/api-keys";
import { useUserStore } from "@/stores/use-user-store";

const MAX_API_KEYS = 100;

function formatTime(value: string | null, fallback: string) {
    return value ? dayjs(value).format("YYYY-MM-DD HH:mm:ss") : fallback;
}

export function APIKeyModal({ onClose }: { onClose: () => void }) {
    const token = useUserStore((state) => state.token);
    const { message } = App.useApp();
    const copyText = useCopyText();
    const [form] = Form.useForm<{ name: string; expiresAt?: Dayjs }>();
    const active = useRef(true);
    const loadRequest = useRef(0);
    const [items, setItems] = useState<UserAPIKey[]>([]);
    const [total, setTotal] = useState(0);
    const [loading, setLoading] = useState(true);
    const [loadError, setLoadError] = useState("");
    const [creating, setCreating] = useState(false);
    const [deleting, setDeleting] = useState("");
    const [expiryMode, setExpiryMode] = useState("never");
    const [newKey, setNewKey] = useState<{ id: string; value: string } | null>(null);

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

    async function createKey(values: { name: string; expiresAt?: Dayjs }) {
        if (loading || creating || deleting || loadError || total >= MAX_API_KEYS || newKey) return;
        setCreating(true);
        try {
            const result = await createUserAPIKey(token, { name: values.name.trim(), expiresAt: expiryMode === "never" ? null : values.expiresAt!.toISOString() });
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
            message.success("API Key 已撤销");
        } catch (error) {
            if (active.current) message.error(error instanceof Error ? error.message : "撤销 API Key 失败");
        } finally {
            if (active.current) setDeleting("");
        }
    }

    return (
        <Modal title="API Key" open width={820} footer={null} onCancel={onClose} destroyOnHidden>
            <div className="space-y-4">
                <Typography.Paragraph type="secondary" className="!mb-0">
                    API Key 可使用你的身份调用本服务中你有权访问的全部 API，调用按你的账号权限和算力点余额结算。请妥善保管，不要分享给他人。
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
                <Form form={form} layout="vertical" onFinish={createKey} disabled={loading || creating || Boolean(deleting) || Boolean(loadError) || total >= MAX_API_KEYS || Boolean(newKey)}>
                    <Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true, message: "请输入 Key 名称" }]}>
                        <Input placeholder="例如：自动化脚本" maxLength={100} autoComplete="off" />
                    </Form.Item>
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
                    scroll={{ x: 680 }}
                    pagination={{ pageSize: 10, showSizeChanger: false }}
                    locale={{ emptyText: loadError ? "加载失败，请重试" : "还没有 API Key，创建后即可调用 API" }}
                    columns={[
                        { title: "名称", dataIndex: "name", width: 130, ellipsis: true },
                        { title: "Key", dataIndex: "prefix", width: 150, render: (value: string) => <span className="font-mono text-xs">{value}…</span> },
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
                            width: 75,
                            fixed: "right",
                            render: (_, item) => (
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
                            ),
                        },
                    ]}
                />
                <Typography.Paragraph type="secondary" className="!mb-0 text-xs">
                    调用时添加请求头：<Typography.Text code>Authorization: Bearer &lt;你的 API Key&gt;</Typography.Text>
                </Typography.Paragraph>
            </div>
        </Modal>
    );
}
