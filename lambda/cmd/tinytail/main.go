package main

import (
	"context"
	"encoding/json"
	"log"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/tinytail/tinytail/internal/alerts"
	"github.com/tinytail/tinytail/internal/handler"
	"github.com/tinytail/tinytail/internal/store"
)

type UniversalHandler struct {
	httpHandler  *handler.Handler
	alertHandler *alerts.AlertHandler
}

func (u *UniversalHandler) Handle(ctx context.Context, event json.RawMessage) (interface{}, error) {
	// Try to detect event type by checking for API Gateway fields
	var apiGatewayCheck map[string]interface{}
	if err := json.Unmarshal(event, &apiGatewayCheck); err == nil {
		if _, hasRequestContext := apiGatewayCheck["requestContext"]; hasRequestContext {
			// It's an API Gateway event
			var apiEvent events.APIGatewayProxyRequest
			if err := json.Unmarshal(event, &apiEvent); err != nil {
				return nil, err
			}
			return u.httpHandler.Handle(ctx, apiEvent)
		}

		// Check for EventBridge event
		if _, hasSource := apiGatewayCheck["source"]; hasSource {
			if _, hasDetailType := apiGatewayCheck["detail-type"]; hasDetailType {
				// It's an EventBridge event - process alerts
				log.Println("Processing alerts triggered by EventBridge")
				return nil, u.alertHandler.ProcessAlerts(ctx)
			}
		}
	}

	log.Printf("Unknown event type: %s", string(event))
	return nil, nil
}

func main() {
	tableName := os.Getenv("TINYTAIL_TABLE_NAME")
	if tableName == "" {
		tableName = "TinyTailLogs"
	}

	sessionsTableName := os.Getenv("TINYTAIL_SESSIONS_TABLE_NAME")
	if sessionsTableName == "" {
		sessionsTableName = "TinyTailSessions"
	}

	alertsTableName := os.Getenv("TINYTAIL_ALERTS_TABLE_NAME")
	if alertsTableName == "" {
		alertsTableName = "TinyTailAlerts"
	}

	ingestSecret := os.Getenv("TINYTAIL_INGEST_SECRET")
	if ingestSecret == "" {
		log.Fatal("TINYTAIL_INGEST_SECRET environment variable is required")
	}

	uiPassword := os.Getenv("TINYTAIL_UI_PASSWORD")
	if uiPassword == "" {
		log.Fatal("TINYTAIL_UI_PASSWORD environment variable is required")
	}

	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("Failed to load AWS config: %v", err)
	}

	dbClient := dynamodb.NewFromConfig(cfg)
	sesClient := ses.NewFromConfig(cfg)

	logStore := store.NewLogStore(dbClient, tableName)
	sessionStore := store.NewSessionStore(dbClient, sessionsTableName)

	httpHandler := handler.NewHandler(logStore, sessionStore, ingestSecret, uiPassword)

	alertHandler, err := alerts.NewAlertHandler(logStore, dbClient, sesClient, alertsTableName)
	if err != nil {
		log.Fatalf("Failed to create alert handler: %v", err)
	}

	universalHandler := &UniversalHandler{
		httpHandler:  httpHandler,
		alertHandler: alertHandler,
	}

	lambda.Start(universalHandler.Handle)
}
