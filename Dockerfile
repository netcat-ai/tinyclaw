FROM node:22-bookworm-slim

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && npm install -g @openai/codex@0.131.0 \
    && rm -rf /var/lib/apt/lists/*

COPY dist/tinyclaw /app/tinyclaw
COPY web/control/dist /app/web/control/dist

ENTRYPOINT ["/app/tinyclaw"]
