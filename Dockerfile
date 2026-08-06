FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/koxmoe-transfer .

FROM alpine:3.22
RUN mkdir -p /data /downloads
COPY --from=build /out/koxmoe-transfer /usr/local/bin/koxmoe-transfer
ENV ADDR=0.0.0.0:8080 DOWNLOAD_DIR=/downloads PROXY_ENABLED=false SESSION_FILE=/data/kzo-session.json
EXPOSE 8080
VOLUME ["/data", "/downloads"]
ENTRYPOINT ["/usr/local/bin/koxmoe-transfer"]
