SHELL := /bin/bash
GO ?= go
EMULATE_DIR ?= ../_local/agent-emulate
MOCK := testing/anthropic-mock.js
GH_SHIM_DIR := $(PWD)/testing/gh-shim

# Routing for the local emulators (see TESTING.md).
BRAIN := --base-url http://localhost:4009
AWS_EMU_ENV := AWS_ENDPOINT_URL=http://localhost:4006 AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_DEFAULT_REGION=us-east-1

PROMPT ?= list my s3 buckets

.PHONY: help build fmt vet test test-all servers-up servers-down demo-aws demo-gh repl-aws claude-auth

help: ## list targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

build: ## build ./ask
	$(GO) build -o ask ./cmd/ant

claude-auth: ## reuse the device's Claude Code OAuth (macOS keychain) as ask's login
	$(GO) run ./testing/claudecode-auth

fmt: ## gofmt the provider files
	gofmt -w pkg/cmd/provider*.go

vet: ## go vet the cmd package
	$(GO) vet ./pkg/cmd/

test: ## run the provider unit tests (no servers needed)
	$(GO) test ./pkg/cmd/ -run 'AWS|GH|Provider|AgentLoop' -count=1

test-all: ## full suite (starts the Stainless mock on :4010 first)
	./scripts/mock --daemon && $(GO) test ./... -count=1

servers-up: build ## start the aws+github emulators and the brain mock
	@cd $(EMULATE_DIR) && (lsof -ti:4006 >/dev/null 2>&1 || nohup npx agent-emulate --service aws --port 4006 >/tmp/emu-aws.log 2>&1 &)
	@cd $(EMULATE_DIR) && (lsof -ti:4001 >/dev/null 2>&1 || nohup npx agent-emulate --service github --port 4001 >/tmp/emu-gh.log 2>&1 &)
	@(lsof -ti:4009 >/dev/null 2>&1 || nohup node $(MOCK) >/tmp/mock.log 2>&1 &)
	@sleep 7; echo "up: :4001 github, :4006 aws, :4009 brain-mock"

servers-down: ## stop the github emulator + brain mock (leaves :4006 aws up)
	-@lsof -ti:4001,4009 | xargs kill 2>/dev/null; echo "stopped :4001 and :4009"

demo-aws: build ## ask aws against the emulator: make demo-aws PROMPT="..."
	ANTHROPIC_API_KEY=dummy $(AWS_EMU_ENV) ./ask $(BRAIN) aws --yes "$(PROMPT)"

demo-gh: build ## ask gh against the emulator (shim): make demo-gh PROMPT="list my repos"
	PATH=$(GH_SHIM_DIR):$$PATH ANTHROPIC_API_KEY=dummy GH_ENTERPRISE_TOKEN=test_token_admin ./ask $(BRAIN) gh --yes "$(PROMPT)"

repl-aws: build ## interactive REPL against the emulator
	ANTHROPIC_API_KEY=dummy $(AWS_EMU_ENV) ./ask $(BRAIN) aws
