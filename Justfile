set unstable := true

[private]
default:
    @just --list

# Build hrd
build *ARGS:
    go build {{ ARGS }} -o hrd ./cmd/hrd

# Run tests with the race detector
test *ARGS:
    go test ./... -race {{ ARGS }}

# Format Go source
fmt *ARGS='.':
    gofmt -w {{ ARGS }}

# Check Go source formatting
fmt-check:
    test -z "$(gofmt -l .)"

# Run static analysis
vet:
    go vet ./...

# Run golangci-lint
lint:
    golangci-lint run

# Check dependencies for known vulnerabilities
vuln:
    govulncheck ./...

# Check module files without rewriting them
tidy-check:
    go mod tidy -diff

# Build release archives without publishing them
release-check:
    goreleaser release --snapshot --clean

# Run all local checks
check: fmt-check test vet lint vuln tidy-check build release-check
