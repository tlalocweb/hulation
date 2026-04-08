FROM golang:1.25-alpine AS build
WORKDIR /src
COPY main.go .
RUN CGO_ENABLED=0 go build -o /counter-backend .

FROM alpine:3.19
COPY --from=build /counter-backend /usr/local/bin/counter-backend
ENTRYPOINT ["counter-backend"]
