package platform

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/c1ayooo/xalgorix/v4/internal/imhub"
)

// Inbound IM gateway persistence: chat bindings, message dedupe and adapter
// offsets. Protocol code lives in internal/imhub; this file only stores state.

// GetBinding implements imhub.Store.
func (s *Store) GetBinding(ctx context.Context, platform, chatID string) (imhub.Target, bool, error) {
	if s == nil || s.pool == nil {
		return imhub.Target{}, false, errors.New("platform/postgres: store unavailable")
	}
	var tgt imhub.Target
	err := s.pool.QueryRow(ctx, `
		SELECT task_id, run_id, instance_id, label FROM im_sessions
		WHERE platform=$1 AND chat_id=$2`, platform, chatID).Scan(&tgt.TaskID, &tgt.RunID, &tgt.InstanceID, &tgt.Label)
	if errors.Is(err, pgx.ErrNoRows) {
		return imhub.Target{}, false, nil
	}
	if err != nil {
		return imhub.Target{}, false, err
	}
	return tgt, true, nil
}

// PutBinding implements imhub.Store.
func (s *Store) PutBinding(ctx context.Context, platform, chatID, userID string, tgt imhub.Target) error {
	if s == nil || s.pool == nil {
		return errors.New("platform/postgres: store unavailable")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO im_sessions (platform, chat_id, user_id, task_id, run_id, instance_id, label, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,now())
		ON CONFLICT (platform, chat_id) DO UPDATE SET
			user_id = EXCLUDED.user_id,
			task_id = EXCLUDED.task_id,
			run_id = EXCLUDED.run_id,
			instance_id = EXCLUDED.instance_id,
			label = EXCLUDED.label,
			updated_at = now()`,
		platform, chatID, userID, tgt.TaskID, tgt.RunID, tgt.InstanceID, tgt.Label)
	return err
}

// DeleteBinding implements imhub.Store.
func (s *Store) DeleteBinding(ctx context.Context, platform, chatID string) error {
	if s == nil || s.pool == nil {
		return errors.New("platform/postgres: store unavailable")
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM im_sessions WHERE platform=$1 AND chat_id=$2`, platform, chatID)
	return err
}

// MarkMessage implements imhub.Store. The first insert wins; a duplicate
// platform delivery reports fresh=false.
func (s *Store) MarkMessage(ctx context.Context, platform, messageID string) (bool, error) {
	if s == nil || s.pool == nil {
		return false, errors.New("platform/postgres: store unavailable")
	}
	if messageID == "" {
		return true, nil
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO im_messages (platform, message_id) VALUES ($1,$2)
		ON CONFLICT DO NOTHING`, platform, messageID)
	if err != nil {
		return false, fmt.Errorf("platform/postgres: record im message: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// GetOffset implements imhub.Store.
func (s *Store) GetOffset(ctx context.Context, platform, key string) (string, bool, error) {
	if s == nil || s.pool == nil {
		return "", false, errors.New("platform/postgres: store unavailable")
	}
	var value string
	err := s.pool.QueryRow(ctx, `SELECT value FROM im_offsets WHERE platform=$1 AND key=$2`, platform, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// SetOffset implements imhub.Store.
func (s *Store) SetOffset(ctx context.Context, platform, key, value string) error {
	if s == nil || s.pool == nil {
		return errors.New("platform/postgres: store unavailable")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO im_offsets (platform, key, value, updated_at) VALUES ($1,$2,$3,now())
		ON CONFLICT (platform, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		platform, key, value)
	return err
}
