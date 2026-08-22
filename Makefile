DOCKER_COMPOSE = docker compose
BINARY = bot

.PHONY: build
build:
	go build -o $(BINARY) ./cmd/bot

# The bot stack owns ONLY the "stash-bot" service. It must never start the
# stash backend — that is deployed separately by the stash repo's own CI.
# Targeting "stash-bot" explicitly guarantees the backend is never launched here.
.PHONY: up
up:
	$(DOCKER_COMPOSE) up -d --build stash-bot

.PHONY: down
down:
	$(DOCKER_COMPOSE) down

.PHONY: logs
logs:
	$(DOCKER_COMPOSE) logs -f stash-bot

.PHONY: deploy
deploy:
	$(DOCKER_COMPOSE) up -d --build --no-cache stash-bot

.PHONY: restart
restart:
	$(DOCKER_COMPOSE) restart stash-bot

.PHONY: format
format:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

.PHONY: test
test:
	go test ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: clean
clean:
	rm -f $(BINARY)
