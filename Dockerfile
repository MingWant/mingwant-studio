# 构建 Vite 前端产物。
FROM oven/bun:1.3.14 AS web-build

WORKDIR /app/web
ARG VITE_TLDRAW_LICENSE_KEY
ARG NPM_REGISTRY=https://registry.npmjs.org
ENV VITE_TLDRAW_LICENSE_KEY=${VITE_TLDRAW_LICENSE_KEY}
COPY web/package.json web/bun.lock ./
# 锁文件保留了开发环境的镜像地址；构建时统一替换为可配置的注册表，
# 避免 Bun 继续跟随失效的 npmmirror CDN 重定向并拿到损坏的 tarball。
RUN sed -i "s#https://registry.npmmirror.com#${NPM_REGISTRY}#g" bun.lock \
    && bun install --registry=${NPM_REGISTRY} --cache-dir=/root/.bun/install/cache
COPY VERSION /app/VERSION
COPY CHANGELOG.md /app/CHANGELOG.md
COPY web ./
RUN bun run build

# 运行镜像：nginx 托管静态前端，并在 Compose 中把 /api 转发到后端服务。
FROM nginx:1.27-alpine

COPY --from=web-build /app/web/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf

EXPOSE 3000
STOPSIGNAL SIGQUIT
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 CMD wget -qO- http://127.0.0.1:3000/ >/dev/null || exit 1
