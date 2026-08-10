import { Select } from "antd";

import type { CanvasVideoEditOperation } from "@/types/canvas";

const VIDEO_OPERATION_OPTIONS: Array<{ label: string; value: CanvasVideoEditOperation }> = [
    { label: "文生视频", value: "text_to_video" },
    { label: "图生视频", value: "image_to_video" },
    { label: "多参考图（实验）", value: "reference_to_video" },
    { label: "视频续写", value: "extend" },
    { label: "局部修改", value: "inpaint" },
    { label: "元素替换", value: "replace_element" },
    { label: "运镜调整", value: "camera_motion" },
    { label: "风格迁移", value: "style_transfer" },
    { label: "音频生视频", value: "audio_to_video" },
    { label: "版本对比", value: "compare_versions" },
];

export function CanvasVideoOperationSelect({ value, operations, onChange, className }: { value?: CanvasVideoEditOperation; operations?: string[]; onChange: (value: CanvasVideoEditOperation) => void; className?: string }) {
    return (
        <Select
            size="small"
            className={className || "canvas-compact-control canvas-control-select !h-9 !w-full"}
            value={value}
            options={videoOperationOptionsForCapability(operations, value)}
            placement="bottomLeft"
            popupMatchSelectWidth={false}
            styles={{ popup: { root: { minWidth: 180, maxWidth: 260 } } }}
            popupRender={(menu) => (
                <div data-canvas-no-zoom onMouseDown={(event) => event.stopPropagation()} onPointerDown={(event) => event.stopPropagation()}>
                    {menu}
                </div>
            )}
            onChange={onChange}
        />
    );
}

function videoOperationOptionsForCapability(operations: string[] | undefined, current?: CanvasVideoEditOperation) {
    const configured = operations?.length ? operations : VIDEO_OPERATION_OPTIONS.map((item) => item.value);
    const options = configured.map((item) => ({ label: videoOperationLabel(item), value: item as CanvasVideoEditOperation }));
    if (current === "concat") return [...options, { label: "合并成片", value: "concat" as const }];
    if (current && !configured.some((item) => item.toLowerCase() === current.toLowerCase())) {
        return [{ label: `${videoOperationLabel(current)}（当前模型不支持）`, value: current, disabled: true }, ...options];
    }
    return options;
}

function videoOperationLabel(value: string) {
    return VIDEO_OPERATION_OPTIONS.find((item) => item.value === value)?.label || value;
}
