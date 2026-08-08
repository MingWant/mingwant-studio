import http from "node:http";
import { createReadStream } from "node:fs";
import { readFile, stat } from "node:fs/promises";
import { randomUUID } from "node:crypto";
import { fileURLToPath, URL } from "node:url";
import { publicAccount } from "./store.mjs";
import { sanitizeProviderError } from "./dola-protocol.mjs";

const ADMIN_UI_PATH = fileURLToPath(new URL("../admin/index.html", import.meta.url));

function json(res, status, payload) {
    const body = JSON.stringify(payload);
    res.statusCode = status;
    res.setHeader("Content-Type", "application/json; charset=utf-8");
    res.setHeader("Cache-Control", "no-store");
    res.setHeader("X-Content-Type-Options", "nosniff");
    res.end(body);
}

function errorPayload(message, code) {
    return { error: { message, type: "dola_adapter_error", code } };
}

function errorStatus(error) {
    switch (String(error?.code || "")) {
        case "not_found": return 404;
        case "duplicate_request": return 409;
        case "unsupported_model":
        case "unsupported_input": return 422;
        case "rate_limited": return 429;
        case "invalid_request": return 400;
        default: return 500;
    }
}

function authorized(req, expected) {
    const authorization = String(req.headers.authorization || "");
    const bearer = authorization.match(/^Bearer\s+(.+)$/i)?.[1] || "";
    const supplied = bearer || String(req.headers["x-api-key"] || req.headers["api-key"] || "");
    return Boolean(supplied) && supplied === expected;
}

function requestPath(req) {
    return new URL(req.url || "/", "http://adapter.invalid").pathname;
}

async function readJSON(req, maxBytes) {
    const declared = Number(req.headers["content-length"] || 0);
    if (declared > maxBytes) throw Object.assign(new Error("请求体超过大小限制"), { code: "invalid_request" });
    const chunks = [];
    let total = 0;
    for await (const chunk of req) {
        total += chunk.length;
        if (total > maxBytes) throw Object.assign(new Error("请求体超过大小限制"), { code: "invalid_request" });
        chunks.push(chunk);
    }
    if (!chunks.length) return {};
    try {
        return JSON.parse(Buffer.concat(chunks).toString("utf8"));
    } catch {
        throw Object.assign(new Error("请求体不是有效 JSON"), { code: "invalid_request" });
    }
}

function accountID(pathname, prefix) {
    if (!pathname.startsWith(prefix)) return "";
    const value = pathname.slice(prefix.length).split("/")[0];
    return value ? decodeURIComponent(value) : "";
}

async function serveFile(req, res, service, id) {
    const result = service.resultFile(id);
    const file = await stat(result.path);
    if (!file.isFile()) throw Object.assign(new Error("视频结果文件不存在"), { code: "not_found" });
    const range = String(req.headers.range || "");
    let start = 0;
    let end = file.size - 1;
    let partial = false;
    if (range) {
        const match = /^bytes=(\d*)-(\d*)$/i.exec(range);
        if (!match || (!match[1] && !match[2])) {
            res.statusCode = 416;
            res.setHeader("Content-Range", `bytes */${file.size}`);
            res.end();
            return;
        }
        partial = true;
        if (match[1]) start = Number(match[1]);
        if (match[2]) end = Number(match[2]);
        if (!match[1] && match[2]) {
            const suffix = Number(match[2]);
            start = Math.max(0, file.size - suffix);
            end = file.size - 1;
        }
        end = Math.min(end, file.size - 1);
    }
    if (!Number.isInteger(start) || !Number.isInteger(end) || start < 0 || start > end || start >= file.size) {
        res.statusCode = 416;
        res.setHeader("Content-Range", `bytes */${file.size}`);
        res.end();
        return;
    }
    res.statusCode = partial ? 206 : 200;
    res.setHeader("Content-Type", result.mimeType);
    res.setHeader("Content-Length", String(end - start + 1));
    res.setHeader("Accept-Ranges", "bytes");
    res.setHeader("Cache-Control", "private, max-age=3600");
    if (partial) res.setHeader("Content-Range", `bytes ${start}-${end}/${file.size}`);
    createReadStream(result.path, { start, end }).pipe(res);
}

async function serveAdminUI(res) {
    const body = await readFile(ADMIN_UI_PATH);
    res.statusCode = 200;
    res.setHeader("Content-Type", "text/html; charset=utf-8");
    res.setHeader("Cache-Control", "no-store");
    res.setHeader("Content-Security-Policy", "default-src 'self'; connect-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; base-uri 'none'; form-action 'self'; frame-ancestors 'none'");
    res.setHeader("X-Frame-Options", "DENY");
    res.setHeader("X-Content-Type-Options", "nosniff");
    res.end(body);
}

export class DolaHTTPServer {
    constructor(service, config) {
        this.service = service;
        this.config = config;
        this.server = http.createServer((req, res) => void this.handle(req, res));
    }

    async handle(req, res) {
        const pathname = requestPath(req);
        const requestId = randomUUID();
        res.setHeader("X-Request-ID", requestId);
        try {
            if (pathname === "/healthz" && req.method === "GET") {
                json(res, 200, { ok: true, data: this.service.health() });
                return;
            }
            if ((pathname === "/admin" || pathname === "/admin/") && req.method === "GET") {
                await serveAdminUI(res);
                return;
            }
            if (pathname.startsWith("/v1/files/") && req.method === "GET") {
                await serveFile(req, res, this.service, accountID(pathname, "/v1/files/"));
                return;
            }
            if (pathname.startsWith("/internal/")) {
                await this.handleInternal(req, res, pathname);
                return;
            }
            if (!pathname.startsWith("/v1/")) {
                json(res, 404, errorPayload("接口不存在", "not_found"));
                return;
            }
            if (!authorized(req, this.config.apiKey)) {
                json(res, 401, errorPayload("API Key 无效", "invalid_api_key"));
                return;
            }
            if (pathname === "/v1/models" && req.method === "GET") {
                json(res, 200, { object: "list", data: [{ id: "dola-seedance-2.5", object: "model", owned_by: "dola-adapter" }] });
                return;
            }
            if (pathname === "/v1/videos" && req.method === "POST") {
                const body = await readJSON(req, this.config.maxBodyBytes);
                const key = req.headers["idempotency-key"] || req.headers["x-idempotency-key"] || "";
                const task = await this.service.createVideo(body, String(key));
                json(res, 202, task);
                return;
            }
            if (pathname.startsWith("/v1/videos/") && req.method === "GET") {
                json(res, 200, this.service.getVideo(accountID(pathname, "/v1/videos/")));
                return;
            }
            json(res, 404, errorPayload("接口不存在", "not_found"));
        } catch (error) {
            const normalized = sanitizeProviderError(error);
            const status = errorStatus(error);
            json(res, status, errorPayload(normalized.message, normalized.code));
        }
    }

    async handleInternal(req, res, pathname) {
        if (!authorized(req, this.config.adminKey)) {
            json(res, 401, errorPayload("管理 Key 无效", "invalid_admin_key"));
            return;
        }
        if (pathname === "/internal/accounts" && req.method === "GET") {
            json(res, 200, { data: this.service.accounts.list() });
            return;
        }
        if (pathname === "/internal/accounts" && req.method === "POST") {
            const body = await readJSON(req, this.config.maxBodyBytes);
            const account = await this.service.accounts.add(body.name, body.profileKey || body.id, body.cdpUrl);
            json(res, 201, { data: publicAccount(account) });
            return;
        }
        const id = accountID(pathname, "/internal/accounts/");
        if (!id) {
            json(res, 404, errorPayload("管理接口不存在", "not_found"));
            return;
        }
        if (pathname.endsWith("/login") && req.method === "POST") {
            const account = this.service.store.account(id);
            if (!account) throw Object.assign(new Error("Dola 账号不存在"), { code: "not_found" });
            const result = await this.service.browser.openLogin(account);
            json(res, 200, { data: { account: publicAccount(account), loginUrl: result.url } });
            return;
        }
        if (req.method === "PATCH") {
            const body = await readJSON(req, this.config.maxBodyBytes);
            const account = await this.service.accounts.update(id, body);
            json(res, 200, { data: publicAccount(account) });
            return;
        }
        json(res, 404, errorPayload("管理接口不存在", "not_found"));
    }

    listen() {
        return new Promise((resolve, reject) => {
            this.server.once("error", reject);
            this.server.listen(this.config.port, this.config.host, () => {
                this.server.removeListener("error", reject);
                resolve();
            });
        });
    }

    close() {
        return new Promise((resolve) => this.server.close(() => resolve()));
    }
}
