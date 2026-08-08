const QUOTA_PATTERN = /(?:you\s+still\s+have\s+|剩余|还剩|仅剩)\s*(\d+)\s*(?:points?|点)/i;
const QUOTA_ERROR_PATTERN = /(?:no|not enough|insufficient|out of|zero)\s+(?:points?|credits?)|(?:积分|点数).{0,12}(?:不足|用完|耗尽|没有)/i;

function parseJSON(value) {
    if (value && typeof value === "object") return value;
    if (typeof value !== "string" || !value.trim()) return null;
    try {
        return JSON.parse(value);
    } catch {
        return null;
    }
}

function textFromBlock(block) {
    return block?.content?.text_block?.text || "";
}

function blocksFromMessage(message) {
    const content = parseJSON(message?.content);
    if (Array.isArray(content)) return content;
    return Array.isArray(message?.content_block) ? message.content_block : [];
}

function eventData(event) {
    return parseJSON(event.data) || {};
}

export function parseSSE(text) {
    const events = [];
    let current = { id: "", event: "message", data: [] };
    const flush = () => {
        if (!current.event && current.data.length === 0) return;
        events.push({ id: current.id, event: current.event || "message", data: current.data.join("\n") });
        current = { id: "", event: "message", data: [] };
    };
    for (const line of String(text || "").replaceAll("\r\n", "\n").split("\n")) {
        if (line === "") {
            flush();
            continue;
        }
        if (line.startsWith("id:")) current.id = line.slice(3).trim();
        else if (line.startsWith("event:")) current.event = line.slice(6).trim();
        else if (line.startsWith("data:")) current.data.push(line.slice(5).trimStart());
    }
    flush();
    return events;
}

function collectCompletionTexts(events) {
    const texts = [];
    for (const event of events) {
        const data = eventData(event);
        if (event.event === "FULL_MSG_NOTIFY") {
            texts.push(...blocksFromMessage(data.message).map(textFromBlock).filter(Boolean));
        } else if (event.event === "STREAM_MSG_NOTIFY") {
            texts.push(...(Array.isArray(data.content?.content_block) ? data.content.content_block : []).map(textFromBlock).filter(Boolean));
        } else if (event.event === "STREAM_CHUNK") {
            for (const operation of Array.isArray(data.patch_op) ? data.patch_op : []) {
                const blocks = operation.patch_value?.content_block;
                texts.push(...(Array.isArray(blocks) ? blocks : []).map(textFromBlock).filter(Boolean));
            }
        }
    }
    return texts;
}

export function parseRemainingPoints(text) {
    const match = QUOTA_PATTERN.exec(String(text || ""));
    return match ? Number.parseInt(match[1], 10) : null;
}

export function classifyDolaText(text) {
    const value = String(text || "");
    if (QUOTA_ERROR_PATTERN.test(value)) return "quota_exhausted";
    if (/(?:captcha|verify you are human|验证不是机器人|验证码)/i.test(value)) return "verification_required";
    if (/(?:sign in|log in|login|登录|重新登录)/i.test(value)) return "needs_login";
    return "";
}

export function parseDolaCompletion(text) {
    const events = parseSSE(text);
    const texts = collectCompletionTexts(events);
    let conversationId = "";
    let localMessageId = "";
    let questionId = "";
    let providerMessageId = "";
    let accepted = false;
    let finished = false;
    for (const event of events) {
        const data = eventData(event);
        if (event.event === "SSE_ACK") {
            conversationId = String(data.ack_client_meta?.conversation_id || "").trim();
            const query = Array.isArray(data.query_list) ? data.query_list[0] : null;
            localMessageId = String(query?.local_message_id || "").trim();
            questionId = String(query?.question_id || "").trim();
            accepted = Boolean(conversationId);
        } else if (event.event === "FULL_MSG_NOTIFY") {
            const message = data.message || {};
            providerMessageId = String(message.message_id || providerMessageId).trim();
            localMessageId = String(message.local_message_id || localMessageId).trim();
        } else if (event.event === "SSE_REPLY_END" && Number(data.end_type) === 3) {
            finished = true;
        }
    }
    const combinedText = texts.join("\n");
    const quotaRemaining = parseRemainingPoints(combinedText);
    const classification = classifyDolaText(combinedText);
    return {
        accepted,
        finished,
        conversationId,
        localMessageId,
        questionId,
        providerMessageId,
        text: combinedText,
        quotaRemaining,
        quotaExhaustedAfterAccept: quotaRemaining === 0,
        quotaBeforeAccept: !accepted && (classification === "quota_exhausted" || quotaRemaining === 0),
        errorCode: accepted ? "" : classification,
    };
}

function responseMessages(payload) {
    const root = parseJSON(payload) || {};
    return root?.downlink_body?.pull_singe_chain_downlink_body?.messages || root?.messages || [];
}

function safeURL(value) {
    if (typeof value !== "string" || !/^https?:\/\//i.test(value) || value.length > 16_384) return "";
    return value;
}

function decodeURL(value) {
    if (typeof value !== "string" || value.length < 16) return "";
    try {
        const decoded = Buffer.from(value, "base64").toString("utf8");
        return safeURL(decoded);
    } catch {
        return "";
    }
}

function videoModelURLs(video) {
    const result = [];
    const model = parseJSON(video?.video_model);
    const list = model?.video_list && typeof model.video_list === "object" ? model.video_list : {};
    for (const item of Object.values(list)) {
        for (const key of ["main_url", "backup_url_1", "backup_url"]) {
            const url = decodeURL(item?.[key]);
            if (url) result.push(url);
        }
    }
    return result;
}

function creationFromBlock(block, messageIndex) {
    const creations = block?.content?.creation_block?.creations;
    if (!Array.isArray(creations)) return null;
    for (const creation of creations) {
        const video = creation?.video;
        const vid = String(video?.vid || "").trim();
        if (!vid) continue;
        const urls = [safeURL(video.download_url), ...videoModelURLs(video)].filter(Boolean);
        return {
            creationId: String(creation.id || "").trim(),
            vid,
            status: video.status,
            duration: Number(video.duration) || 0,
            width: Number(video.width) || 0,
            height: Number(video.height) || 0,
            videoType: String(video.video_type || "").trim(),
            downloadUrl: urls[0] || "",
            downloadUrls: [...new Set(urls)],
            messageIndex: Number(messageIndex) || 0,
        };
    }
    return null;
}

export function extractCreationFromChain(payload) {
    let latest = null;
    for (const message of responseMessages(payload)) {
        for (const block of blocksFromMessage(message)) {
            if (Number(block?.block_type) !== 2074) continue;
            const creation = creationFromBlock(block, message.index_in_conv);
            if (creation) latest = creation;
        }
    }
    return latest;
}

export function maxChainIndex(payload) {
    const indexes = responseMessages(payload).map((message) => Number(message.index_in_conv)).filter(Number.isFinite);
    return indexes.length ? Math.max(...indexes) : null;
}

export function isCreationReady(creation) {
    return Boolean(creation?.vid) && (Number(creation.status) === 3 || creation.downloadUrls?.length > 0);
}

export function sanitizeProviderError(error) {
    const code = String(error?.code || "provider_error").replace(/[^a-z0-9_.-]/gi, "_").slice(0, 80) || "provider_error";
    const message = String(error?.publicMessage || error?.message || "Dola 请求失败").replace(/\s+/g, " ").trim().slice(0, 300);
    return { code, message };
}

export function isTransientBrowserError(error) {
    const code = String(error?.code || "");
    if (["needs_login", "verification_required", "quota_exhausted", "provider_state_uncertain"].includes(code)) return false;
    const message = String(error?.message || "").toLowerCase();
    return Boolean(error?.transient) || /timeout|timed out|429|502|503|504|connection|target closed|net::/i.test(`${code} ${message}`);
}
