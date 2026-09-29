.PHONY: test check build compose-check linux-build

test:
	go test -race ./...

check:
	go vet ./...

build:
	mkdir -p bin
	CGO_ENABLED=1 go build -trimpath -o bin/dockyard ./cmd/dockyard
	CGO_ENABLED=1 go build -trimpath -o bin/dockyardctl ./cmd/dockyardctl

compose-check:
	DOCKYARD_COMPOSE_TEST=1 go test ./internal/runtime -run 'Test(ComposeLiteralEnvironmentRoundTrip|SubmittedMultiServiceComposeRoundTrip)' -v

linux-build:
	docker build --pull --file Dockerfile.build --output bin/linux .
