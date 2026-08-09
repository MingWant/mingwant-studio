import { useEffect, useMemo, useRef, useState } from "react";
import { App, Button, Drawer, Input, Select, Skeleton } from "antd";
import { BookOpenCheck, Copy, Search, Sparkles, Workflow } from "lucide-react";
import { useNavigate } from "react-router";

import { CollectionGrid, ListToolbar, PageHeader, WorkspacePage } from "@/components/layout/workspace-page";
import { WorkspaceState } from "@/components/layout/workspace-state";
import { useCopyText } from "@/hooks/use-copy-text";
import { createCanvasNode } from "@/lib/canvas/canvas-project-domain";
import { extractMingWantPromptVariables, loadMingWantPrompt, MINGWANT_PROMPT_CATEGORIES, mingwantPromptTemplates, type MingWantPromptCategory, type MingWantPromptTemplate } from "@/lib/mingwant-prompt-library";
import { createCanvasProjectWithRemoteSync } from "@/services/user-data-sync";
import { useCanvasStore } from "@/stores/canvas/use-canvas-store";
import { CanvasNodeType } from "@/types/canvas";

type CategoryFilter = "all" | MingWantPromptCategory;

export default function PromptsPage() {
    const navigate = useNavigate();
    const copyText = useCopyText();
    const { message } = App.useApp();
    const [query, setQuery] = useState("");
    const [category, setCategory] = useState<CategoryFilter>("all");
    const [activeTemplate, setActiveTemplate] = useState<MingWantPromptTemplate | null>(null);
    const [activeContent, setActiveContent] = useState("");
    const [contentLoading, setContentLoading] = useState(false);
    const [copyingId, setCopyingId] = useState<string | null>(null);
    const [handoffId, setHandoffId] = useState<string | null>(null);
    const handoffPendingRef = useRef(false);
    const canvasHydrated = useCanvasStore((state) => state.hydrated);

    const templates = useMemo(() => {
        const normalized = query.trim().toLowerCase();
        return mingwantPromptTemplates.filter((template) => {
            if (category !== "all" && template.category !== category) return false;
            return !normalized || `${template.name} ${template.categoryLabel} ${template.description}`.toLowerCase().includes(normalized);
        });
    }, [category, query]);

    useEffect(() => {
        if (!activeTemplate) {
            setActiveContent("");
            setContentLoading(false);
            return;
        }
        let cancelled = false;
        setActiveContent("");
        setContentLoading(true);
        void loadMingWantPrompt(activeTemplate)
            .then((content) => {
                if (!cancelled) setActiveContent(content);
            })
            .catch((error) => {
                if (!cancelled) message.error(error instanceof Error ? error.message : "提示词读取失败");
            })
            .finally(() => {
                if (!cancelled) setContentLoading(false);
            });
        return () => {
            cancelled = true;
        };
    }, [activeTemplate, message]);

    const copyTemplate = async (template: MingWantPromptTemplate) => {
        setCopyingId(template.id);
        try {
            const content = await loadMingWantPrompt(template);
            await copyText(content, `已复制“${template.name}”`);
        } catch (error) {
            message.error(error instanceof Error ? error.message : "提示词读取失败");
        } finally {
            setCopyingId(null);
        }
    };

    const variables = useMemo(() => extractMingWantPromptVariables(activeContent), [activeContent]);

    const useTemplateInCanvas = async (template: MingWantPromptTemplate, loadedContent?: string) => {
        if (handoffPendingRef.current) return;
        handoffPendingRef.current = true;
        setHandoffId(template.id);
        try {
            const content = loadedContent ?? await loadMingWantPrompt(template);
            if (!content.trim()) throw new Error("提示词内容为空，无法创建画布");
            const templateVariables = extractMingWantPromptVariables(content);
            const baseNode = createCanvasNode(CanvasNodeType.Text, { x: 500, y: 360 }, { content, prompt: content, status: "success", fontSize: 14 });
            const node = {
                ...baseNode,
                title: `提示词 · ${template.name}${templateVariables.length ? ` · ${templateVariables.length} 个变量` : ""}`,
                position: { x: 180, y: 100 },
                width: 640,
                height: 520,
            };
            const { id, syncError } = await createCanvasProjectWithRemoteSync(`提示词 · ${template.name}`, undefined, { nodes: [node], viewport: { x: 0, y: 0, k: 1 } });
            if (syncError) message.warning("画布已在本地创建，但云端同步暂时失败；模板内容不会丢失");
            navigate(`/canvas/${id}`);
        } catch (error) {
            message.error(error instanceof Error ? error.message : "提示词无法交接到画布");
        } finally {
            handoffPendingRef.current = false;
            setHandoffId(null);
        }
    };

    return (
        <>
            <WorkspacePage grid>
                <PageHeader
                    icon="skills"
                    title="明想提示词库"
                    description="保留你的原始模板；先让文本模型生成最终图片或视频提示词，再连接对应生成节点。"
                    meta={<span className="text-xs text-foreground/45">{mingwantPromptTemplates.length} 份模板</span>}
                    actions={<Button icon={<Workflow className="size-4" />} onClick={() => navigate("/canvas")}>管理画布</Button>}
                />

                <ListToolbar
                    active={Boolean(query || category !== "all")}
                    onReset={() => {
                        setQuery("");
                        setCategory("all");
                    }}
                    trailing={<span className="text-xs text-foreground/42">当前 {templates.length} 份</span>}
                >
                    <div className="min-w-[220px] flex-1 sm:max-w-[420px]">
                        <Input
                            allowClear
                            className="w-full"
                            prefix={<Search className="size-4 text-foreground/40" />}
                            value={query}
                            placeholder="搜索名称、分类或用途"
                            onChange={(event) => setQuery(event.target.value)}
                        />
                    </div>
                    <Select<CategoryFilter>
                        className="w-full sm:w-44"
                        value={category}
                        options={[
                            { label: "全部分类", value: "all" },
                            ...MINGWANT_PROMPT_CATEGORIES.map((value) => ({ label: categoryLabel(value), value })),
                        ]}
                        onChange={setCategory}
                    />
                </ListToolbar>

                {templates.length ? (
                    <CollectionGrid className="sm:grid-cols-[repeat(auto-fill,minmax(280px,1fr))] xl:grid-cols-[repeat(auto-fill,minmax(300px,1fr))]">
                        {templates.map((template) => (
                            <PromptCard
                                key={template.id}
                                template={template}
                                copying={copyingId === template.id}
                                handingOff={handoffId === template.id}
                                handoffDisabled={!canvasHydrated || Boolean(handoffId)}
                                onOpen={() => setActiveTemplate(template)}
                                onCopy={() => void copyTemplate(template)}
                                onUse={() => void useTemplateInCanvas(template)}
                            />
                        ))}
                    </CollectionGrid>
                ) : (
                    <WorkspaceState icon="empty" title="没有匹配的提示词" description="换一个关键词或分类继续查找。" />
                )}
            </WorkspacePage>

            <Drawer
                open={Boolean(activeTemplate)}
                size="large"
                destroyOnHidden
                title={activeTemplate ? `提示词 · ${activeTemplate.name}` : "提示词详情"}
                extra={activeTemplate ? <div className="flex flex-wrap items-center justify-end gap-2"><Button disabled={!activeContent} icon={<Copy className="size-4" />} onClick={() => void copyTemplate(activeTemplate)}>复制全文</Button><Button type="primary" disabled={!activeContent || !canvasHydrated || Boolean(handoffId)} loading={handoffId === activeTemplate.id} icon={<Workflow className="size-4" />} onClick={() => void useTemplateInCanvas(activeTemplate, activeContent)}>在新画布使用</Button></div> : null}
                onClose={() => setActiveTemplate(null)}
            >
                {activeTemplate ? (
                    <div className="space-y-4">
                        <div className="border-b border-border pb-4">
                            <div className="flex flex-wrap items-center gap-2">
                                <span className="rounded bg-foreground px-2 py-1 text-[10px] font-medium text-background">明想库</span>
                                <span className="rounded bg-foreground/[.06] px-2 py-1 text-[10px] font-medium text-foreground/55">{activeTemplate.categoryLabel}</span>
                                <span className="text-[10px] text-foreground/38">文本模型模板</span>
                            </div>
                            <p className="mt-3 text-sm leading-6 text-foreground/58">{activeTemplate.description}</p>
                            {activeContent ? <p className="mt-2 text-xs tabular-nums text-foreground/38">{activeContent.length.toLocaleString("zh-CN")} 个字符</p> : null}
                        </div>

                        {variables.length ? (
                            <section>
                                <h3 className="text-xs font-semibold">需要替换的变量</h3>
                                <div className="mt-2 flex flex-wrap gap-1.5">
                                    {variables.map((variable) => <span key={variable} className="rounded border border-border bg-foreground/[.035] px-2 py-1 text-[11px] text-foreground/62">{"{{"}{variable}{"}}"}</span>)}
                                </div>
                            </section>
                        ) : null}

                        <section>
                            <div className="mb-2 flex items-center gap-2 text-xs font-semibold"><BookOpenCheck className="size-4 text-foreground/45" />原始内容</div>
                            {contentLoading ? (
                                <Skeleton active paragraph={{ rows: 16 }} />
                            ) : (
                                <pre className="thin-scrollbar max-h-[calc(100vh-280px)] overflow-auto whitespace-pre-wrap break-words rounded-lg border border-border bg-foreground/[.025] p-4 text-xs leading-6 text-foreground/72">{activeContent}</pre>
                            )}
                        </section>
                    </div>
                ) : null}
            </Drawer>
        </>
    );
}

function PromptCard({ template, copying, handingOff, handoffDisabled, onOpen, onCopy, onUse }: { template: MingWantPromptTemplate; copying: boolean; handingOff: boolean; handoffDisabled: boolean; onOpen: () => void; onCopy: () => void; onUse: () => void }) {
    return (
        <article className="app-collection-card flex h-full flex-col p-4">
            <button type="button" className="min-w-0 flex-1 text-left" onClick={onOpen}>
                <div className="flex items-center justify-between gap-3">
                    <span className="inline-flex items-center gap-1.5 text-[10px] font-semibold text-[var(--workspace-accent)]"><Sparkles className="size-3.5" />{template.categoryLabel}</span>
                    <span className="text-[9px] font-medium text-foreground/32">明想库</span>
                </div>
                <h2 className="mt-3 line-clamp-1 text-sm font-semibold">{template.name}</h2>
                <p className="mt-2 line-clamp-3 min-h-[60px] text-xs leading-5 text-foreground/52">{template.description}</p>
            </button>
            <div className="mt-4 flex items-center gap-2 border-t border-border/70 pt-3">
                <Button onClick={onOpen}>查看</Button>
                <Button loading={copying} icon={<Copy className="size-3.5" />} onClick={onCopy}>复制</Button>
                <Button className="min-w-0 flex-1" type="primary" disabled={handoffDisabled} loading={handingOff} icon={<Workflow className="size-3.5" />} onClick={onUse}>用于画布</Button>
            </div>
        </article>
    );
}

function categoryLabel(category: MingWantPromptCategory) {
    return mingwantPromptTemplates.find((template) => template.category === category)?.categoryLabel || category;
}
