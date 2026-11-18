.PHONY: build fmt lint test integration e2e demo-safe demo-unsafe benchmark clean

build:
	go build -trimpath -o bin/releaseproof ./cmd/releaseproof
fmt:
	gofmt -w cmd internal examples/rolling-deployment/app
lint:
	go vet ./...
	test -z "$$(gofmt -l cmd internal examples/rolling-deployment/app)"
test:
	go test -race -count=1 ./...
integration:
	go test -race -tags integration -count=1 -timeout 25m -v ./internal/runner
e2e: build
	python3 scripts/e2e.py
demo-safe: build
	bin/releaseproof check --config examples/rolling-deployment/safe.yaml
demo-unsafe: build
	bin/releaseproof check --config examples/rolling-deployment/unsafe.yaml
benchmark: build
	python3 scripts/benchmark.py --runs 3
clean:
	go clean ./...
