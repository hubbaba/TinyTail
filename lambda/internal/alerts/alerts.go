package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	sesTypes "github.com/aws/aws-sdk-go-v2/service/ses/types"
	"github.com/tinytail/tinytail/internal/store"
)

type AlertRule struct {
	Pattern string `json:"pattern"`
	Window  string `json:"window"`
	Email   string `json:"email"`
}

type AlertHandler struct {
	logStore        *store.LogStore
	dbClient        *dynamodb.Client
	sesClient       *ses.Client
	alertsTableName string
	rules           []AlertRule
}

func NewAlertHandler(logStore *store.LogStore, dbClient *dynamodb.Client, sesClient *ses.Client, alertsTableName string) (*AlertHandler, error) {
	// Read alert rules from config file
	rulesFile := "alert-rules.json"
	rulesData, err := os.ReadFile(rulesFile)
	if err != nil {
		log.Printf("No alert rules file found (%s), alerts disabled: %v", rulesFile, err)
		return &AlertHandler{
			logStore:        logStore,
			dbClient:        dbClient,
			sesClient:       sesClient,
			alertsTableName: alertsTableName,
			rules:           []AlertRule{},
		}, nil
	}

	var rules []AlertRule
	if err := json.Unmarshal(rulesData, &rules); err != nil {
		// Don't crash - just log the error and continue with no rules
		log.Printf("WARNING: Failed to parse %s, alerts disabled: %v", rulesFile, err)
		return &AlertHandler{
			logStore:        logStore,
			dbClient:        dbClient,
			sesClient:       sesClient,
			alertsTableName: alertsTableName,
			rules:           []AlertRule{},
		}, nil
	}

	log.Printf("Loaded %d alert rules from %s", len(rules), rulesFile)
	return &AlertHandler{
		logStore:        logStore,
		dbClient:        dbClient,
		sesClient:       sesClient,
		alertsTableName: alertsTableName,
		rules:           rules,
	}, nil
}

func (a *AlertHandler) ProcessAlerts(ctx context.Context) error {
	if len(a.rules) == 0 {
		return nil
	}

	log.Printf("Processing %d alert rules", len(a.rules))

	for i, rule := range a.rules {
		if err := a.processRule(ctx, i, rule); err != nil {
			log.Printf("Error processing rule %d: %v", i, err)
			// Continue processing other rules
		}
	}

	return nil
}

func (a *AlertHandler) processRule(ctx context.Context, ruleIndex int, rule AlertRule) error {
	// Parse window
	windowDuration, err := parseWindow(rule.Window)
	if err != nil {
		return fmt.Errorf("invalid window: %w", err)
	}

	ruleID := fmt.Sprintf("rule-%d", ruleIndex)

	// Check if we've already alerted within the window
	shouldAlert, err := a.shouldSendAlert(ctx, ruleID, windowDuration)
	if err != nil {
		return fmt.Errorf("failed to check alert state: %w", err)
	}

	if !shouldAlert {
		log.Printf("Rule %d: skipping (already alerted within window)", ruleIndex)
		return nil
	}

	// Query logs for matches (limit to 200 to avoid expensive scans)
	startTime := time.Now().Add(-windowDuration)
	logs, err := a.logStore.SearchLogsWithLimit(ctx, rule.Pattern, startTime, time.Now(), 200)
	if err != nil {
		return fmt.Errorf("failed to search logs: %w", err)
	}

	if len(logs) == 0 {
		log.Printf("Rule %d: no matches found", ruleIndex)
		return nil
	}

	log.Printf("Rule %d: found %d matches", ruleIndex, len(logs))

	// Send alert email
	if err := a.sendAlertEmail(ctx, rule, logs, windowDuration); err != nil {
		// Don't fail - just log the error and continue
		log.Printf("Rule %d: WARNING - failed to send alert email: %v", ruleIndex, err)
		log.Printf("Rule %d: skipping alert (email failed, will retry on next match)", ruleIndex)
		return nil
	}

	// Update alert state (only if email sent successfully)
	if err := a.recordAlert(ctx, ruleID, len(logs), windowDuration); err != nil {
		log.Printf("Rule %d: WARNING - failed to record alert state: %v", ruleIndex, err)
		// Continue anyway - email was sent
	}

	log.Printf("Rule %d: alert sent successfully", ruleIndex)
	return nil
}

func (a *AlertHandler) shouldSendAlert(ctx context.Context, ruleID string, window time.Duration) (bool, error) {
	result, err := a.dbClient.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(a.alertsTableName),
		Key: map[string]types.AttributeValue{
			"ruleID": &types.AttributeValueMemberS{Value: ruleID},
		},
	})
	if err != nil {
		return false, err
	}

	if result.Item == nil {
		return true, nil
	}

	// Check last alert time
	if lastAlertAttr, ok := result.Item["lastAlertSent"]; ok {
		if lastAlertNum, ok := lastAlertAttr.(*types.AttributeValueMemberN); ok {
			var lastAlertUnix int64
			fmt.Sscanf(lastAlertNum.Value, "%d", &lastAlertUnix)
			lastAlertTime := time.Unix(lastAlertUnix, 0)
			if time.Since(lastAlertTime) < window {
				return false, nil
			}
		}
	}

	return true, nil
}

func (a *AlertHandler) recordAlert(ctx context.Context, ruleID string, matchCount int, window time.Duration) error {
	now := time.Now()
	ttl := now.Add(window + 24*time.Hour).Unix()

	_, err := a.dbClient.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(a.alertsTableName),
		Item: map[string]types.AttributeValue{
			"ruleID":        &types.AttributeValueMemberS{Value: ruleID},
			"lastAlertSent": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", now.Unix())},
			"matchCount":    &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", matchCount)},
			"ttl":           &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", ttl)},
		},
	})
	return err
}

func (a *AlertHandler) sendAlertEmail(ctx context.Context, rule AlertRule, logs []store.LogEntry, window time.Duration) error {
	maxLogsInEmail := 20
	truncated := len(logs) > maxLogsInEmail

	subject := fmt.Sprintf("[TinyTail Alert] %s (%d matches in %s)",
		truncateString(rule.Pattern, 50), len(logs), formatDuration(window))

	// Build email body
	var body strings.Builder
	body.WriteString(fmt.Sprintf("Found %d matches for pattern: %s\n", len(logs), rule.Pattern))
	body.WriteString(fmt.Sprintf("Time window: %s\n\n", formatDuration(window)))
	body.WriteString("Matching logs:\n")
	body.WriteString(strings.Repeat("=", 80) + "\n\n")

	displayLogs := logs
	if truncated {
		displayLogs = logs[:maxLogsInEmail]
	}

	for _, entry := range displayLogs {
		body.WriteString(fmt.Sprintf("[%s] [%s] [%s]\n",
			entry.Timestamp.Format("2006-01-02 15:04:05"),
			entry.Level,
			entry.Source))
		body.WriteString(fmt.Sprintf("%s\n\n", entry.Message))
	}

	if truncated {
		body.WriteString(fmt.Sprintf("... and %d more matches (showing first %d)\n\n",
			len(logs)-maxLogsInEmail, maxLogsInEmail))
	}

	body.WriteString(strings.Repeat("=", 80) + "\n")
	body.WriteString(fmt.Sprintf("\nAutomated alert from TinyTail | %s\n", time.Now().Format(time.RFC3339)))

	// Send via SES
	fromEmail := os.Getenv("TINYTAIL_ALERT_FROM_EMAIL")
	if fromEmail == "" {
		fromEmail = rule.Email // Fallback to recipient if not set
	}

	input := &ses.SendEmailInput{
		Source: aws.String(fromEmail),
		Destination: &sesTypes.Destination{
			ToAddresses: []string{rule.Email},
		},
		Message: &sesTypes.Message{
			Subject: &sesTypes.Content{
				Data: aws.String(subject),
			},
			Body: &sesTypes.Body{
				Text: &sesTypes.Content{
					Data: aws.String(body.String()),
				},
			},
		},
	}

	_, err := a.sesClient.SendEmail(ctx, input)
	return err
}

func parseWindow(window string) (time.Duration, error) {
	window = strings.TrimSpace(strings.ToLower(window))

	if strings.HasSuffix(window, "m") {
		minutes := strings.TrimSuffix(window, "m")
		mins, err := time.ParseDuration(minutes + "m")
		if err != nil {
			return 0, err
		}
		return mins, nil
	}

	if strings.HasSuffix(window, "h") {
		return time.ParseDuration(window)
	}

	if strings.HasSuffix(window, "d") {
		days := strings.TrimSuffix(window, "d")
		var d int
		fmt.Sscanf(days, "%d", &d)
		return time.Duration(d) * 24 * time.Hour, nil
	}

	return time.ParseDuration(window)
}

func formatDuration(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
