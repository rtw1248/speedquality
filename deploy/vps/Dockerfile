FROM node:24.14.0-bookworm-slim
WORKDIR /app
COPY run.sh install-node.sh ./
COPY deploy/cloudflare-worker/src/ deploy/cloudflare-worker/src/
COPY deploy/cloudflare-worker/migrations/ deploy/cloudflare-worker/migrations/
COPY deploy/cloudflare-worker/package.json deploy/cloudflare-worker/package.json
COPY deploy/vps/ deploy/vps/
USER node
CMD ["node", "deploy/vps/server.mjs"]
