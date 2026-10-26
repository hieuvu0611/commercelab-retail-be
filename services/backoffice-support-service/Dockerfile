# Multi-stage Dockerfile cho NestJS Backoffice Support Service (tối ưu <= 250MB RAM & <= 0.25 CPU)
FROM node:20-alpine AS builder
WORKDIR /app

COPY package.json tsconfig.json ./
RUN npm install --no-audit --no-fund

COPY src ./src
RUN npm run build && npm prune --omit=dev

FROM node:20-alpine
WORKDIR /app

ENV NODE_ENV=production
# Khóa V8 Old Space <= 180MB và libuv threadpool = 2 để tổng RSS không vượt ngưỡng 250MB RAM / 0.25 CPU
ENV NODE_OPTIONS="--max-old-space-size=180"
ENV UV_THREADPOOL_SIZE=2

COPY --from=builder /app/package.json ./package.json
COPY --from=builder /app/node_modules ./node_modules
COPY --from=builder /app/dist ./dist

EXPOSE 8088
CMD ["node", "--max-old-space-size=180", "dist/main.js"]
