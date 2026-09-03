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
CREATE TABLE IF NOT EXISTS messages(message_id TEXT PRIMARY KEY, reply TEXT NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS grants(id INTEGER PRIMARY KEY, open_id TEXT NOT NULL, operator_name TEXT NOT NULL DEFAULT '', ip TEXT NOT NULL, source TEXT NOT NULL, created_at INTEGER NOT NULL, expire_at INTEGER NOT NULL, revoked_at INTEGER);
CREATE INDEX IF NOT EXISTS grants_active_ip ON grants(ip, expire_at, revoked_at);
CREATE INDEX IF NOT EXISTS grants_user ON grants(open_id, expire_at, revoked_at);
CREATE TABLE IF NOT EXISTS auth_tokens(token_hash BLOB PRIMARY KEY, open_id TEXT NOT NULL, operator_name TEXT NOT NULL DEFAULT '', message_id TEXT NOT NULL UNIQUE, ttl_seconds INTEGER NOT NULL, expires_at INTEGER NOT NULL, used_at INTEGER);
CREATE TABLE IF NOT EXISTS managed_ips(ip TEXT PRIMARY KEY);`); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

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
