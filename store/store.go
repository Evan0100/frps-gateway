package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrTokenInvalid  = errors.New("authorization token is invalid, expired, or already used")
	ErrActiveIPLimit = errors.New("active IP limit reached")
)

type Store struct{ db *sql.DB }

type OutboxMessage struct {
	ID, ChatID, Text string
	Attempts         int
}

type InboxEvent struct {
	ID       string
	Payload  []byte
	Attempts int
}

type Grant struct {
	OpenID, OperatorName, IP, Source string
	CreatedAt, ExpireAt              time.Time
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS messages(message_id TEXT PRIMARY KEY, reply TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS grants(id INTEGER PRIMARY KEY, open_id TEXT NOT NULL, operator_name TEXT NOT NULL DEFAULT '', ip TEXT NOT NULL, source TEXT NOT NULL, created_at INTEGER NOT NULL, expire_at INTEGER NOT NULL, revoked_at INTEGER);
CREATE INDEX IF NOT EXISTS grants_active_ip ON grants(ip, expire_at, revoked_at);
CREATE INDEX IF NOT EXISTS grants_user ON grants(open_id, expire_at, revoked_at);
CREATE TABLE IF NOT EXISTS auth_tokens(token_hash BLOB PRIMARY KEY, open_id TEXT NOT NULL, operator_name TEXT NOT NULL DEFAULT '', message_id TEXT NOT NULL UNIQUE, ttl_seconds INTEGER NOT NULL, expires_at INTEGER NOT NULL, used_at INTEGER);
CREATE TABLE IF NOT EXISTS managed_ips(ip TEXT PRIMARY KEY);
CREATE TABLE IF NOT EXISTS access_records(instance TEXT NOT NULL, seq INTEGER NOT NULL, time INTEGER NOT NULL, ip TEXT NOT NULL, user TEXT NOT NULL DEFAULT '', source TEXT NOT NULL, action TEXT NOT NULL, reason TEXT NOT NULL, PRIMARY KEY(instance, seq));
CREATE INDEX IF NOT EXISTS access_records_time ON access_records(time);
CREATE TABLE IF NOT EXISTS reply_outbox(id TEXT PRIMARY KEY, chat_id TEXT NOT NULL, body TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, next_attempt_at INTEGER NOT NULL, created_at INTEGER NOT NULL, sent_at INTEGER);
CREATE INDEX IF NOT EXISTS reply_outbox_pending ON reply_outbox(sent_at,next_attempt_at);
CREATE TABLE IF NOT EXISTS bot_inbox(id TEXT PRIMARY KEY, payload BLOB NOT NULL, status TEXT NOT NULL DEFAULT 'pending', attempts INTEGER NOT NULL DEFAULT 0, next_attempt_at INTEGER NOT NULL, created_at INTEGER NOT NULL, completed_at INTEGER);
CREATE INDEX IF NOT EXISTS bot_inbox_pending ON bot_inbox(status,next_attempt_at);
INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(1,strftime('%s','now'));
INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(2,strftime('%s','now'));
INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(3,strftime('%s','now'));
UPDATE bot_inbox SET status='pending' WHERE status='processing';`); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) CachedReply(ctx context.Context, messageID string) (string, bool, error) {
	if messageID == "" {
		return "", false, nil
	}
	var reply string
	err := s.db.QueryRowContext(ctx, `SELECT reply FROM messages WHERE message_id=?`, messageID).Scan(&reply)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return reply, err == nil, err
}

func (s *Store) SaveReply(ctx context.Context, messageID, reply string) error {
	if messageID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO messages(message_id,reply,created_at) VALUES(?,?,?) ON CONFLICT(message_id) DO NOTHING`, messageID, reply, time.Now().Unix())
	return err
}

// EnqueueReply durably records a reply before the Feishu event is considered
// complete. messageID is used as the idempotency key.
func (s *Store) EnqueueReply(ctx context.Context, messageID, chatID, body string) error {
	if messageID == "" || chatID == "" {
		return errors.New("reply outbox requires message and chat IDs")
	}
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO reply_outbox(id,chat_id,body,next_attempt_at,created_at) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET chat_id=excluded.chat_id,body=excluded.body WHERE reply_outbox.sent_at IS NULL`, messageID, chatID, body, now, now)
	return err
}

func (s *Store) PendingReplies(ctx context.Context, limit int) ([]OutboxMessage, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,chat_id,body,attempts FROM reply_outbox WHERE sent_at IS NULL AND next_attempt_at<=? ORDER BY created_at LIMIT ?`, time.Now().Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutboxMessage
	for rows.Next() {
		var m OutboxMessage
		if err := rows.Scan(&m.ID, &m.ChatID, &m.Text, &m.Attempts); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) MarkReplySent(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE reply_outbox SET sent_at=? WHERE id=?`, time.Now().Unix(), id)
	return err
}

func (s *Store) RetryReply(ctx context.Context, id string, attempts int) error {
	delay := time.Duration(1<<min(attempts, 8)) * time.Second
	_, err := s.db.ExecContext(ctx, `UPDATE reply_outbox SET attempts=attempts+1,next_attempt_at=? WHERE id=? AND sent_at IS NULL`, time.Now().Add(delay).Unix(), id)
	return err
}

func (s *Store) PendingReplyCount(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM reply_outbox WHERE sent_at IS NULL`).Scan(&n)
	return n, err
}

func (s *Store) EnqueueInbox(ctx context.Context, id string, payload []byte) error {
	if id == "" || len(payload) == 0 {
		return errors.New("inbox requires event ID and payload")
	}
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO bot_inbox(id,payload,next_attempt_at,created_at) VALUES(?,?,?,?) ON CONFLICT(id) DO NOTHING`, id, payload, now, now)
	return err
}

func (s *Store) ClaimInbox(ctx context.Context) (InboxEvent, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return InboxEvent{}, false, err
	}
	defer tx.Rollback()
	var item InboxEvent
	err = tx.QueryRowContext(ctx, `SELECT id,payload,attempts FROM bot_inbox WHERE status='pending' AND next_attempt_at<=? ORDER BY created_at LIMIT 1`, time.Now().Unix()).Scan(&item.ID, &item.Payload, &item.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return InboxEvent{}, false, nil
	}
	if err != nil {
		return InboxEvent{}, false, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE bot_inbox SET status='processing' WHERE id=? AND status='pending'`, item.ID)
	if err != nil {
		return InboxEvent{}, false, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return InboxEvent{}, false, nil
	}
	if err = tx.Commit(); err != nil {
		return InboxEvent{}, false, err
	}
	return item, true, nil
}

func (s *Store) CompleteInbox(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE bot_inbox SET status='completed',completed_at=? WHERE id=?`, time.Now().Unix(), id)
	return err
}

func (s *Store) RetryInbox(ctx context.Context, id string, attempts int) error {
	delay := time.Duration(1<<min(attempts, 8)) * time.Second
	_, err := s.db.ExecContext(ctx, `UPDATE bot_inbox SET status='pending',attempts=attempts+1,next_attempt_at=? WHERE id=?`, time.Now().Add(delay).Unix(), id)
	return err
}

func (s *Store) CreateToken(ctx context.Context, raw []byte, openID, name, messageID string, ttl, validFor time.Duration) error {
	h := sha256.Sum256(raw)
	_, err := s.db.ExecContext(ctx, `INSERT INTO auth_tokens(token_hash,open_id,operator_name,message_id,ttl_seconds,expires_at) VALUES(?,?,?,?,?,?)`, h[:], openID, name, messageID, int64(ttl.Seconds()), time.Now().Add(validFor).Unix())
	return err
}

func (s *Store) ValidateToken(ctx context.Context, raw []byte) error {
	h := sha256.Sum256(raw)
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM auth_tokens WHERE token_hash=? AND used_at IS NULL AND expires_at>=?`, h[:], time.Now().Unix()).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTokenInvalid
	}
	return err
}

func (s *Store) ConsumeToken(ctx context.Context, raw []byte, ip string, maxActiveIPs int) (Grant, error) {
	h := sha256.Sum256(raw)
	now := time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Grant{}, err
	}
	defer tx.Rollback()
	var g Grant
	var ttl int64
	err = tx.QueryRowContext(ctx, `SELECT open_id,operator_name,ttl_seconds FROM auth_tokens WHERE token_hash=? AND used_at IS NULL AND expires_at>=?`, h[:], now.Unix()).Scan(&g.OpenID, &g.OperatorName, &ttl)
	if errors.Is(err, sql.ErrNoRows) {
		return Grant{}, ErrTokenInvalid
	}
	if err != nil {
		return Grant{}, err
	}
	g.IP = ip
	g.Source = "feishu_link"
	g.CreatedAt = now
	g.ExpireAt = now.Add(time.Duration(ttl) * time.Second)
	if err = enforceActiveIPLimit(ctx, tx, g.OpenID, ip, maxActiveIPs, now.Unix()); err != nil {
		return Grant{}, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE auth_tokens SET used_at=? WHERE token_hash=? AND used_at IS NULL`, now.Unix(), h[:])
	if err != nil {
		return Grant{}, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return Grant{}, ErrTokenInvalid
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO grants(open_id,operator_name,ip,source,created_at,expire_at) VALUES(?,?,?,?,?,?)`, g.OpenID, g.OperatorName, g.IP, g.Source, g.CreatedAt.Unix(), g.ExpireAt.Unix()); err != nil {
		return Grant{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO managed_ips(ip) VALUES(?) ON CONFLICT(ip) DO NOTHING`, ip); err != nil {
		return Grant{}, err
	}
	return g, tx.Commit()
}

func (s *Store) AddGrant(ctx context.Context, g Grant) error {
	return s.AddGrantLimited(ctx, g, 0)
}

func (s *Store) AddGrantLimited(ctx context.Context, g Grant, maxActiveIPs int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = enforceActiveIPLimit(ctx, tx, g.OpenID, g.IP, maxActiveIPs, time.Now().Unix()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO grants(open_id,operator_name,ip,source,created_at,expire_at) VALUES(?,?,?,?,?,?)`, g.OpenID, g.OperatorName, g.IP, g.Source, g.CreatedAt.Unix(), g.ExpireAt.Unix()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO managed_ips(ip) VALUES(?) ON CONFLICT(ip) DO NOTHING`, g.IP); err != nil {
		return err
	}
	return tx.Commit()
}

func enforceActiveIPLimit(ctx context.Context, tx *sql.Tx, openID, targetIP string, maxActiveIPs int, now int64) error {
	if maxActiveIPs <= 0 {
		return nil
	}
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT ip) FROM grants WHERE open_id=? AND ip<>? AND revoked_at IS NULL AND expire_at>?`, openID, targetIP, now).Scan(&count)
	if err != nil {
		return err
	}
	if count >= maxActiveIPs {
		return ErrActiveIPLimit
	}
	return nil
}

func (s *Store) RevokeUserIP(ctx context.Context, openID, ip string) (int64, error) {
	r, err := s.db.ExecContext(ctx, `UPDATE grants SET revoked_at=? WHERE open_id=? AND ip=? AND revoked_at IS NULL AND expire_at>?`, time.Now().Unix(), openID, ip, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

func (s *Store) EffectiveExpiry(ctx context.Context, ip string) (time.Time, bool, error) {
	var v sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(expire_at) FROM grants WHERE ip=? AND revoked_at IS NULL AND expire_at>?`, ip, time.Now().Unix()).Scan(&v)
	return time.Unix(v.Int64, 0), v.Valid, err
}

func (s *Store) ListUser(ctx context.Context, openID string) ([]Grant, error) {
	return s.list(ctx, `WHERE open_id=? AND revoked_at IS NULL AND expire_at>?`, openID, time.Now().Unix())
}
func (s *Store) ListActive(ctx context.Context) ([]Grant, error) {
	return s.list(ctx, `WHERE revoked_at IS NULL AND expire_at>?`, time.Now().Unix())
}
func (s *Store) list(ctx context.Context, where string, args ...any) ([]Grant, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT open_id,operator_name,ip,source,created_at,expire_at FROM grants `+where+` ORDER BY expire_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		var g Grant
		var c, e int64
		if err := rows.Scan(&g.OpenID, &g.OperatorName, &g.IP, &g.Source, &c, &e); err != nil {
			return nil, err
		}
		g.CreatedAt = time.Unix(c, 0)
		g.ExpireAt = time.Unix(e, 0)
		out = append(out, g)
	}
	return out, rows.Err()
}

// AccessRecord is one whitelist enforcement decision shipped from frps.
type AccessRecord struct {
	Instance string
	Seq      uint64
	Time     int64
	IP       string
	User     string
	Source   string
	Action   string
	Reason   string
}

// InsertAccessRecords stores records in one transaction. Records already
// present (same instance and seq) are skipped; the return value is the
// number of newly stored rows.
func (s *Store) InsertAccessRecords(ctx context.Context, records []AccessRecord) (int64, error) {
	if len(records) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO access_records(instance,seq,time,ip,user,source,action,reason) VALUES(?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var inserted int64
	for _, rec := range records {
		res, err := stmt.ExecContext(ctx, rec.Instance, rec.Seq, rec.Time, rec.IP, rec.User, rec.Source, rec.Action, rec.Reason)
		if err != nil {
			return inserted, err
		}
		if n, err := res.RowsAffected(); err == nil {
			inserted += n
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

// PruneAccessRecords deletes access records older than before and returns
// the number of removed rows.
func (s *Store) PruneAccessRecords(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM access_records WHERE time<?`, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountAccessRecords returns the total number of stored access records.
func (s *Store) CountAccessRecords(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM access_records`).Scan(&n)
	return n, err
}

func (s *Store) MaxAccessSequence(ctx context.Context, instance string) (uint64, bool, error) {
	var seq sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(seq) FROM access_records WHERE instance=?`, instance).Scan(&seq)
	return uint64(seq.Int64), seq.Valid, err
}

func (s *Store) ListManagedIPs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ip FROM managed_ips ORDER BY ip`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ips []string
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			return nil, err
		}
		ips = append(ips, ip)
	}
	return ips, rows.Err()
}

func (s *Store) ForgetManagedIP(ctx context.Context, ip string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM managed_ips WHERE ip=?`, ip)
	return err
}
