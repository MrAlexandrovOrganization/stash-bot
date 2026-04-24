DOCKER_COMPOSE = docker compose
BINARY = bot

.PHONY: build
build:
	go build -o $(BINARY) ./cmd/bot

.PHONY: up
up:
	$(DOCKER_COMPOSE) up -d --build

.PHONY: down
down:
	$(DOCKER_COMPOSE) down

.PHONY: logs
logs:
	$(DOCKER_COMPOSE) logs -f

.PHONY: deploy
deploy:
	$(DOCKER_COMPOSE) up -d --build --no-cache

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
