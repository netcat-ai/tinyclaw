FROM ghcr.io/netcat-ai/tinyclaw:latest

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
    nodejs \
    npm \
    && npm install -g @openai/codex@0.131.0 \
    && rm -rf /var/lib/apt/lists/*

COPY dist/tinyclaw /app/tinyclaw
COPY channel/wecom/finance/lib /app/channel/wecom/finance/lib

ENV LD_LIBRARY_PATH=/app/channel/wecom/finance/lib

ENTRYPOINT ["/app/tinyclaw"]
