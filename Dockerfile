ARG GO_VERSION
ARG ALPINE_VERSION
FROM golang:${GO_VERSION}-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o bot ./cmd/bot && \
    CGO_ENABLED=0 go build -o repair-media ./cmd/repair-media

FROM alpine:${ALPINE_VERSION}
RUN apk add --no-cache ca-certificates
RUN addgroup -S app && adduser -S app -G app
WORKDIR /app
COPY --from=builder --chown=app:app /app/bot .
# One-shot tool for repairing items whose bytes were never stored correctly;
# run via: docker compose run --rm --entrypoint /repair-media stash-bot
COPY --from=builder --chown=app:app /app/repair-media .
USER app
ENTRYPOINT ["./bot"]
