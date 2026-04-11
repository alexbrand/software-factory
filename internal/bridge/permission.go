package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/alexbrand/software-factory/pkg/events"
)

const defaultPermissionTimeout = 1 * time.Hour

// NATSSubscriber is the subset of *nats.Conn needed for permission reply subscriptions.
type NATSSubscriber interface {
	SubscribeSync(subj string) (*nats.Subscription, error)
}

// PermissionHandler handles runtime permission requests from the SDK.
// In requireApproval mode, it publishes a request event to NATS and blocks
// waiting for an approval response on a reply subject.
type PermissionHandler struct {
	publisher *events.Publisher
	natsConn  NATSSubscriber
	namespace string
	logger    *slog.Logger
}

// NewPermissionHandler creates a new PermissionHandler.
func NewPermissionHandler(publisher *events.Publisher, natsConn NATSSubscriber, namespace string, logger *slog.Logger) *PermissionHandler {
	return &PermissionHandler{
		publisher: publisher,
		natsConn:  natsConn,
		namespace: namespace,
		logger:    logger,
	}
}

// permissionParams holds the parsed fields from a permission request.
type permissionParams struct {
	ToolName string `json:"toolName"`
	Title    string `json:"title"`
}

// HandleRequireApproval publishes a permission request to NATS, blocks waiting
// for an external approval on the reply subject, and returns the decision.
func (h *PermissionHandler) HandleRequireApproval(ctx context.Context, sessionID string, params permissionParams) {
	permID := uuid.New().String()

	// 1. Publish EventPermissionRequested to NATS.
	reqData := events.PermissionRequestData{
		PermissionID: permID,
		ToolName:     params.ToolName,
		Title:        params.Title,
	}
	h.publishPermissionEvent(sessionID, events.EventPermissionRequested, reqData)

	// 2. Subscribe to reply subject and block.
	replySubject := fmt.Sprintf("permissions.%s", permID)
	sub, err := h.natsConn.SubscribeSync(replySubject)
	if err != nil {
		h.logger.Error("failed to subscribe to permission reply", "subject", replySubject, "error", err)
		return
	}
	defer func() { _ = sub.Unsubscribe() }()

	msg, err := sub.NextMsg(defaultPermissionTimeout)
	if err != nil {
		h.logger.Warn("permission request timed out or cancelled", "permissionId", permID, "error", err)
		// Publish a timeout response.
		h.publishPermissionEvent(sessionID, events.EventPermissionResponded, events.PermissionResponseData{
			PermissionID: permID,
			Decision:     "deny",
			RespondedBy:  "bridge:timeout",
		})
		return
	}

	// 3. Parse the decision.
	var decision events.PermissionResponseData
	if err := json.Unmarshal(msg.Data, &decision); err != nil {
		h.logger.Error("failed to parse permission decision", "error", err)
		return
	}
	decision.PermissionID = permID

	// 4. Publish EventPermissionResponded.
	h.publishPermissionEvent(sessionID, events.EventPermissionResponded, decision)
}

// HandleAutoApprove immediately approves a permission request and publishes audit events.
func (h *PermissionHandler) HandleAutoApprove(ctx context.Context, sessionID string, params permissionParams) {
	permID := uuid.New().String()

	h.publishPermissionEvent(sessionID, events.EventPermissionRequested, events.PermissionRequestData{
		PermissionID: permID,
		ToolName:     params.ToolName,
		Title:        params.Title,
	})

	h.publishPermissionEvent(sessionID, events.EventPermissionResponded, events.PermissionResponseData{
		PermissionID: permID,
		Decision:     "allow",
		Remember:     "session",
		RespondedBy:  "bridge:autoApprove",
	})
}

func (h *PermissionHandler) publishPermissionEvent(sessionID string, eventType events.EventType, data any) {
	dataBytes, err := json.Marshal(data)
	if err != nil {
		h.logger.Error("failed to marshal permission event data", "error", err)
		return
	}

	event := events.Event{
		ID:        uuid.New().String(),
		SessionID: sessionID,
		Timestamp: time.Now().UTC(),
		Type:      eventType,
		Data:      dataBytes,
	}

	if err := h.publisher.Publish(context.Background(), h.namespace, event); err != nil {
		h.logger.Error("failed to publish permission event", "eventType", eventType, "error", err)
	}
}
