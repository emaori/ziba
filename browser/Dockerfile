FROM mcr.microsoft.com/playwright:v1.62.0-noble

WORKDIR /app

COPY package.json ./
RUN npm install --omit=dev --no-audit --no-fund

COPY server.mjs safe-proxy.mjs safe-proxy.test.mjs public-url.mjs public-url.test.mjs ./
RUN npm test

USER pwuser
EXPOSE 3000

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD node -e "fetch('http://127.0.0.1:3000/health').then(r => process.exit(r.ok ? 0 : 1)).catch(() => process.exit(1))"

ENTRYPOINT ["node", "/app/server.mjs"]
