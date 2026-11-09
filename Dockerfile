FROM gcr.io/distroless/static:nonroot
ARG TARGETARCH
WORKDIR /
COPY ./dist/krci-agent-dispatcher-${TARGETARCH} /krci-agent-dispatcher

USER 65532:65532

ENTRYPOINT ["/krci-agent-dispatcher"]
