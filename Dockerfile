FROM golang AS builder
WORKDIR /app
COPY . .
ARG GITHUB_SHA
ARG VERSION
RUN echo "Building commit: ${GITHUB_SHA:0:7}" && \
    go mod tidy && \
    go build -ldflags="-s -w -X main.Version=${VERSION} -X main.CurrentCommit=${GITHUB_SHA:0:7}" -trimpath -o subs-check .

FROM ubuntu
ENV DEBIAN_FRONTEND=noninteractive TZ=Asia/Shanghai
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates tzdata && \
    ln -fs /usr/share/zoneinfo/$TZ /etc/localtime && \
    echo $TZ > /etc/timezone && \
    dpkg-reconfigure -f noninteractive tzdata && \
    apt-get clean && rm -rf /var/lib/apt/lists/* /tmp/*
COPY --from=builder /app/subs-check /app/subs-check
CMD /app/subs-check
EXPOSE 8199
EXPOSE 8299