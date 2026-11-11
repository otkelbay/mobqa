FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/mobqa-api ./cmd/mobqa-worker

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 mobqa \
    && mkdir -p /var/lib/mobqa/data && chown -R mobqa /var/lib/mobqa
COPY --from=build /out/ /usr/local/bin/
USER mobqa
WORKDIR /var/lib/mobqa
EXPOSE 18080
ENTRYPOINT ["mobqa-api"]
