FROM golang:alpine AS builder
WORKDIR /app
COPY . .
ARG GITHUB_SHA
ARG VERSION
RUN echo "Building commit: ${GITHUB_SHA:0:7}" && \
    go build -ldflags="-s -w -X main.Version=${VERSION} -X main.CurrentCommit=${GITHUB_SHA:0:7}" -trimpath -o subs-check . && \
    go build -ldflags="-s -w" -trimpath -o home-filter ./cmd/home-filter

FROM alpine
WORKDIR /app
ENV TZ=Asia/Shanghai
RUN apk add --no-cache alpine-conf ca-certificates nodejs &&\
    /usr/sbin/setup-timezone -z Asia/Shanghai && \
    apk del alpine-conf && \
    rm -rf /var/cache/apk/*
COPY --from=builder /app/subs-check /app/subs-check
COPY --from=builder /app/home-filter /app/home-filter
COPY docker/entrypoint.sh /app/entrypoint.sh
ENTRYPOINT ["/app/entrypoint.sh"]
CMD ["-f", "/app/config/config.yaml"]
EXPOSE 8199
EXPOSE 8299
