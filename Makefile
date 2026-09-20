# zsh-autopilot — top-level developer tasks.
# The Go daemon is a separate module under daemon/.

GO   ?= go
BIN  ?= bin

# Fragments carry a 2-digit numeric prefix encoding source order; $(sort)
# orders lexically, which matches numeric order only while prefixes stay
# 2 digits.
PLUGIN  ?= zsh-autopilot.zsh
ZSH_SRC := $(sort $(wildcard zsh/[0-9]*.zsh))

.PHONY: all build daemon plugin hooks test fmt vet clean

all: build plugin

hooks: ## Install git hooks (regenerate the zsh bundle on commit)
	git config core.hooksPath .githooks
	@echo "git hooks installed: core.hooksPath=.githooks"

build: daemon ## Build the daemon binary

daemon: ## Build the daemon -> bin/autopilotd
	cd daemon && $(GO) build -o ../$(BIN)/autopilotd ./cmd/autopilotd

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
