ok, .PHONY: build-TinyTailFunction

build-TinyTailFunction:
	@echo "Building Lambda function for custom runtime..."
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -tags lambda.norpc -o $(ARTIFACTS_DIR)/bootstrap ./cmd/tinytail
	@echo "Copying alert-rules.json..."
	cp -f alert-rules.json $(ARTIFACTS_DIR)/alert-rules.json || echo "Warning: alert-rules.json not found, alerts will be disabled"
	@echo "Build complete: $(ARTIFACTS_DIR)/bootstrap"