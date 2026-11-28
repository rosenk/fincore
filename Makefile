SHELL := /bin/bash
GO      ?= go
BIN     := bin
FINC0   := $(BIN)/finc0
FINCSEED := $(BIN)/finc_seed
FINC1   := $(BIN)/finc1
FINC2   := $(BIN)/finc2
FINC3   := $(BIN)/finc3
FINC    := $(BIN)/finc
SEED_GZ := bootstrap/seed/finc_seed.s.gz
FUEL    ?= 1000000

export CGO_ENABLED ?= 0

.PHONY: all build finc0 finc-seed update-seed finc1 finc bootstrap test vet fmt fmt-check lint test-corpus test-examples integration ci clean help

all: vet test build finc ## vet, test, build finc0 and self-hosted finc

help: ## list targets
	@grep -E '^[a-zA-Z0-9_-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-14s %s\n", $$1, $$2}'

build: finc0 ## build the Stage 0 native bootstrap compiler

finc0: ## build bin/finc0 (static Go binary, frozen at v0.1 Native)
	@mkdir -p $(BIN)
	$(GO) build -trimpath -o $(FINC0) ./bootstrap/cmd/finc0

finc-seed: ## assemble rolling self-hosting seed compiler from bootstrap/seed/finc_seed.s.gz
	@mkdir -p $(BIN)
	@set -eo pipefail; d=$$(mktemp -d); trap 'rm -rf "$$d"' EXIT; \
	gzip -dc $(SEED_GZ) > "$$d/seed.s"; \
	as --64 -o "$$d/seed.o" "$$d/seed.s"; \
	for f in sys fmt net scope effect fiber; do \
	  as --64 -o "$$d/rt_$$f.o" "runtime/linux_amd64/$$f.s"; \
	done; \
	ld -static -z noexecstack --build-id=none -o $(FINCSEED) \
	  "$$d/rt_sys.o" "$$d/rt_fmt.o" "$$d/rt_net.o" "$$d/rt_scope.o" "$$d/rt_effect.o" "$$d/rt_fiber.o" "$$d/seed.o"

finc1: finc-seed ## build Stage 1 native compiler (bin/finc1) using rolling seed
	FINCROOT=$$(pwd) FINCORE_ROOT=$$(pwd) $(FINCSEED) build -o $(FINC1) ./compiler

finc: finc1 ## build self-hosted native compiler (bin/finc = finc2)
	FINCROOT=$$(pwd) FINCORE_ROOT=$$(pwd) $(FINC1) build -o $(FINC) ./compiler

bootstrap: finc1 ## full 3-stage native fixed-point check (finc1 -> finc2 == finc3) + integration
	@set -eo pipefail; d=$$(mktemp -d); trap 'rm -rf "$$d"' EXIT; \
	FINCROOT=$$(pwd) FINCORE_ROOT=$$(pwd) $(FINC1) build --emit-asm="$$d/stage2.s" -o $(FINC2) ./compiler; \
	FINCROOT=$$(pwd) FINCORE_ROOT=$$(pwd) $(FINC2) build --emit-asm="$$d/stage3.s" -o $(FINC3) ./compiler; \
	cmp -s "$$d/stage2.s" "$$d/stage3.s" || { echo "bootstrap: stage2.s != stage3.s"; exit 1; }; \
	cmp -s $(FINC2) $(FINC3) || { echo "bootstrap: finc2 != finc3"; exit 1; }; \
	cp $(FINC2) $(FINC); \
	sha256sum "$$d/stage2.s" "$$d/stage3.s" $(FINC2) $(FINC3); \
	echo "bootstrap: native self-hosting fixed point verified (finc2 == finc3)"; \
	FINC0=$$(pwd)/$(FINC2) $(GO) test -count=1 ./testdata/integration/

update-seed: bootstrap ## promote verified fixed-point finc2 assembly to bootstrap/seed/finc_seed.s.gz
	@mkdir -p bootstrap/seed
	@set -eo pipefail; d=$$(mktemp -d); trap 'rm -rf "$$d"' EXIT; \
	FINCROOT=$$(pwd) FINCORE_ROOT=$$(pwd) $(FINC2) build --emit-asm="$$d/seed.s" -o "$$d/seed_bin" ./compiler; \
	gzip -n -9 -c "$$d/seed.s" > $(SEED_GZ); \
	echo "update-seed: updated $(SEED_GZ) ($$(stat -c%s $(SEED_GZ)) bytes)"

test: ## run bootstrap Go unit tests
	$(GO) test -count=1 ./bootstrap/...

vet: ## go vet
	$(GO) vet ./...

fmt: ## gofmt all Go files
	gofmt -w $$(find bootstrap testdata -name '*.go')

fmt-check: ## fail if any Go file is not gofmt-ed
	@out=$$(gofmt -l $$(find bootstrap testdata -name '*.go')); test -z "$$out" || { echo "$$out"; exit 1; }

lint: fmt-check vet ## fmt-check + vet

test-corpus: finc0 ## run finc0 test on testdata/ (native static ELF64 binaries)
	$(FINC0) test -v testdata/

test-examples: finc ## compile all examples/*.fc and versioned examples/v0{3,4,5,6,7}/*.fc with self-hosted bin/finc
	@set -eo pipefail; d=$$(mktemp -d); trap 'rm -rf "$$d"' EXIT; \
	for f in examples/*.fc examples/v03/*.fc examples/v04/*.fc examples/v05/*.fc examples/v06/*.fc examples/v07/*.fc; do \
	  o=$${f%.fc}.out; \
	  $(FINC) build --comptime-fuel=$(FUEL) -o "$$d/example" "$$f"; \
	  if [ -f "$$o" ]; then \
	    "$$d/example" | cmp -s - "$$o" || { echo "test-examples: $$f output differs from $$o"; exit 1; }; \
	  fi; \
	done; \
	echo "test-examples: ok"

integration: finc0 ## run end-to-end native TCP echo + corpus integration tests
	FINC0=$$(pwd)/$(FINC0) $(GO) test -count=1 -v ./testdata/integration/

ci: lint test build test-corpus test-examples integration bootstrap ## full CI + self-hosting fixed-point pipeline

clean: ## remove build outputs
	rm -rf $(BIN)
