FROM --platform=$BUILDPLATFORM tonistiigi/xx AS xx
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS build
LABEL org.opencontainers.image.source=https://github.com/tlalocweb/hulation
LABEL org.opencontainers.image.description="Hula - the modern web server for DM and CDP services"
LABEL org.opencontainers.image.licenses=Apache-2.0

# get xx tools to build image
RUN apk add clang lld
COPY --from=xx / /

ADD hulation /src/hulation
ADD conftagz /src/conftagz
ADD clickhouse /src/clickhouse
RUN mkdir -p /src
ARG TARGETPLATFORM
RUN xx-apk add musl-dev gcc
ENV CGO_ENABLED=1
WORKDIR /src/hulation
# RUN gcc -dumpmachine
ARG hulaversion=notset
ENV hulaversion=$hulaversion
RUN xx-go --wrap
RUN go build -ldflags "-X config.Version=$(hulaversion)" -o hula . && xx-verify hula
WORKDIR /src
#RUN git clone https://github.com/FiloSottile/mkcert.git
#WORKDIR /src/mkcert
#RUN go build -ldflags "-X main.Version=$(git describe --tags)" && xx-verify mkcert
FROM --platform=$BUILDPLATFORM alpine:3.19
RUN mkdir -p /etc/hula /var/hula /hula
ADD hulation/docker-example-config.yaml /etc/hula/config.yaml
COPY --from=build /src/hulation/hula /hula/hula
#COPY --from=build /src/mkcert/mkcert /usr/bin
ENTRYPOINT [ "/hula/hula", "-config", "/etc/hula/config.yaml" ]
