const MAX_AGENT_IMAGE_EDGE = 2048;
const MAX_AGENT_IMAGE_DATA_URL_LENGTH = 3_800_000;

export async function buildCanvasAgentImagePreview(dataUrl: string) {
    const image = await loadImage(dataUrl);
    const scale = Math.min(1, MAX_AGENT_IMAGE_EDGE / Math.max(image.naturalWidth, image.naturalHeight));
    const canvas = document.createElement("canvas");
    canvas.width = Math.max(1, Math.round(image.naturalWidth * scale));
    canvas.height = Math.max(1, Math.round(image.naturalHeight * scale));
    const context = canvas.getContext("2d");
    if (!context) throw new Error("无法创建标注预览");
    context.fillStyle = "#fff";
    context.fillRect(0, 0, canvas.width, canvas.height);
    context.drawImage(image, 0, 0, canvas.width, canvas.height);
    // MCP 只需要看清标记关系；限制边长和单图体积，避免多标注读取超过本地 Agent 的 30MB 请求上限。
    let quality = 0.9;
    let preview = canvas.toDataURL("image/jpeg", quality);
    while (preview.length > MAX_AGENT_IMAGE_DATA_URL_LENGTH && quality > 0.42) {
        quality -= 0.12;
        preview = canvas.toDataURL("image/jpeg", quality);
    }
    return preview;
}

function loadImage(dataUrl: string) {
    return new Promise<HTMLImageElement>((resolve, reject) => {
        const image = new Image();
        image.onload = () => resolve(image);
        image.onerror = () => reject(new Error("标注图片预览读取失败"));
        image.src = dataUrl;
    });
}
