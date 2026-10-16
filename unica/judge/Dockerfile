# The gate as a container. Build from unica/ (it needs pkg/ beside judge/):
#   docker build -t unica-judge -f judge/Dockerfile .
#   docker run -p 8787:8787 -v judge-data:/data -e JUDGE_TOKEN=... unica-judge
# Builds outside the go.work on purpose: judge depends on pkg and nothing else.
FROM golang:1.23-alpine AS builder
ARG VERSION=dev
WORKDIR /src
COPY pkg/ pkg/
COPY judge/ judge/
RUN cd judge && GOWORK=off CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" -o /bin/judge ./cmd/judge

FROM alpine:3.19
RUN adduser -D -u 10001 judge && mkdir -p /data/facts /data/evidence && chown -R judge /data
COPY --from=builder /bin/judge /bin/judge
USER judge
VOLUME /data
EXPOSE 8787
# Facts and evidence live on the volume so a restart continues the chain.
# Listening beyond loopback without JUDGE_TOKEN logs a warning at start-up.
ENV JUDGE_ADDR=0.0.0.0:8787 JUDGE_FACTS=/data/facts JUDGE_EVIDENCE=/data/evidence
ENTRYPOINT ["/bin/judge"]
CMD ["serve"]
