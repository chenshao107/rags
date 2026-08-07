FROM golang:1.23-alpine AS build

ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /rags ./cmd/rags

FROM alpine:3.20

RUN apk add --no-cache ca-certificates wget

COPY --from=build /rags /usr/local/bin/rags
COPY docker/entrypoint.sh /entrypoint.sh

RUN chmod +x /entrypoint.sh

ENV RAGS_NAME=bk7258-docs \
    RAGS_PORT=18787 \
    RAGS_PUBLIC_HOST=127.0.0.1 \
    QDRANT_URL=http://qdrant:6333 \
    DOCS_PATH=/data/docs \
    RAGS_SETTINGS_DIR=/etc/rags

EXPOSE 18787

ENTRYPOINT ["/entrypoint.sh"]
