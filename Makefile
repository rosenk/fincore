GO      ?= go
BIN     := bin
FINC0   := $(BIN)/finc0
FUEL    ?= 1000000

export CGO_ENABLED ?= 0

.PHONY: all build finc0 test race vet fmt fmt-check lint parse-corpus check-corpus \
        integration finc1 bootstrap ci clean help

all: vet test build ## vet, test and build finc0

help: ## list targets
	@grep -E '^[a-zA-Z0-9_-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-14s %s\n", $$1, $$2}'

build: finc0 ## build the Stage 0 compiler

finc0: ## build bin/finc0 (static, trimpath)
	@mkdir -p $(BIN)
	$(GO) build -trimpath -o $(FINC0) ./bootstrap/cmd/finc0

test: ## unit tests
	$(GO) test -count=1 ./...

race: ## unit tests with the race detector (needs cgo)
	CGO_ENABLED=1 $(GO) test -race -count=1 ./...

vet: ## go vet
	$(GO) vet ./...

fmt: ## gofmt all Go files
	gofmt -w $$(git ls-files '*.go')

fmt-check: ## fail if any Go file is not gofmt-ed
	@out=$$(gofmt -l $$(git ls-files '*.go')); test -z "$$out" || { echo "$$out"; exit 1; }

lint: fmt-check vet ## fmt-check + vet

parse-corpus: finc0 ## finc0 parse on examples, testdata and compiler/
	@set -e; for f in examples/*.fc testdata/pass/*.fc testdata/fail/*.fc; do $(FINC0) parse $$f >/dev/null; done
	$(FINC0) parse compiler/ >/dev/null
	@echo "parse-corpus: ok"

check-corpus: finc0 ## finc0 check on examples, pass tests and compiler/
	@set -e; for f in examples/*.fc testdata/pass/*.fc; do $(FINC0) check --comptime-fuel=$(FUEL) $$f; done
	$(FINC0) check compiler/
	@echo "check-corpus: ok"

integration: ## end-to-end TCP echo + corpus tests (skips until finc0 can build)
	$(GO) test -count=1 -v ./testdata/integration/

finc1: finc0 ## build the self-hosted compiler with finc0 (Stage 1)
	$(FINC0) build --emit-go=$(BIN)/stage1.go -o $(BIN)/finc1 ./compiler

bootstrap: finc0 ## full fixed-point bootstrap gate (stage2.go == stage3.go)
	$(GO) run ./scripts/bootstrap_verify.go

ci: lint test build integration ## what CI runs

clean: ## remove build outputs
	rm -rf $(BIN)
