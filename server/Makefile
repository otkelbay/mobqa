GO ?= go
TEST_DATABASE_URL ?= $(MOBQA_TEST_DATABASE_URL)

.PHONY: check fmt vet staticcheck lint test integration e2e cover

check: fmt vet staticcheck lint test integration e2e

fmt:
	@unformatted="$$(gofmt -l .)"; if [ -n "$$unformatted" ]; then echo "$$unformatted"; exit 1; fi

vet:
	$(GO) vet ./...
	$(GO) vet -tags integration,e2e ./...

staticcheck:
	staticcheck -tags integration,e2e ./...

lint:
	golangci-lint run ./...

test:
	$(GO) test -race ./...

integration:
	@if [ -z "$(TEST_DATABASE_URL)" ]; then echo "skip integration: MOBQA_TEST_DATABASE_URL unset"; else \
		MOBQA_TEST_DATABASE_URL="$(TEST_DATABASE_URL)" $(GO) test -race -tags integration ./...; fi

e2e:
	@if [ -z "$(TEST_DATABASE_URL)" ]; then echo "skip e2e: MOBQA_TEST_DATABASE_URL unset"; else \
		MOBQA_TEST_DATABASE_URL="$(TEST_DATABASE_URL)" $(GO) test -race -tags e2e ./...; fi

cover:
	@if [ -z "$(TEST_DATABASE_URL)" ]; then echo "cover needs MOBQA_TEST_DATABASE_URL"; exit 1; fi
	MOBQA_TEST_DATABASE_URL="$(TEST_DATABASE_URL)" $(GO) test -p 1 -tags integration,e2e \
		-coverpkg=./internal/... -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -n 1
