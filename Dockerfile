# 白泽后端（baize-backend）镜像 —— 多阶段构建，供 CI 或「有 Docker 的机器」使用。
#
# 只把「服务端」容器化：手机 APK / Windows 桌面端 / arm64 内核不是容器能装的东西（原因见交接文档）。
# 本机没有 Docker 时不要用这个文件，走 tools/deploy-nas.ps1 的 NAS 侧构建（Dockerfile.nas）。
#
#   本地构建：  docker build -t baize-backend:dev .
#   CI 构建：   .github/workflows/docker.yml 构建并推到 ghcr.io/hdkl0802/baize-backend

FROM golang:1.25-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOPROXY=https://goproxy.cn,direct

# go.mod / go.sum 单独一层：只改代码不会重装依赖
COPY core/go.mod   core/go.sum   ./core/
COPY shared/go.mod shared/go.sum ./shared/
COPY agent/go.mod  agent/go.sum  ./agent/
RUN cd agent && go mod download

COPY core/   ./core/
COPY shared/ ./shared/
COPY agent/  ./agent/
RUN cd agent && go build -trimpath -ldflags "-s -w" -o /out/baize-backend ./cmd/backend

FROM alpine:3.20
# 出网要调百炼 / DeepSeek 等，必须有 CA 证书；时区让日志和定时任务对得上
# chromium：browser 工具的浏览器内核（没有它，browser 工具会明确报"没有可用浏览器"）
# font-noto-cjk：中文字形（没有它，网页截图里的汉字会变方框）
RUN apk add --no-cache ca-certificates tzdata chromium fontconfig font-noto-cjk
# 预建字体缓存：否则第一次起 chromium 要现场扫字体（CJK 字体大，慢）
RUN fc-cache -f
ENV TZ=Asia/Shanghai
WORKDIR /app
COPY --from=build /out/baize-backend /app/baize-backend
VOLUME ["/data"]
EXPOSE 8787
ENTRYPOINT ["/app/baize-backend"]
CMD ["--addr", "0.0.0.0:8787", "--data", "/data"]
