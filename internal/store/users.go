package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// userIDBytes random bytes give a 32-hex-char user id, unguessable and stable across e-mail changes.
	userIDBytes = 16
	// touchInterval bounds last_seen_at writes to one per user per minute; the
	// inactivity purge counts in days, so finer precision buys nothing.
	touchInterval = time.Minute
	// maxEmailMatches is how many rows UserBySubOrEmail fetches by e-mail: two
	// are enough to tell a unique match from an ambiguous one.
	maxEmailMatches = 2
)

// userColumns is the column list scanned by scanUser, in order.
const userColumns = `id, google_sub, email, created_at, last_seen_at`

// User is a person who connected their Google account.
type User struct {
	ID, GoogleSub, Email  string
	CreatedAt, LastSeenAt time.Time
}

// GoogleToken holds a user's Google OAuth tokens in clear, in memory only.
type GoogleToken struct {
	RefreshToken, AccessToken string
	// Expiry is the access token expiry; zero when unknown.
	Expiry time.Time
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanUser(row rowScanner) (User, error) {
	var u User
	var created, lastSeen int64
	if err := row.Scan(&u.ID, &u.GoogleSub, &u.Email, &created, &lastSeen); err != nil {
		return User{}, err
	}
	u.CreatedAt = time.Unix(created, 0)
	u.LastSeenAt = time.Unix(lastSeen, 0)
	return u, nil
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func newUserID() (string, error) {
	b := make([]byte, userIDBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("store: read user id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// UpsertUser creates the user for a Google sub, or updates its e-mail if it
// exists. Both cases mark the user as seen now.
func (s *Store) UpsertUser(ctx context.Context, sub, email string) (User, error) {
	if sub == "" {
		return User{}, errors.New("store: empty google sub")
	}
	id, err := newUserID()
	if err != nil {
		return User{}, err
	}
	now := s.now().Unix()
	u, err := scanUser(s.db.QueryRowContext(ctx, `
		INSERT INTO users (id, google_sub, email, created_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (google_sub) DO UPDATE SET email = excluded.email, last_seen_at = excluded.last_seen_at
		RETURNING `+userColumns,
		id, sub, normalizeEmail(email), now, now))
	if err != nil {
		return User{}, fmt.Errorf("store: upsert user: %w", err)
	}
	return u, nil
}

// UserBySubOrEmail finds a user by exact Google sub, else by case-insensitive
// e-mail. A sub match wins; an e-mail held by several users is ErrAmbiguous.
func (s *Store) UserBySubOrEmail(ctx context.Context, q string) (User, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return User{}, ErrNotFound
	}
	u, err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE google_sub = ?`, q))
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return User{}, fmt.Errorf("store: user by sub: %w", err)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE email = ? LIMIT ?`, normalizeEmail(q), maxEmailMatches)
	if err != nil {
		return User{}, fmt.Errorf("store: user by email: %w", err)
	}
	users, err := collectUsers(rows)
	if err != nil {
		return User{}, fmt.Errorf("store: user by email: %w", err)
	}
	switch len(users) {
	case 0:
		return User{}, ErrNotFound
	case 1:
		return users[0], nil
	default:
		return User{}, ErrAmbiguous
	}
}

func collectUsers(rows *sql.Rows) (users []User, err error) {
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// TouchUser records activity. It writes at most once per touchInterval per
// user; the throttle is in memory, so a restart allows one early write.
func (s *Store) TouchUser(ctx context.Context, id string) error {
	now := s.now()
	s.touchMu.Lock()
	last, ok := s.touched[id]
	if ok && now.Sub(last) < touchInterval {
		s.touchMu.Unlock()
		return nil
	}
	s.touched[id] = now
	s.touchMu.Unlock()

	if _, err := s.db.ExecContext(ctx,
		`UPDATE users SET last_seen_at = ? WHERE id = ?`, now.Unix(), id); err != nil {
		s.forgetTouch(id)
		return fmt.Errorf("store: touch user: %w", err)
	}
	return nil
}

func (s *Store) forgetTouch(id string) {
	s.touchMu.Lock()
	delete(s.touched, id)
	s.touchMu.Unlock()
}

// SaveGoogleToken stores both tokens encrypted under the active key, bound to
// userID through the AEAD additional data, replacing any previous row.
func (s *Store) SaveGoogleToken(ctx context.Context, userID string, t GoogleToken) error {
	aad := []byte(userID)
	refreshCT, err := s.kr.Encrypt([]byte(t.RefreshToken), aad)
	if err != nil {
		return fmt.Errorf("store: encrypt refresh token: %w", err)
	}
	accessCT, err := s.kr.Encrypt([]byte(t.AccessToken), aad)
	if err != nil {
		return fmt.Errorf("store: encrypt access token: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO google_tokens (user_id, refresh_ct, access_ct, expiry, key_id)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET
			refresh_ct = excluded.refresh_ct, access_ct = excluded.access_ct,
			expiry = excluded.expiry, key_id = excluded.key_id`,
		userID, refreshCT, accessCT, unixOrZero(t.Expiry), s.kr.ActiveID()); err != nil {
		return fmt.Errorf("store: save google token: %w", err)
	}
	return nil
}

// GoogleToken returns the decrypted tokens of a user, or ErrNotFound.
func (s *Store) GoogleToken(ctx context.Context, userID string) (GoogleToken, error) {
	var refreshCT, accessCT []byte
	var expiry int64
	err := s.db.QueryRowContext(ctx,
		`SELECT refresh_ct, access_ct, expiry FROM google_tokens WHERE user_id = ?`, userID).
		Scan(&refreshCT, &accessCT, &expiry)
	if errors.Is(err, sql.ErrNoRows) {
		return GoogleToken{}, ErrNotFound
	}
	if err != nil {
		return GoogleToken{}, fmt.Errorf("store: load google token: %w", err)
	}
	aad := []byte(userID)
	refresh, err := s.kr.Decrypt(refreshCT, aad)
	if err != nil {
		return GoogleToken{}, fmt.Errorf("store: decrypt refresh token: %w", err)
	}
	access, err := s.kr.Decrypt(accessCT, aad)
	if err != nil {
		return GoogleToken{}, fmt.Errorf("store: decrypt access token: %w", err)
	}
	t := GoogleToken{RefreshToken: string(refresh), AccessToken: string(access)}
	if expiry != 0 {
		t.Expiry = time.Unix(expiry, 0)
	}
	return t, nil
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// deleteUserChildren removes every row owned by a user, run before the user
// row itself so the foreign keys never see an orphan.
var deleteUserChildren = []string{
	`DELETE FROM google_tokens WHERE user_id = ?`,
	`DELETE FROM oauth_tokens WHERE user_id = ?`,
}

// DeleteUser removes a user with their Google tokens and every fosite row
// bound to them, in one transaction. It returns ErrNotFound for an unknown id.
func (s *Store) DeleteUser(ctx context.Context, userID string) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: delete user: begin: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	for _, query := range deleteUserChildren {
		if _, err := tx.ExecContext(ctx, query, userID); err != nil {
			return fmt.Errorf("store: delete user rows: %w", err)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID)
	if err != nil {
		return fmt.Errorf("store: delete user: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete user: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: delete user: commit: %w", err)
	}
	s.forgetTouch(userID)
	return nil
}

// InactiveUsers lists users last seen strictly before the cutoff.
func (s *Store) InactiveUsers(ctx context.Context, before time.Time) ([]User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE last_seen_at < ? ORDER BY last_seen_at`, before.Unix())
	if err != nil {
		return nil, fmt.Errorf("store: inactive users: %w", err)
	}
	users, err := collectUsers(rows)
	if err != nil {
		return nil, fmt.Errorf("store: inactive users: %w", err)
	}
	return users, nil
}

type sealedToken struct {
	userID              string
	refreshCT, accessCT []byte
}

// Rekey re-encrypts under the active key every Google token row holding a
// ciphertext sealed by another key, and returns how many rows it rewrote.
// It runs in one transaction: a row sealed by a key missing from the keyring
// aborts the whole run.
func (s *Store) Rekey(ctx context.Context) (n int, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: rekey: begin: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	rows, err := tx.QueryContext(ctx, `SELECT user_id, refresh_ct, access_ct FROM google_tokens`)
	if err != nil {
		return 0, fmt.Errorf("store: rekey: list: %w", err)
	}
	// Rows are read fully before writing: the transaction holds the only connection.
	stale, err := s.collectStale(rows)
	if err != nil {
		return 0, err
	}
	active := s.kr.ActiveID()
	for _, row := range stale {
		aad := []byte(row.userID)
		refreshCT, err := s.reseal(row.refreshCT, aad)
		if err != nil {
			return 0, fmt.Errorf("store: rekey user %s: refresh token: %w", row.userID, err)
		}
		accessCT, err := s.reseal(row.accessCT, aad)
		if err != nil {
			return 0, fmt.Errorf("store: rekey user %s: access token: %w", row.userID, err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE google_tokens SET refresh_ct = ?, access_ct = ?, key_id = ? WHERE user_id = ?`,
			refreshCT, accessCT, active, row.userID); err != nil {
			return 0, fmt.Errorf("store: rekey user %s: %w", row.userID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: rekey: commit: %w", err)
	}
	return len(stale), nil
}

func (s *Store) collectStale(rows *sql.Rows) (stale []sealedToken, err error) {
	defer func() { err = errors.Join(err, rows.Close()) }()
	active := s.kr.ActiveID()
	for rows.Next() {
		var t sealedToken
		if err := rows.Scan(&t.userID, &t.refreshCT, &t.accessCT); err != nil {
			return nil, fmt.Errorf("store: rekey: scan: %w", err)
		}
		for _, ct := range [][]byte{t.refreshCT, t.accessCT} {
			id, err := s.kr.KeyID(ct)
			if err != nil {
				return nil, fmt.Errorf("store: rekey user %s: %w", t.userID, err)
			}
			if id != active {
				stale = append(stale, t)
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: rekey: list: %w", err)
	}
	return stale, nil
}

// reseal decrypts ct with whichever key sealed it and encrypts it again under the active key.
func (s *Store) reseal(ct, aad []byte) ([]byte, error) {
	plaintext, err := s.kr.Decrypt(ct, aad)
	if err != nil {
		return nil, err
	}
	return s.kr.Encrypt(plaintext, aad)
}
