import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";

import { AGENT_PROMPT, loadConfig, type CanvasAgentConfig, VERSION } from "./config.js";
import { toolDescriptions, toolInputSchemas, toolNames, type ToolName } from "./schemas.js";
import { parseToolInput } from "./tools.js";

type CanvasAgentToolResponse = { ok?: boolean; result?: unknown; error?: string };

export async function startMcpServer() {
    const config = loadConfig(true);
    const server = new McpServer({ name: "canvas-agent", version: VERSION }, { instructions: AGENT_PROMPT });
    toolNames.forEach((name) => registerCanvasTool(server, config, name));
    await server.connect(new StdioServerTransport());
}

function registerCanvasTool(server: McpServer, config: CanvasAgentConfig, name: ToolName) {
    const schema = toolInputSchemas[name];
    server.registerTool(name, { description: toolDescriptions[name], inputSchema: schema.shape }, async (input: unknown) => {
        const result = await postCanvasAgentTool(config, name, parseToolInput(name, input));
        if (name === "canvas_get_image_annotations") return imageAnnotationToolResult(result);
        return { content: [{ type: "text" as const, text: JSON.stringify(result, null, 2) }] };
    });
}

function imageAnnotationToolResult(result: unknown) {
    const value = result && typeof result === "object" && !Array.isArray(result) ? result as Record<string, unknown> : {};
    const annotations = Array.isArray(value.annotations) ? value.annotations.filter((item): item is Record<string, unknown> => Boolean(item) && typeof item === "object" && !Array.isArray(item)) : [];
    const summary = annotations.map((item) => {
        const image = item.image && typeof item.image === "object" && !Array.isArray(item.image) ? item.image as Record<string, unknown> : {};
        return { ...item, image: { mimeType: image.mimeType, includedAsMcpImage: typeof image.dataUrl === "string" } };
    });
    const images = annotations.flatMap((item) => {
        const image = item.image && typeof item.image === "object" && !Array.isArray(item.image) ? item.image as Record<string, unknown> : {};
        const parsed = parseImageDataUrl(image.dataUrl);
        return parsed ? [{ type: "image" as const, data: parsed.data, mimeType: parsed.mimeType }] : [];
    });
    return { content: [{ type: "text" as const, text: JSON.stringify({ annotations: summary }, null, 2) }, ...images] };
}

function parseImageDataUrl(value: unknown) {
    if (typeof value !== "string") return null;
    const match = /^data:(image\/[a-z0-9.+-]+);base64,(.+)$/is.exec(value);
    return match ? { mimeType: match[1], data: match[2] } : null;
}

async function postCanvasAgentTool(config: CanvasAgentConfig, name: ToolName, input: unknown) {
    const res = await fetch(`${config.url}/api/tools`, { method: "POST", headers: { "content-type": "application/json", "x-canvas-agent-token": config.token }, body: JSON.stringify({ name, input }) });
    const body = (await res.json()) as CanvasAgentToolResponse;
    if (!body.ok) throw new Error(body.error || "tool call failed");
    return body.result;
}
