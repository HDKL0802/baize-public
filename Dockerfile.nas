# 白泽后端镜像 —— NAS 侧构建（本机没有 Docker 时走这条）。
#
# 前置：先把本地交叉编译出的 linux/amd64 二进制传成同目录下的 `baize-backend`
#      （tools/deploy-nas.ps1 会自动做），再：
#         sudo docker build -f Dockerfile.nas -t baize-backend:local .
# 基础镜像可覆盖：NAS 自带的 fnOS 镜像源对 library/alpine 会 401，
# 因此默认走已验证可用的 daocloud 源；deploy-nas.ps1 会显式传 --build-arg。
ARG BASE_IMAGE=docker.m.daocloud.io/library/alpine:3.20
FROM ${BASE_IMAGE}
# chromium：browser 工具的浏览器内核（没有它，browser 工具会明确报"没有可用浏览器"）
# fontconfig：让截图里的字能渲染出来（CJK 字体按需再加 font-noto-cjk，会大不少）
RUN apk add --no-cache ca-certificates tzdata chromium fontconfig
ENV TZ=Asia/Shanghai
WORKDIR /app
COPY baize-backend /app/baize-backend
RUN chmod +x /app/baize-backend
VOLUME ["/data"]
EXPOSE 8787
ENTRYPOINT ["/app/baize-backend"]
CMD ["--addr", "0.0.0.0:8787", "--data", "/data"]
