FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o bot ./cmd/bot && \
    CGO_ENABLED=0 go build -o repair-media ./cmd/repair-media

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /app/bot .
# One-shot tool for repairing items whose bytes were never stored correctly;
# run via: docker compose run --rm --entrypoint /repair-media stash-bot
COPY --from=builder /app/repair-media .
ENTRYPOINT ["./bot"]
