FROM ghcr.io/agents-sandbox/coding-runtime:latest

ARG OPENCLAW_VERSION
RUN npm install -g "openclaw@${OPENCLAW_VERSION}"

COPY entrypoint.sh /usr/local/bin/openclaw-entrypoint.sh
RUN chmod +x /usr/local/bin/openclaw-entrypoint.sh

ENTRYPOINT ["openclaw-entrypoint.sh"]
CMD ["openclaw", "gateway", "run", "--port", "18789", "--bind", "lan"]
