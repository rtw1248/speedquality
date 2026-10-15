FROM node:24.14.0-bookworm-slim
WORKDIR /app
COPY deploy/vps/package.json deploy/vps/package-lock.json deploy/vps/
RUN cd deploy/vps && npm ci --omit=dev --no-audit --no-fund
# The private Core is mounted at /core; make its pinned runtime dependencies
# available through Node's normal parent-directory module resolution.
RUN ln -s /app/deploy/vps/node_modules /node_modules
COPY run.sh install-node.sh ./
COPY deploy/cloudflare-worker/src/ deploy/cloudflare-worker/src/
COPY deploy/cloudflare-worker/migrations/ deploy/cloudflare-worker/migrations/
COPY deploy/cloudflare-worker/package.json deploy/cloudflare-worker/package.json
COPY deploy/vps/ deploy/vps/
USER node
CMD ["node", "deploy/vps/server.mjs"]
