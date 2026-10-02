# Image for the PigeonDrop upload server, built by Coolify (Base Directory: /server).
FROM node:24-alpine

ENV NODE_ENV=production \
    PORT=3000 \
    STORAGE_DIR=/data

WORKDIR /app

# Dependencies first, so this layer is reused until the lockfile changes.
COPY package.json package-lock.json ./
RUN npm ci --omit=dev

COPY . .

# STORAGE_DIR (accounts, upload history, recordings, server.log) must be a
# persistent volume mounted at /data. It is created and owned here so that a
# fresh Docker volume is writable by the unprivileged user below.
RUN mkdir -p /data && chown -R node:node /data
USER node

EXPOSE 3000

# /setup is the cheapest route that touches the server code and needs no state.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:${PORT}/setup || exit 1

CMD ["node", "server.js"]
