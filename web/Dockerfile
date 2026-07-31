# syntax=docker/dockerfile:1.6

FROM node:24.18.0-alpine AS build
WORKDIR /src
COPY package.json package-lock.json ./
ARG NPM_CONFIG_REGISTRY=https://registry.npmjs.org/
RUN npm ci \
    --fetch-retries=5 \
    --fetch-retry-factor=2 \
    --fetch-retry-mintimeout=10000 \
    --fetch-retry-maxtimeout=120000
COPY . .
ENV VITE_USE_MOCK=false \
    VITE_API_BASE=
RUN npm run build

FROM nginxinc/nginx-unprivileged:1.27.4-alpine3.21
ENV MEMD_UPSTREAM=http://memd:8080 \
    MEM_MAX_BODY_SIZE=256m \
    NGINX_ENVSUBST_FILTER="^(MEMD_UPSTREAM|MEM_MAX_BODY_SIZE)$"
COPY nginx/default.conf.template /etc/nginx/templates/default.conf.template
COPY --from=build /src/dist /usr/share/nginx/html
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
