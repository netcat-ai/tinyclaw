FROM node:22-bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    git \
    jq \
    tini \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY package*.json ./
RUN npm ci

COPY tsconfig.json ./
COPY src ./src
RUN npm run build && npm prune --omit=dev

COPY scripts/entrypoint.sh /app/entrypoint.sh

RUN chmod +x /app/entrypoint.sh \
    && mkdir -p /workspace /var/lib/tinyclaw /var/log/tinyclaw \
    && chown -R node:node /app /workspace /var/lib/tinyclaw /var/log/tinyclaw

ENV NODE_ENV=production
USER node

ENTRYPOINT ["/usr/bin/tini", "--", "/app/entrypoint.sh"]
