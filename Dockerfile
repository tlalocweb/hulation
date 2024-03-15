FROM --platform=$BUILDPLATFORM tonistiigi/xx AS xx
FROM --platform=$BUILDPLATFORM golang:1.22-alpine
LABEL org.opencontainers.image.source=https://github.com/tlalocweb/hulation
LABEL org.opencontainers.image.description="Hula - the modern web server for DM and CDP services"
LABEL org.opencontainers.image.licenses=Apache-2.0

# get xx tools to build image
RUN apk add clang lld
COPY --from=xx / /
RUN mkdir -p /src
ADD hulation /src/hulation
ADD conftagz /src/conftagz
ADD clickhouse /src/clickhouse
ARG TARGETPLATFORM
RUN xx-apk add musl-dev gcc
ENV CGO_ENABLED=1
WORKDIR /src/hulation
# RUN gcc -dumpmachine
RUN xx-go --wrap
RUN go build -o hula . && xx-verify hula
ENTRYPOINT [ "/hula/hula" ]
