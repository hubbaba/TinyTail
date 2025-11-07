package store

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/oklog/ulid/v2"
)

const (
	MaxMessageSize = 350 * 1024 //350KB
	TTLDays        = 180
	PartitionKey   = "LOGS"
)

// LogEntry represents a log entry exposed to handlers and API
type LogEntry struct {
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	Source    string    `json:"source"`
	Logger    string    `json:"logger"`
	Timestamp time.Time `json:"timestamp"`
	RequestID string    `json:"request_id"`
	Cursor    string    `json:"cursor,omitempty"`
}

// dynamoDBLogItem represents a log item as stored in DynamoDB (internal use only)
type dynamoDBLogItem struct {
	PK           string `dynamodbav:"pk"`
	TimestampSeq string `dynamodbav:"timestamp_seq"`
	Timestamp    string `dynamodbav:"timestamp"`
	Level        string `dynamodbav:"level"`
	Message      string `dynamodbav:"message"`
	Source       string `dynamodbav:"source"`
	Logger       string `dynamodbav:"logger"`
	RequestID    string `dynamodbav:"request_id"`
	ChunkIndex   int    `dynamodbav:"chunk_index,omitempty"`
	TotalChunks  int    `dynamodbav:"total_chunks,omitempty"`
	ExpireAt     int64  `dynamodbav:"expire_at,omitempty"`
}

type LogStore struct {
	client    *dynamodb.Client
	tableName string
}

func NewLogStore(client *dynamodb.Client, tableName string) *LogStore {
	return &LogStore{
		client:    client,
		tableName: tableName,
	}
}

func (s *LogStore) StoreLogEntry(ctx context.Context, entry *LogEntry) error {
	id := ulid.MustNew(ulid.Timestamp(entry.Timestamp), rand.Reader)
	ulidStr := id.String()

	messageBytes := []byte(entry.Message)
	messageSize := len(messageBytes)

	if messageSize <= MaxMessageSize {
		return s.storeSingleItem(ctx, entry, ulidStr, 0, 1)
	}

	return s.storeChunkedItems(ctx, entry, ulidStr, messageBytes)
}

func (s *LogStore) storeSingleItem(ctx context.Context, entry *LogEntry, ulidStr string, chunkIndex, totalChunks int) error {
	expireAt := time.Now().Add(TTLDays * 24 * time.Hour).Unix()

	// Use default request_id if empty (DynamoDB GSI requires non-empty strings)
	requestID := entry.RequestID
	if requestID == "" {
		requestID = "none"
	}

	item := dynamoDBLogItem{
		PK:           PartitionKey,
		TimestampSeq: fmt.Sprintf("%s#%d", ulidStr, chunkIndex),
		Timestamp:    entry.Timestamp.Format(time.RFC3339Nano),
		Level:        entry.Level,
		Message:      entry.Message,
		Source:       entry.Source,
		Logger:       entry.Logger,
		RequestID:    requestID,
		ChunkIndex:   chunkIndex,
		TotalChunks:  totalChunks,
		ExpireAt:     expireAt,
	}

	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return fmt.Errorf("failed to marshal item: %w", err)
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.tableName),
		Item:      av,
	})

	return err
}

func (s *LogStore) storeChunkedItems(ctx context.Context, entry *LogEntry, ulidStr string, messageBytes []byte) error {
	totalChunks := (len(messageBytes) + MaxMessageSize - 1) / MaxMessageSize
	expireAt := time.Now().Add(TTLDays * 24 * time.Hour).Unix()

	// Use default request_id if empty (DynamoDB GSI requires non-empty strings)
	requestID := entry.RequestID
	if requestID == "" {
		requestID = "none"
	}

	for i := 0; i < totalChunks; i++ {
		start := i * MaxMessageSize
		end := start + MaxMessageSize
		if end > len(messageBytes) {
			end = len(messageBytes)
		}

		chunk := string(messageBytes[start:end])

		item := dynamoDBLogItem{
			PK:           PartitionKey,
			TimestampSeq: fmt.Sprintf("%s#%d", ulidStr, i),
			Timestamp:    entry.Timestamp.Format(time.RFC3339Nano),
			Level:        entry.Level,
			Message:      chunk,
			Source:       entry.Source,
			Logger:       entry.Logger,
			RequestID:    requestID,
			ChunkIndex:   i,
			TotalChunks:  totalChunks,
			ExpireAt:     expireAt,
		}

		av, err := attributevalue.MarshalMap(item)
		if err != nil {
			return fmt.Errorf("failed to marshal chunk %d: %w", i, err)
		}

		_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
			TableName: aws.String(s.tableName),
			Item:      av,
		})
		if err != nil {
			return fmt.Errorf("failed to store chunk %d: %w", i, err)
		}
	}

	return nil
}

func (s *LogStore) GetRecentLogs(ctx context.Context, minutes int) ([]LogEntry, error) {
	startTime := time.Now().Add(-time.Duration(minutes) * time.Minute)
	return s.queryLogsByTimeRange(ctx, startTime, time.Now())
}

func (s *LogStore) GetLogsByDate(ctx context.Context, date string) ([]LogEntry, error) {
	startTime, err := time.Parse("2006-01-02", date)
	if err != nil {
		return nil, fmt.Errorf("invalid date format: %w", err)
	}
	endTime := startTime.Add(24 * time.Hour)

	return s.queryLogsByTimeRange(ctx, startTime, endTime)
}

func (s *LogStore) SearchLogs(ctx context.Context, query string, startTime, endTime time.Time) ([]LogEntry, error) {
	return s.SearchLogsWithLimit(ctx, query, startTime, endTime, 0)
}

func (s *LogStore) SearchLogsWithLimit(ctx context.Context, query string, startTime, endTime time.Time, limit int) ([]LogEntry, error) {
	logs, err := s.queryLogsByTimeRange(ctx, startTime, endTime)
	if err != nil {
		return nil, err
	}

	if query == "" {
		if limit > 0 && len(logs) > limit {
			return logs[:limit], nil
		}
		return logs, nil
	}

	var filtered []LogEntry
	lowerQuery := strings.ToLower(query)
	for _, log := range logs {
		if strings.Contains(strings.ToLower(log.Message), lowerQuery) ||
			strings.Contains(strings.ToLower(log.Level), lowerQuery) ||
			strings.Contains(strings.ToLower(log.Source), lowerQuery) {
			filtered = append(filtered, log)
			if limit > 0 && len(filtered) >= limit {
				break
			}
		}
	}

	return filtered, nil
}

func (s *LogStore) queryLogsByTimeRange(ctx context.Context, startTime, endTime time.Time) ([]LogEntry, error) {
	startULID := ulid.MustNew(ulid.Timestamp(startTime), nil)
	endULID := ulid.MustNew(ulid.Timestamp(endTime), nil)

	input := &dynamodb.QueryInput{
		TableName:              aws.String(s.tableName),
		KeyConditionExpression: aws.String("pk = :pk AND timestamp_seq BETWEEN :start AND :end"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":    &types.AttributeValueMemberS{Value: PartitionKey},
			":start": &types.AttributeValueMemberS{Value: startULID.String() + "#0"},
			":end":   &types.AttributeValueMemberS{Value: endULID.String() + "#999"},
		},
		ScanIndexForward: aws.Bool(false),
	}

	var allLogs []LogEntry
	paginator := dynamodb.NewQueryPaginator(s.client, input)
	for paginator.HasMorePages() {
		output, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to query logs: %w", err)
		}

		items, err := s.unmarshalAndReassemble(output.Items)
		if err != nil {
			return nil, err
		}

		allLogs = append(allLogs, items...)
	}

	return allLogs, nil
}

func (s *LogStore) unmarshalAndReassemble(items []map[string]types.AttributeValue) ([]LogEntry, error) {
	chunkedMessages := make(map[string][]dynamoDBLogItem)
	var singleMessages []dynamoDBLogItem

	for _, item := range items {
		var dbItem dynamoDBLogItem
		err := attributevalue.UnmarshalMap(item, &dbItem)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal item: %w", err)
		}

		if dbItem.TotalChunks > 1 {
			ulidPart := strings.Split(dbItem.TimestampSeq, "#")[0]
			chunkedMessages[ulidPart] = append(chunkedMessages[ulidPart], dbItem)
		} else {
			singleMessages = append(singleMessages, dbItem)
		}
	}

	var logs []LogEntry

	for _, chunks := range chunkedMessages {
		if len(chunks) == 0 {
			continue
		}

		firstChunk := chunks[0]
		var fullMessage strings.Builder
		for _, chunk := range chunks {
			fullMessage.WriteString(chunk.Message)
		}

		timestamp, _ := time.Parse(time.RFC3339Nano, firstChunk.Timestamp)
		ulidCursor := strings.Split(firstChunk.TimestampSeq, "#")[0]

		logs = append(logs, LogEntry{
			Level:     firstChunk.Level,
			Message:   fullMessage.String(),
			Source:    firstChunk.Source,
			Logger:    firstChunk.Logger,
			Timestamp: timestamp,
			RequestID: firstChunk.RequestID,
			Cursor:    ulidCursor,
		})
	}

	for _, dbItem := range singleMessages {
		timestamp, _ := time.Parse(time.RFC3339Nano, dbItem.Timestamp)
		ulidCursor := strings.Split(dbItem.TimestampSeq, "#")[0]

		logs = append(logs, LogEntry{
			Level:     dbItem.Level,
			Message:   dbItem.Message,
			Source:    dbItem.Source,
			Logger:    dbItem.Logger,
			Timestamp: timestamp,
			RequestID: dbItem.RequestID,
			Cursor:    ulidCursor,
		})
	}

	return logs, nil
}

func (s *LogStore) GetLogs(ctx context.Context, limit int, afterCursor, beforeCursor string) ([]LogEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}

	var keyCondition string
	var expressionValues map[string]types.AttributeValue
	var scanForward bool

	if afterCursor != "" {
		keyCondition = "pk = :pk AND timestamp_seq > :cursor"
		expressionValues = map[string]types.AttributeValue{
			":pk":     &types.AttributeValueMemberS{Value: PartitionKey},
			":cursor": &types.AttributeValueMemberS{Value: afterCursor + "#0"},
		}
		scanForward = true
	} else if beforeCursor != "" {
		keyCondition = "pk = :pk AND timestamp_seq < :cursor"
		expressionValues = map[string]types.AttributeValue{
			":pk":     &types.AttributeValueMemberS{Value: PartitionKey},
			":cursor": &types.AttributeValueMemberS{Value: beforeCursor + "#0"},
		}
		scanForward = false
	} else {
		keyCondition = "pk = :pk"
		expressionValues = map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: PartitionKey},
		}
		scanForward = false
	}

	input := &dynamodb.QueryInput{
		TableName:                 aws.String(s.tableName),
		KeyConditionExpression:    aws.String(keyCondition),
		ExpressionAttributeValues: expressionValues,
		ScanIndexForward:          aws.Bool(scanForward),
		Limit:                     aws.Int32(int32(limit * 2)),
	}

	output, err := s.client.Query(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to query logs: %w", err)
	}

	allLogs, err := s.unmarshalAndReassemble(output.Items)
	if err != nil {
		return nil, err
	}

	if beforeCursor != "" && !scanForward {
		for i, j := 0, len(allLogs)-1; i < j; i, j = i+1, j-1 {
			allLogs[i], allLogs[j] = allLogs[j], allLogs[i]
		}
	}

	if len(allLogs) > limit {
		allLogs = allLogs[:limit]
	}

	return allLogs, nil
}

func (s *LogStore) GetLogsByTimeRange(ctx context.Context, startTime, endTime time.Time, limit int) ([]LogEntry, error) {
	startULID := ulid.MustNew(ulid.Timestamp(startTime), nil)
	endULID := ulid.MustNew(ulid.Timestamp(endTime), nil)

	input := &dynamodb.QueryInput{
		TableName:              aws.String(s.tableName),
		KeyConditionExpression: aws.String("pk = :pk AND timestamp_seq BETWEEN :start AND :end"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":    &types.AttributeValueMemberS{Value: PartitionKey},
			":start": &types.AttributeValueMemberS{Value: startULID.String() + "#0"},
			":end":   &types.AttributeValueMemberS{Value: endULID.String() + "#999"},
		},
		ScanIndexForward: aws.Bool(false),
		Limit:            aws.Int32(int32(limit * 2)),
	}

	output, err := s.client.Query(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to query logs: %w", err)
	}

	allLogs, err := s.unmarshalAndReassemble(output.Items)
	if err != nil {
		return nil, err
	}

	if len(allLogs) > limit {
		allLogs = allLogs[:limit]
	}

	return allLogs, nil
}
