package handler

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/tinytail/tinytail/internal/store"
)

//go:embed ui/index.html
var indexHTML string

//go:embed ui/login.html
var loginHTML string

type Handler struct {
	logStore     *store.LogStore
	sessionStore *store.SessionStore
	ingestSecret string
	uiPassword   string
}

func NewHandler(logStore *store.LogStore, sessionStore *store.SessionStore, ingestSecret, uiPassword string) *Handler {
	return &Handler{
		logStore:     logStore,
		sessionStore: sessionStore,
		ingestSecret: ingestSecret,
		uiPassword:   uiPassword,
	}
}

func (h *Handler) Handle(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	switch {
	// Public routes - no auth required
	case request.HTTPMethod == "GET" && request.Path == "/login":
		return h.serveLoginPage()
	case request.HTTPMethod == "POST" && request.Path == "/auth/login":
		return h.handleLogin(ctx, request)
	case request.HTTPMethod == "POST" && request.Path == "/logs/ingest":
		return h.ingestLogs(ctx, request)

	// Protected routes - require session
	case request.HTTPMethod == "GET" && request.Path == "/":
		return h.requireAuth(ctx, request, h.serveIndex)
	case request.HTTPMethod == "POST" && request.Path == "/auth/logout":
		return h.requireAuth(ctx, request, h.handleLogout)
	case request.HTTPMethod == "GET" && request.Path == "/logs/latest":
		return h.requireAuth(ctx, request, h.getLatestLogs)
	case request.HTTPMethod == "GET" && request.Path == "/logs":
		return h.requireAuth(ctx, request, h.getLogs)
	case request.HTTPMethod == "GET" && request.Path == "/logs/date":
		return h.requireAuth(ctx, request, h.getLogsByDate)
	case request.HTTPMethod == "GET" && request.Path == "/logs/datetime":
		return h.requireAuth(ctx, request, h.getLogsByDateTime)
	case request.HTTPMethod == "GET" && request.Path == "/logs/search":
		return h.requireAuth(ctx, request, h.searchLogs)
	default:
		return jsonResponse(http.StatusNotFound, map[string]string{"error": "Not found"})
	}
}

// requireAuth wraps protected handlers with session validation
func (h *Handler) requireAuth(ctx context.Context, request events.APIGatewayProxyRequest, handler func(context.Context, events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error)) (events.APIGatewayProxyResponse, error) {
	// Extract session cookie
	sessionID := h.getSessionFromCookie(request)
	if sessionID == "" {
		return h.redirectToLogin(request)
	}

	// Validate session in DynamoDB
	valid, err := h.sessionStore.ValidateSession(ctx, sessionID)
	if err != nil || !valid {
		return h.redirectToLogin(request)
	}

	// Session valid, proceed to handler
	return handler(ctx, request)
}

func (h *Handler) getSessionFromCookie(request events.APIGatewayProxyRequest) string {
	cookieHeader := request.Headers["cookie"]
	if cookieHeader == "" {
		cookieHeader = request.Headers["Cookie"]
	}

	cookies := strings.Split(cookieHeader, ";")
	for _, cookie := range cookies {
		cookie = strings.TrimSpace(cookie)
		if strings.HasPrefix(cookie, "session=") {
			return strings.TrimPrefix(cookie, "session=")
		}
	}
	return ""
}

func (h *Handler) redirectToLogin(request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	// Build login path with stage prefix if present
	loginPath := "/login"
	if request.RequestContext.Stage != "" && request.RequestContext.Stage != "$default" {
		loginPath = "/" + request.RequestContext.Stage + "/login"
	}

	return events.APIGatewayProxyResponse{
		StatusCode: http.StatusFound,
		Headers: map[string]string{
			"Location": loginPath,
		},
	}, nil
}

func (h *Handler) serveLoginPage() (events.APIGatewayProxyResponse, error) {
	return events.APIGatewayProxyResponse{
		StatusCode: http.StatusOK,
		Headers: map[string]string{
			"Content-Type": "text/html",
		},
		Body: loginHTML,
	}, nil
}

func (h *Handler) serveIndex(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	return events.APIGatewayProxyResponse{
		StatusCode: http.StatusOK,
		Headers: map[string]string{
			"Content-Type": "text/html",
		},
		Body: indexHTML,
	}, nil
}

func (h *Handler) handleLogin(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	var loginReq struct {
		Password string `json:"password"`
	}

	if err := json.Unmarshal([]byte(request.Body), &loginReq); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
	}

	// Validate password
	if loginReq.Password != h.uiPassword {
		return jsonResponse(http.StatusUnauthorized, map[string]string{"error": "Invalid password"})
	}

	// Create session
	userAgent := request.Headers["user-agent"]
	if userAgent == "" {
		userAgent = request.Headers["User-Agent"]
	}

	sess, err := h.sessionStore.CreateSession(ctx, userAgent)
	if err != nil {
		fmt.Printf("ERROR: Failed to create session: %v\n", err)
		return jsonResponse(http.StatusInternalServerError, map[string]string{"error": "Failed to create session"})
	}

	// Set session cookie (2 weeks, HttpOnly, Secure, SameSite)
	cookie := fmt.Sprintf("session=%s; Max-Age=%d; Path=/; HttpOnly; Secure; SameSite=Strict",
		sess.SessionID, 14*24*60*60)

	return events.APIGatewayProxyResponse{
		StatusCode: http.StatusOK,
		Headers: map[string]string{
			"Content-Type": "application/json",
			"Set-Cookie":   cookie,
		},
		Body: `{"success": true}`,
	}, nil
}

func (h *Handler) handleLogout(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	sessionID := h.getSessionFromCookie(request)
	if sessionID != "" {
		_ = h.sessionStore.DeleteSession(ctx, sessionID)
	}

	// Clear cookie
	cookie := "session=; Max-Age=0; Path=/; HttpOnly; Secure; SameSite=Strict"

	return events.APIGatewayProxyResponse{
		StatusCode: http.StatusOK,
		Headers: map[string]string{
			"Content-Type": "application/json",
			"Set-Cookie":   cookie,
		},
		Body: `{"success": true}`,
	}, nil
}

func (h *Handler) ingestLogs(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	authHeader := request.Headers["authorization"]
	if authHeader == "" {
		authHeader = request.Headers["Authorization"]
	}

	expectedAuth := "Bearer " + h.ingestSecret
	if authHeader != expectedAuth {
		return jsonResponse(http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
	}

	var entry store.LogEntry
	if err := json.Unmarshal([]byte(request.Body), &entry); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
	}

	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}

	if entry.Level == "" {
		entry.Level = "INFO"
	}

	if err := h.logStore.StoreLogEntry(ctx, &entry); err != nil {
		// Log the actual error for debugging
		fmt.Printf("ERROR: Failed to store log entry: %v\n", err)
		return jsonResponse(http.StatusInternalServerError, map[string]string{"error": "Failed to store log"})
	}

	return jsonResponse(http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) getLatestLogs(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	limitStr := request.QueryStringParameters["limit"]

	limit := 100
	if limitStr != "" {
		parsedLimit, err := strconv.Atoi(limitStr)
		if err != nil || parsedLimit < 1 {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "Invalid limit parameter"})
		}
		limit = parsedLimit
	}

	logs, err := h.logStore.GetLogs(ctx, limit, "", "")
	if err != nil {
		fmt.Printf("ERROR: Failed to query latest logs: %v\n", err)
		return jsonResponse(http.StatusInternalServerError, map[string]string{"error": "Failed to query logs"})
	}

	return jsonResponse(http.StatusOK, logs)
}

func (h *Handler) getLogs(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	limitStr := request.QueryStringParameters["limit"]
	afterCursor := request.QueryStringParameters["after"]
	beforeCursor := request.QueryStringParameters["before"]

	limit := 100
	if limitStr != "" {
		parsedLimit, err := strconv.Atoi(limitStr)
		if err != nil || parsedLimit < 1 {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "Invalid limit parameter"})
		}
		limit = parsedLimit
	}

	logs, err := h.logStore.GetLogs(ctx, limit, afterCursor, beforeCursor)
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to query logs: %v", err)})
	}

	return jsonResponse(http.StatusOK, logs)
}

func (h *Handler) getLogsByDate(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	dateStr := request.QueryStringParameters["date"]
	if dateStr == "" {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "Missing date parameter"})
	}

	var targetTime time.Time
	var err error

	targetTime, err = time.Parse(time.RFC3339, dateStr)
	if err != nil {
		targetTime, err = time.Parse("2006-01-02", dateStr)
		if err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "Invalid date format. Use YYYY-MM-DD or RFC3339"})
		}
		targetTime = targetTime.Add(12 * time.Hour)
	}

	logsBefore, err := h.logStore.GetLogsByTimeRange(ctx, targetTime.Add(-24*time.Hour), targetTime, 100)
	if err != nil {
		fmt.Printf("ERROR: Failed to query logs before date: %v\n", err)
		return jsonResponse(http.StatusInternalServerError, map[string]string{"error": "Failed to query logs before date"})
	}

	logsAfter, err := h.logStore.GetLogsByTimeRange(ctx, targetTime, targetTime.Add(24*time.Hour), 100)
	if err != nil {
		fmt.Printf("ERROR: Failed to query logs after date: %v\n", err)
		return jsonResponse(http.StatusInternalServerError, map[string]string{"error": "Failed to query logs after date"})
	}

	allLogs := append(logsBefore, logsAfter...)

	return jsonResponse(http.StatusOK, allLogs)
}

func (h *Handler) getLogsByDateTime(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	timestampStr := request.QueryStringParameters["timestamp"]
	if timestampStr == "" {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "Missing timestamp parameter"})
	}

	targetTime, err := time.Parse(time.RFC3339, timestampStr)
	if err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "Invalid timestamp format. Use RFC3339"})
	}

	// Convert target time to ULID cursor
	targetCursor := h.logStore.TimeToCursor(targetTime)

	// Get 100 logs before the target cursor (no time window - just the 100 logs before this cursor)
	logsBefore, err := h.logStore.GetLogs(ctx, 100, "", targetCursor)
	if err != nil {
		fmt.Printf("ERROR: Failed to query logs before datetime: %v\n", err)
		return jsonResponse(http.StatusInternalServerError, map[string]string{"error": "Failed to query logs before datetime"})
	}

	// Get 100 logs after the target cursor (no time window - just the 100 logs after this cursor)
	logsAfter, err := h.logStore.GetLogs(ctx, 100, targetCursor, "")
	if err != nil {
		fmt.Printf("ERROR: Failed to query logs after datetime: %v\n", err)
		return jsonResponse(http.StatusInternalServerError, map[string]string{"error": "Failed to query logs after datetime"})
	}

	// Combine: logs before (already in chronological order) + logs after (already in chronological order)
	allLogs := append(logsBefore, logsAfter...)

	return jsonResponse(http.StatusOK, allLogs)
}

func (h *Handler) searchLogs(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	query := request.QueryStringParameters["q"]
	startStr := request.QueryStringParameters["start"]
	endStr := request.QueryStringParameters["end"]
	limitStr := request.QueryStringParameters["limit"]
	beforeCursor := request.QueryStringParameters["before"]

	var startTime, endTime time.Time
	var err error

	if startStr != "" {
		startTime, err = time.Parse(time.RFC3339, startStr)
		if err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "Invalid start time format"})
		}
	} else {
		startTime = time.Now().Add(-24 * time.Hour)
	}

	if endStr != "" {
		endTime, err = time.Parse(time.RFC3339, endStr)
		if err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "Invalid end time format"})
		}
	} else {
		endTime = time.Now()
	}

	// Parse limit parameter
	limit := 0
	if limitStr != "" {
		parsedLimit, err := strconv.Atoi(limitStr)
		if err != nil || parsedLimit < 1 {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "Invalid limit parameter"})
		}
		limit = parsedLimit
	}

	logs, err := h.logStore.SearchLogsWithCursor(ctx, query, startTime, endTime, beforeCursor, limit)
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to search logs: %v", err)})
	}

	return jsonResponse(http.StatusOK, logs)
}

func jsonResponse(statusCode int, body interface{}) (events.APIGatewayProxyResponse, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return events.APIGatewayProxyResponse{
			StatusCode: http.StatusInternalServerError,
			Body:       `{"error": "Failed to marshal response"}`,
		}, nil
	}

	return events.APIGatewayProxyResponse{
		StatusCode: statusCode,
		Headers: map[string]string{
			"Content-Type":                "application/json",
			"Access-Control-Allow-Origin": "*",
		},
		Body: string(jsonBody),
	}, nil
}
