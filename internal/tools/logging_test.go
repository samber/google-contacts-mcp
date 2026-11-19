package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/samber/google-contacts-mcp/internal/contacts"
	"github.com/samber/google-contacts-mcp/internal/limits"
	"github.com/samber/google-contacts-mcp/internal/obs"
)

const (
	// fakeEmail is personal data that must never reach an info log.
	fakeEmail = "ada.secret@example.com"
	// fakeGoogleStatus is the status the fake People client reports.
	fakeGoogleStatus = 203
)

// syncBuffer is a goroutine-safe log sink: the MCP handler logs from server
// goroutines while the test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// lines decodes every JSON log record whose msg is msg.
func (b *syncBuffer) lines(t *testing.T, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(b.String()), "\n") {
		if raw == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			t.Fatalf("log line %q is not JSON: %v", raw, err)
		}
		if rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

// statusPeople reports a Google status on every Get, as the People client's
// hook does in production.
type statusPeople struct {
	*fakePeople
}

func (s statusPeople) Get(ctx context.Context, rn string) (contacts.Contact, error) {
	obs.RecordGoogleStatus(ctx, fakeGoogleStatus)
	return s.fakePeople.Get(ctx, rn)
}

type statusFactory struct{ people statusPeople }

func (f statusFactory) People(context.Context, string) (contacts.People, error) { return f.people, nil }

// logHarness builds a harness logging JSON at level into the returned buffer.
func logHarness(t *testing.T, level slog.Level, opts ...harnessOption) (*harness, *syncBuffer) {
	t.Helper()
	buf := &syncBuffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level}))
	opts = append([]harnessOption{func(d *Deps) {
		d.Logger = logger
		d.People = statusFactory{people: statusPeople{fakePeople: d.People.(*fakeFactory).people}}
	}}, opts...)
	return newHarness(t, opts...), buf
}

func toolLine(t *testing.T, buf *syncBuffer, tool string) map[string]any {
	t.Helper()
	for _, rec := range buf.lines(t, "tool call") {
		if rec["tool"] == tool {
			return rec
		}
	}
	t.Fatalf("no tool call line for %s in:\n%s", tool, buf.String())
	return nil
}

func TestToolLog_InfoHasNoPayload(t *testing.T) {
	h, buf := logHarness(t, slog.LevelInfo)
	c := h.people.put(contacts.Contact{GivenName: "Ada", Emails: []contacts.Typed{{Value: fakeEmail}}})
	h.ok("get_contact", map[string]any{"resource_name": c.ResourceName}, nil)

	rec := toolLine(t, buf, "get_contact")
	if rec["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", rec["level"])
	}
	for _, key := range []string{"args", "result"} {
		if _, ok := rec[key]; ok {
			t.Errorf("info line has %q", key)
		}
	}
	want := map[string]any{
		"user_id":       testUserID,
		"status":        "ok",
		"google_status": float64(fakeGoogleStatus),
		"result_count":  float64(1),
	}
	for k, v := range want {
		if rec[k] != v {
			t.Errorf("%s = %v, want %v", k, rec[k], v)
		}
	}
	if _, ok := rec["duration_ms"].(float64); !ok {
		t.Errorf("duration_ms = %v, want a number", rec["duration_ms"])
	}
	if strings.Contains(buf.String(), fakeEmail) {
		t.Errorf("info logs contain the contact e-mail:\n%s", buf.String())
	}
}

func TestToolLog_DebugHasPayload(t *testing.T) {
	h, buf := logHarness(t, slog.LevelDebug)
	c := h.people.put(contacts.Contact{GivenName: "Ada", Emails: []contacts.Typed{{Value: fakeEmail}}})
	h.ok("get_contact", map[string]any{"resource_name": c.ResourceName}, nil)

	rec := toolLine(t, buf, "get_contact")
	args, ok := rec["args"].(map[string]any)
	if !ok || args["resource_name"] != c.ResourceName {
		t.Errorf("args = %v, want resource_name %s", rec["args"], c.ResourceName)
	}
	result, err := json.Marshal(rec["result"])
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if !strings.Contains(string(result), fakeEmail) {
		t.Errorf("result = %s, want the contact e-mail", result)
	}
}

func TestToolLog_ResultCounts(t *testing.T) {
	h, buf := logHarness(t, slog.LevelInfo)
	a := h.people.put(contacts.Contact{GivenName: "Ada"})
	h.people.put(contacts.Contact{GivenName: "Adam"})
	h.ok("search_contacts", map[string]any{"query": "ad"}, nil)
	h.ok("delete_contact", map[string]any{"resource_name": a.ResourceName}, nil)

	for tool, want := range map[string]float64{"search_contacts": 2, "delete_contact": 0} {
		rec := toolLine(t, buf, tool)
		if rec["result_count"] != want {
			t.Errorf("%s result_count = %v, want %v", tool, rec["result_count"], want)
		}
		// Neither tool reached the status hook of the fake.
		if rec["google_status"] != float64(0) {
			t.Errorf("%s google_status = %v, want 0", tool, rec["google_status"])
		}
	}
}

func TestToolLog_RateLimitedAndMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := obs.NewMetrics(reg)
	h, buf := logHarness(t, slog.LevelInfo, func(d *Deps) {
		d.Limiter = limits.New(1000, 1)
		d.Metrics = m
	})
	h.ok("create_contact_group", map[string]any{"name": "a"}, nil)
	res := h.call("create_contact_group", map[string]any{"name": "b"})
	if !res.IsError {
		t.Fatal("second write was not rate limited")
	}
	h.people.setErr("Get", contacts.RateLimitedError{RetryAfter: time.Second})
	h.call("get_contact", map[string]any{"resource_name": "people/c1"})

	if rec := toolLine(t, buf, "get_contact"); rec["status"] != "rate_limited" {
		t.Errorf("get_contact status = %v, want rate_limited", rec["status"])
	}
	statuses := map[string]int{}
	for _, rec := range buf.lines(t, "tool call") {
		if rec["tool"] == "create_contact_group" {
			statuses[rec["status"].(string)]++
		}
	}
	if statuses["ok"] != 1 || statuses["rate_limited"] != 1 {
		t.Errorf("create_contact_group statuses = %v, want one ok and one rate_limited", statuses)
	}

	count := func(name string, labels map[string]string) float64 {
		t.Helper()
		families, err := reg.Gather()
		if err != nil {
			t.Fatalf("Gather: %v", err)
		}
		for _, f := range families {
			if f.GetName() != name {
				continue
			}
		next:
			for _, metric := range f.GetMetric() {
				for _, l := range metric.GetLabel() {
					if labels[l.GetName()] != l.GetValue() {
						continue next
					}
				}
				return metric.GetCounter().GetValue()
			}
		}
		return 0
	}
	checks := []struct {
		name   string
		labels map[string]string
		want   float64
	}{
		{"gcmcp_tool_calls_total", map[string]string{"tool": "create_contact_group", "status": "ok"}, 1},
		{"gcmcp_tool_calls_total", map[string]string{"tool": "create_contact_group", "status": "rate_limited"}, 1},
		{"gcmcp_tool_calls_total", map[string]string{"tool": "get_contact", "status": "rate_limited"}, 1},
		{"gcmcp_rate_limited_total", map[string]string{"kind": "write"}, 1},
		{"gcmcp_rate_limited_total", map[string]string{"kind": "google"}, 1},
	}
	for _, c := range checks {
		if got := count(c.name, c.labels); got != c.want {
			t.Errorf("%s%v = %v, want %v", c.name, c.labels, got, c.want)
		}
	}
	if n := testutil.CollectAndCount(reg, "gcmcp_tool_call_duration_seconds"); n == 0 {
		t.Error("no tool latency histogram")
	}
}

func TestMapError_InternalErrorHidesCause(t *testing.T) {
	h, buf := logHarness(t, slog.LevelInfo)
	cause := errors.New("people search: dial tcp 10.0.0.1:443: connection refused")
	h.people.setErr("Search", cause)
	h.fail("search_contacts", map[string]any{"query": "secret-query"}, msgInternal)

	if rec := toolLine(t, buf, "search_contacts"); rec["status"] != "error" {
		t.Errorf("status = %v, want error", rec["status"])
	}
	failed := buf.lines(t, "tool call failed")
	if len(failed) != 1 {
		t.Fatalf("tool call failed lines = %d, want 1:\n%s", len(failed), buf.String())
	}
	if failed[0]["level"] != "ERROR" || failed[0]["error"] != cause.Error() || failed[0]["tool"] != "search_contacts" {
		t.Errorf("failure line = %v, want ERROR with the cause", failed[0])
	}
	if strings.Contains(buf.String(), "secret-query") {
		t.Errorf("logs contain the tool argument:\n%s", buf.String())
	}
}

func TestMapError_KnownErrorsPassThrough(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{contacts.UpstreamStatusError{Op: "search", Status: 503}, "people search: google returned HTTP 503"},
		{contacts.GroupError{Group: "x"}, `group "x" not found`},
		{contacts.GroupError{Group: "x", Matches: 2}, `group "x" is ambiguous (2 matches)`},
		{contacts.ErrConflict, contacts.ErrConflict.Error()},
		{contacts.RateLimitedError{RetryAfter: time.Second}, "rate limited, retry after 1s"},
		{errors.New("boom"), msgInternal},
	}
	for _, c := range cases {
		got := mapError(c.err, msgContactNotFound)
		if got.Error() != c.want {
			t.Errorf("mapError(%v) = %q, want %q", c.err, got.Error(), c.want)
		}
	}
}
