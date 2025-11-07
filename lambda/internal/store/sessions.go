package store

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
)

const (
	SessionTTLDays = 14
)

type Session struct {
	SessionID string    `dynamodbav:"session_id"`
	CreatedAt time.Time `dynamodbav:"created_at"`
	ExpireAt  int64     `dynamodbav:"expire_at"`
	UserAgent string    `dynamodbav:"user_agent,omitempty"`
}

type SessionStore struct {
	client    *dynamodb.Client
	tableName string
}

func NewSessionStore(client *dynamodb.Client, tableName string) *SessionStore {
	return &SessionStore{
		client:    client,
		tableName: tableName,
	}
}

// CreateSession creates a new session with a 2-week TTL
func (s *SessionStore) CreateSession(ctx context.Context, userAgent string) (*Session, error) {
	now := time.Now()
	sessionID := uuid.New().String()

	session := &Session{
		SessionID: sessionID,
		CreatedAt: now,
		ExpireAt:  now.Add(SessionTTLDays * 24 * time.Hour).Unix(),
		UserAgent: userAgent,
	}

	av, err := attributevalue.MarshalMap(session)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal session: %w", err)
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.tableName),
		Item:      av,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to store session: %w", err)
	}

	return session, nil
}

// ValidateSession checks if a session exists and is not expired
func (s *SessionStore) ValidateSession(ctx context.Context, sessionID string) (bool, error) {
	result, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"session_id": &types.AttributeValueMemberS{Value: sessionID},
		},
	})
	if err != nil {
		return false, fmt.Errorf("failed to get session: %w", err)
	}

	if result.Item == nil {
		return false, nil
	}

	var session Session
	err = attributevalue.UnmarshalMap(result.Item, &session)
	if err != nil {
		return false, fmt.Errorf("failed to unmarshal session: %w", err)
	}

	// Check if expired (although DynamoDB TTL should handle this)
	if session.ExpireAt < time.Now().Unix() {
		return false, nil
	}

	return true, nil
}

// DeleteSession removes a session (for logout)
func (s *SessionStore) DeleteSession(ctx context.Context, sessionID string) error {
	_, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"session_id": &types.AttributeValueMemberS{Value: sessionID},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}

	return nil
}