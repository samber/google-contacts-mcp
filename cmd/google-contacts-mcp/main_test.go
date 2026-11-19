package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/samber/google-contacts-mcp/internal/crypto"
	"github.com/samber/google-contacts-mcp/internal/store"
)

func noEnv(string) string { return "" }

func TestRun_Version(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, noEnv, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if got := out.String(); got != "dev\n" {
		t.Errorf("stdout = %q, want %q", got, "dev\n")
	}
}

func TestRun_StubsAndUnknown(t *testing.T) {
	cases := map[string][]string{
		"default serve": nil,
		"flags only":    {"--listen=:1"},
		"serve":         {"serve"},
		"rekey":         {"rekey"},
		"disconnect":    {"disconnect", "a@b.c"},
		"unknown":       {"bogus"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := run(args, noEnv, &out, &errOut); code != exitFailure {
				t.Errorf("exit = %d, want %d", code, exitFailure)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want empty", out.String())
			}
			if errOut.Len() == 0 || !strings.HasSuffix(errOut.String(), "\n") {
				t.Errorf("stderr = %q, want a message", errOut.String())
			}
		})
	}
}

// storeEnv returns a getenv with a real generated key and a database in a temp dir.
func storeEnv(t *testing.T) (getenv func(string) string, dbPath, keys string) {
	t.Helper()
	keys, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	dbPath = filepath.Join(t.TempDir(), "db.sqlite")
	vars := map[string]string{
		"GCMCP_ENCRYPTION_KEYS":      keys,
		"GCMCP_DB_PATH":              dbPath,
		"GCMCP_GOOGLE_CLIENT_ID":     "client-id",
		"GCMCP_GOOGLE_CLIENT_SECRET": "client-secret",
	}
	return func(key string) string { return vars[key] }, dbPath, keys
}

func TestRun_Rekey(t *testing.T) {
	env, _, _ := storeEnv(t)
	var out, errOut bytes.Buffer
	if code := run([]string{"rekey"}, env, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if got := out.String(); got != "re-encrypted 0 rows\n" {
		t.Errorf("stdout = %q", got)
	}

	errOut.Reset()
	run([]string{"serve", "stray"}, env, &out, &errOut)
	if !strings.Contains(errOut.String(), "stray") {
		t.Errorf("stderr = %q, want config error naming stray", errOut.String())
	}
}

func TestRun_RekeyBadKeys(t *testing.T) {
	env := func(key string) string {
		return map[string]string{"GCMCP_ENCRYPTION_KEYS": "k1:AAAA", "GCMCP_DB_PATH": filepath.Join(t.TempDir(), "db")}[key]
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"rekey"}, env, &out, &errOut); code != exitFailure {
		t.Fatalf("exit = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(errOut.String(), "k1") || out.Len() != 0 {
		t.Errorf("stdout = %q, stderr = %q; want key error naming k1", out.String(), errOut.String())
	}
}

func TestRun_Disconnect(t *testing.T) {
	ctx := context.Background()
	env, dbPath, keys := storeEnv(t)
	kr, err := crypto.ParseKeyring(keys)
	if err != nil {
		t.Fatalf("ParseKeyring: %v", err)
	}
	st, err := store.Open(ctx, dbPath, kr)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	u, err := st.UpsertUser(ctx, "sub-1", "a@example.com")
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var out, errOut bytes.Buffer
	if code := run([]string{"disconnect", "A@example.com"}, env, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if got, want := out.String(), "disconnected "+u.ID+"\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}

	out.Reset()
	if code := run([]string{"disconnect", "sub-1"}, env, &out, &errOut); code != exitFailure {
		t.Errorf("second disconnect exit = %d, want %d", code, exitFailure)
	}
	if !strings.Contains(errOut.String(), "not found") || out.Len() != 0 {
		t.Errorf("stdout = %q, stderr = %q; want not found", out.String(), errOut.String())
	}
}

func TestRun_Help(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"serve", "-h"}, noEnv, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "-base-url") {
		t.Errorf("stdout = %q, want usage", out.String())
	}
}

func TestRun_Genkey(t *testing.T) {
	// genkey must not need any configuration: it runs before keys exist.
	var out, errOut bytes.Buffer
	if code := run([]string{"genkey"}, noEnv, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want empty", errOut.String())
	}
	line := out.String()
	if strings.Count(line, "\n") != 1 || !strings.HasSuffix(line, "\n") {
		t.Fatalf("stdout = %q, want one line", line)
	}
	if _, err := crypto.ParseKeyring(strings.TrimSuffix(line, "\n")); err != nil {
		t.Errorf("genkey output does not parse: %v", err)
	}

	errOut.Reset()
	if code := run([]string{"genkey", "extra"}, noEnv, &out, &errOut); code != exitFailure {
		t.Errorf("genkey with args: exit = %d, want %d", code, exitFailure)
	}
}

type fakeRevoker struct {
	revoked []string
	err     error
}

func (f *fakeRevoker) Revoke(_ context.Context, token string) error {
	f.revoked = append(f.revoked, token)
	return f.err
}

func TestDisconnectUser(t *testing.T) {
	cases := []struct {
		name        string
		token       bool
		revokeErr   error
		wantRevoked []string
	}{
		{name: "revokes then deletes", token: true, wantRevoked: []string{"refresh-1"}},
		{name: "revoke failure still deletes", token: true, revokeErr: errors.New("HTTP 503"), wantRevoked: []string{"refresh-1"}},
		{name: "no token deletes only", token: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			keys, err := crypto.GenerateKey()
			if err != nil {
				t.Fatalf("GenerateKey: %v", err)
			}
			kr, err := crypto.ParseKeyring(keys)
			if err != nil {
				t.Fatalf("ParseKeyring: %v", err)
			}
			st, err := store.Open(ctx, filepath.Join(t.TempDir(), "db.sqlite"), kr)
			if err != nil {
				t.Fatalf("store.Open: %v", err)
			}
			t.Cleanup(func() {
				if err := st.Close(); err != nil {
					t.Errorf("Close: %v", err)
				}
			})
			u, err := st.UpsertUser(ctx, "sub-1", "a@example.com")
			if err != nil {
				t.Fatalf("UpsertUser: %v", err)
			}
			if tc.token {
				if err := st.SaveGoogleToken(ctx, u.ID, store.GoogleToken{RefreshToken: "refresh-1", AccessToken: "access-1"}); err != nil {
					t.Fatalf("SaveGoogleToken: %v", err)
				}
			}
			rv := &fakeRevoker{err: tc.revokeErr}
			var out bytes.Buffer
			if err := disconnectUser(ctx, st, rv, "sub-1", &out); err != nil {
				t.Fatalf("disconnectUser: %v", err)
			}
			if !reflect.DeepEqual(rv.revoked, tc.wantRevoked) {
				t.Errorf("revoked = %q, want %q", rv.revoked, tc.wantRevoked)
			}
			if _, err := st.UserBySubOrEmail(ctx, "sub-1"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("user still present: %v", err)
			}
			if got, want := out.String(), "disconnected "+u.ID+"\n"; got != want {
				t.Errorf("stdout = %q, want %q", got, want)
			}
		})
	}
}
