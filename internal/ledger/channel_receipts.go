package ledger

import (
	"context"
	"errors"
	"strings"

	"github.com/celanwang/stead/internal/channels/feishu"
	"github.com/celanwang/stead/internal/delegation"
	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// RecordDeliveryAcknowledgment persists the trusted Feishu sender's validated success response.
// Keeping this write separate from settlement preserves the provider message ID across worker restarts.
func (l *Ledger) RecordDeliveryAcknowledgment(ctx context.Context, r delegation.Receipt) error {
	if r.State != delegation.Delivered || !r.Valid() || strings.TrimSpace(r.Detail) != r.Detail || len(r.Detail) > 191 {
		return errors.New("a positive provider receipt with a message ID is required")
	}
	return l.transaction(ctx, func(tx *gorm.DB) error {
		d, action, err := lockAction(tx, r.ActionID)
		if err != nil {
			return err
		}
		chat, ok := strings.CutPrefix(action.Data.Scope.Target, feishu.TargetPrefix)
		if !ok || chat == "" || d.Data.Briefing != nil || action.Data.Scope.Guard != nil {
			return errors.New("provider acknowledgment requires a Feishu scheduling action")
		}
		var previous string
		result := tx.Raw("SELECT message_id FROM channel_delivery_acknowledgments WHERE action_id = ?", r.ActionID).Scan(&previous)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 0 {
			if previous != r.Detail {
				return errors.New("action already has a different provider message ID")
			}
			return nil
		}
		if action.State != delegation.Sending && action.State != delegation.Unknown {
			return errors.New("only an in-flight or unresolved action can receive a new provider acknowledgment")
		}
		err = tx.Exec(`INSERT INTO channel_delivery_acknowledgments
			(action_id, provider, message_id, recorded_at) VALUES (?, 'feishu', ?, ?)`, r.ActionID, r.Detail, l.now()).Error
		var duplicate *mysqldriver.MySQLError
		if errors.As(err, &duplicate) && duplicate.Number == 1062 {
			return errors.New("provider message ID already belongs to another action")
		}
		return err
	})
}

// VerifyDeliveryAcknowledgments only settles unknown actions backed by an independent provider acknowledgment.
func (l *Ledger) VerifyDeliveryAcknowledgments(ctx context.Context) error {
	var rows []struct{ ActionID, MessageID string }
	err := l.db.WithContext(ctx).Raw(`SELECT ack.action_id, ack.message_id
		FROM channel_delivery_acknowledgments ack JOIN actions a ON a.id = ack.action_id
		WHERE ack.provider = 'feishu' AND a.state = ? ORDER BY a.seq`, delegation.Unknown).Scan(&rows).Error
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := l.Settle(ctx, delegation.Receipt{ActionID: row.ActionID, State: delegation.Delivered, Detail: row.MessageID}); err != nil {
			return err
		}
	}
	return nil
}
