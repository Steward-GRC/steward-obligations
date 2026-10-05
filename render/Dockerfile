# syntax=docker/dockerfile:1

FROM node:22-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts
COPY tsconfig.json ./
COPY src ./src
RUN npm run build && npm prune --omit=dev

FROM node:22-alpine AS runtime
ARG VERSION=dev
ARG COMMIT=
ENV NODE_ENV=production \
    PORT=8091 \
    VERSION=$VERSION \
    COMMIT=$COMMIT
WORKDIR /app

# The server runs node directly, and npm's bundled dependencies are the bulk
# of the image's CVE surface, so npm and npx are removed.
RUN apk upgrade --no-cache \
    && rm -rf /usr/local/lib/node_modules/npm /usr/local/bin/npm /usr/local/bin/npx \
    && addgroup -S render && adduser -S render -G render

COPY --from=build /app/package.json ./
COPY --from=build /app/node_modules ./node_modules
COPY --from=build /app/dist ./dist

USER render
EXPOSE 8091

# The server binds loopback by default; the healthcheck runs in the
# container's own network namespace, so 127.0.0.1 reaches it.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- "http://127.0.0.1:${PORT}/livez" || exit 1

CMD ["node", "dist/server.js"]
