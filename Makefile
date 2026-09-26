.PHONY: build test vet check
build:
	go build -o bin/cham ./cmd/cham
test:
	go test ./...
vet:
	go vet ./...
check: test vet
	@! go list -deps ./... | grep -E '(sqlite|gorilla)'
