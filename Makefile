default: all

.PHONY: all
all: clean build test check

VERSION := `git describe --abbrev=0 --tags || echo "0.0.0"`
BUILD := `git rev-parse --short HEAD`
LDFLAGS=-ldflags "-X=github.com/peak/anthropics/v2/version.GitCommit=$(BUILD)"

.PHONY: build
build:
	@go get ./...
	@go build ${GCFLAGS} ${LDFLAGS} -mod=mod .

TEST_TYPE:=test_with_race
ifeq ($(OS),Windows_NT)
	TEST_TYPE=test_without_race
endif

.PHONY: test
test: $(TEST_TYPE)

.PHONY: test_with_race
test_with_race:
	@S5CMD_BUILD_BINARY_WITHOUT_RACE_FLAG=0 go test -count=1 -race ./...

.PHONY: test_without_race
test_without_race:
	@S5CMD_BUILD_BINARY_WITHOUT_RACE_FLAG=1 go test -count=1 ./...

.PHONY: check
check: vet staticcheck check-fmt check-codegen

.PHONY: staticcheck
staticcheck:
	@go install honnef.co/go/tools/cmd/staticcheck@2023.1.7
	@staticcheck -checks 'all,-ST1000' ./...

.PHONY: vet
vet:
	@go vet ./...

.PHONY: check-fmt
check-fmt:
	@if [ $$(go fmt ./...) ]; then\
		echo "Go code is not formatted";\
		exit 1;\
	fi

.PHONY: check-codegen
check-codegen: gogenerate ## Check generated code is up-to-date
	@git diff --exit-code --

.PHONY: gogenerate
gogenerate:
	@go install go.uber.org/mock/mockgen@0.4.0
	@go generate ./...

.PHONY: clean
clean:
	@rm -f ./s5cmd

.NOTPARALLEL:
