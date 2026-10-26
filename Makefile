.PHONY: build-ApiFunction build-WorkerFunction build-AlexaFunction build-MapResolverFunction build-HourlyTelemetryFunction
build-ApiFunction:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -tags lambda.norpc -ldflags "-s -w -X main.Version=$(BUILD_COMMIT) -X main.Built=$(BUILD_TIME)" -o "$(ARTIFACTS_DIR)/bootstrap" ./cmd/api
build-WorkerFunction:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -tags lambda.norpc -ldflags "-s -w -X main.Version=$(BUILD_COMMIT) -X main.Built=$(BUILD_TIME)" -o "$(ARTIFACTS_DIR)/bootstrap" ./cmd/worker
build-AlexaFunction:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -tags lambda.norpc -ldflags "-s -w -X main.Version=$(BUILD_COMMIT) -X main.Built=$(BUILD_TIME)" -o "$(ARTIFACTS_DIR)/bootstrap" ./cmd/alexa
build-MapResolverFunction:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -tags lambda.norpc -ldflags "-s -w" -o "$(ARTIFACTS_DIR)/bootstrap" ./cmd/mapresolver
build-HourlyTelemetryFunction:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -tags lambda.norpc -ldflags "-s -w -X main.Version=$(BUILD_COMMIT)" -o "$(ARTIFACTS_DIR)/bootstrap" ./cmd/hourlytelemetry
