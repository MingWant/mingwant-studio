const PROXY_DEFAULT_PORTS = Object.freeze({
    "http:": 80,
    "https:": 443,
    "socks4:": 1080,
    "socks5:": 1080,
});

const MAX_PROXY_SERVER_LENGTH = 512;
const MAX_PROXY_USERNAME_LENGTH = 200;
const MAX_PROXY_PASSWORD_LENGTH = 1024;

export function normalizeProxyServer(value) {
    const raw = typeof value === "string" ? value.trim() : "";
    if (!raw) return null;
    if (raw.length > MAX_PROXY_SERVER_LENGTH) throw new Error("代理地址过长");
    if (!/^[a-z][a-z\d+.-]*:\/\//i.test(raw)) throw new Error("代理地址必须包含 http://、https://、socks4:// 或 socks5:// 协议");

    let url;
    try {
        url = new URL(raw);
    } catch {
        throw new Error("代理地址格式无效");
    }
    const defaultPort = PROXY_DEFAULT_PORTS[url.protocol];
    if (!defaultPort) throw new Error("代理仅支持 HTTP、HTTPS、SOCKS4 和 SOCKS5");
    if (!url.hostname) throw new Error("代理地址缺少主机名或 IP");
    if ((url.pathname && url.pathname !== "/") || url.search || url.hash) throw new Error("代理地址不能包含路径、查询参数或锚点");

    const port = url.port ? Number(url.port) : defaultPort;
    if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error("代理端口必须在 1 到 65535 之间");
    return {
        server: `${url.protocol}//${url.hostname}:${port}`,
        host: normalizeProxyHost(url.hostname),
        port,
        username: normalizeProxyUsername(decodeCredential(url.username, "代理用户名")),
        password: normalizeProxyPassword(decodeCredential(url.password, "代理密码")),
    };
}

export function normalizeProxyUsername(value) {
    const username = typeof value === "string" ? value.trim() : "";
    if (username.length > MAX_PROXY_USERNAME_LENGTH) throw new Error("代理用户名过长");
    if (/[\u0000-\u001f\u007f]/.test(username)) throw new Error("代理用户名包含不允许的控制字符");
    return username;
}

export function normalizeProxyPassword(value) {
    const password = typeof value === "string" ? value : "";
    if (password.length > MAX_PROXY_PASSWORD_LENGTH) throw new Error("代理密码过长");
    if (/[\u0000\r\n]/.test(password)) throw new Error("代理密码包含不允许的控制字符");
    return password;
}

export function proxyMatchesAuthenticationChallenge(server, authInfo) {
    if (!authInfo?.isProxy) return false;
    const proxy = normalizeProxyServer(server);
    if (!proxy) return false;
    return proxy.host === normalizeProxyHost(authInfo.host) && proxy.port === Number(authInfo.port);
}

function normalizeProxyHost(value) {
    return String(value || "").trim().replace(/^\[|\]$/g, "").toLowerCase();
}

function decodeCredential(value, label) {
    if (!value) return "";
    try {
        return decodeURIComponent(value);
    } catch {
        throw new Error(`${label}编码无效`);
    }
}
