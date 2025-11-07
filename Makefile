.PHONY: build build-lambda build-appender deploy clean test

build: build-lambda build-appender

build-lambda:
	@echo "Building Lambda function..."
	cd lambda && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -tags lambda.norpc -o bootstrap ./cmd/tinytail

build-appender:
	@echo "Building Logback appender..."
	cd logback-appender && gradle build

deploy: build
	@echo "Deploying to AWS..."
	cd infrastructure && sam deploy --guided

deploy-fast: build
	@echo "Deploying to AWS (no prompts)..."
	cd infrastructure && sam deploy

clean:
	@echo "Cleaning build artifacts..."
	rm -f lambda/bootstrap
	cd logback-appender && gradle clean
	rm -rf infrastructure/.aws-sam

test:
	@echo "Running Go tests..."
	cd lambda && go test ./...

local:
	@echo "Starting local API..."
	cd infrastructure && sam local start-api

deps:
	@echo "Installing Go dependencies..."
	cd lambda && go mod download
	@echo "Done"