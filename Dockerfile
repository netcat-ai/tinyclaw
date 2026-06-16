FROM node:22-bookworm-slim

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

RUN npm install -g @openai/codex@0.131.0

COPY dist/tinyclaw /app/tinyclaw
COPY web/control/dist /app/web/control/dist

ENTRYPOINT ["/app/tinyclaw"]
