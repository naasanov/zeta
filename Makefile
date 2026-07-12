# zsh-autopilot — top-level developer tasks.
# The Go daemon is a separate module under daemon/.

GO   ?= go
BIN  ?= bin

# zsh client: hand-edited fragments in zsh/ are concatenated into the committed
# bundle below, in filename order. Fragments carry a 2-digit numeric prefix
# (10_, 20_, 30_ ...) that encodes source order — zsh is sourced top-to-bottom,
# so e.g. config precedes bind/widgets and start comes last. Gaps of 10 leave
# room to insert without renumbering. Only NUMBERED files are bundled, so an
# unprefixed fragment in zsh/ is treated as work-in-progress and excluded.
# $(sort) orders lexically, which equals numeric order while prefixes stay 2 digits.
PLUGIN  ?= zsh-autopilot.zsh
ZSH_SRC := $(sort $(wildcard zsh/[0-9]*.zsh))

.PHONY: all build daemon spike plugin hooks test fmt vet clean

all: build plugin

hooks: ## Install git hooks (regenerate the zsh bundle on commit)
	git config core.hooksPath .githooks
	@echo "git hooks installed: core.hooksPath=.githooks"

build: daemon ## Build the daemon binary

daemon: ## Build the daemon -> bin/autopilotd
	cd daemon && $(GO) build -o ../$(BIN)/autopilotd ./cmd/autopilotd

spike: ## Build the Phase 0 echo server -> bin/echo-server
	cd spike/echo-server && $(GO) build -o ../../$(BIN)/echo-server .

plugin: $(PLUGIN) ## Concatenate zsh/*.zsh fragments -> zsh-autopilot.zsh

$(PLUGIN): $(ZSH_SRC) LICENSE
	@printf '# %s — GENERATED FILE, DO NOT EDIT.\n' '$(PLUGIN)' > $@
	@printf '# Built from zsh/*.zsh by `make plugin`; edit the fragments there.\n#\n' >> $@
	@sed -e 's/^/# /' LICENSE >> $@
	@printf '\n' >> $@
	@cat $(ZSH_SRC) >> $@
	@echo "Built $@ from: $(ZSH_SRC)"

test: ## Run daemon tests with the race detector
	cd daemon && $(GO) test -race ./...

fmt: ## Format Go code
	cd daemon && $(GO) fmt ./...

vet: ## Vet Go code
	cd daemon && $(GO) vet ./...

clean: ## Remove build output
	rm -rf $(BIN)
