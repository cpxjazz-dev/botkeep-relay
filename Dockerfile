# love / 中转服务 Dockerfile（给 Cloady 这类 PaaS 用）
# 构建：docker build -t relay .
# 运行：docker run -e PORT=8080 -e DATABASE_URL=... -e AWS_ACCESS_KEY_ID=... \
#         -e AWS_SECRET_ACCESS_KEY=... -e AWS_ENDPOINT_URL_S3=... \
#         -e AWS_REGION=ap-southeast-1 -e NEON_STORAGE_BUCKET=uploads \
#         -e LOVE_SUPERADMIN_USERNAME=cpxjazz relay
# 注意：.env.local 不进镜像（gitignore），凭据一律走环境变量；
# QQ 网关只有配了 QQ AppID/Secret 才启动，不配就是纯 blog+love 服务。
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY vendor/ ./vendor/
COPY *.go ./
COPY index.html ./
COPY blog/ ./blog/
COPY love/ ./love/
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -o relay .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /src/relay ./relay
# Cloady 这类 PaaS 用 PORT 环境变量告诉应用监听哪个端口；
# relay 自己会读 SERVER_PORT，其次 PORT，默认 8080。
ENV PORT=8080
EXPOSE 8080
CMD ["./relay"]
