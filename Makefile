all: validate

validate:
	@go build ./...
	@go vet ./...
	@go tool vet -shadow .
	@go tool vet -shadow lib/twister/
	@golint ./...
	@ineffassign .
	@ineffassign lib/twister/
