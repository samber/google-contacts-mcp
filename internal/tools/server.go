// Package tools serves the MCP endpoint: the contact tools, their per-call
// middleware (user, rate limit, write lock, error mapping) and the bearer
// check in front of the stateless Streamable HTTP handler.
package tools

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/samber/google-contacts-mcp/internal/contacts"
	"github.com/samber/google-contacts-mcp/internal/limits"
	"github.com/samber/google-contacts-mcp/internal/oauth"
)

const (
	// serverName is the MCP implementation name advertised to clients.
	serverName = "google-contacts-mcp"
	// Protocol versions served (spec §5.2). The two legacy ones keep clients
	// that open with initialize working (go-sdk PR #1345).
	protocolVersion20260728 = "2026-07-28"
	protocolVersion20251125 = "2025-11-25"
	protocolVersion20250618 = "2025-06-18"
)

// Tool error messages returned to the assistant (spec §8.2). They never carry
// tool arguments or results, since Task 11 logs tool errors at info.
const (
	msgContactNotFound = "contact not found"
	msgGroupNotFound   = "group not found"
	msgUpstreamAuth    = "Google access was revoked; reconnect this connector"
	msgNoUser          = "internal error: no authenticated user"
	msgInvalidArgument = "invalid argument"
)

// PeopleFactory returns a People API client acting as one user;
// *google.Upstream implements it.
type PeopleFactory interface {
	People(ctx context.Context, userID string) (contacts.People, error)
}

// UserToucher records user activity; *store.Store implements it.
type UserToucher interface {
	TouchUser(ctx context.Context, id string) error
}

// Deps are the collaborators of the MCP endpoint.
type Deps struct {
	People  PeopleFactory
	Store   UserToucher
	Limiter *limits.Limiter
	// Verifier checks bearer tokens; (*oauth.Server).Verifier() in production.
	Verifier auth.TokenVerifier
	// ResourceMetadataURL goes into the WWW-Authenticate header of 401
	// answers; (*oauth.Server).ResourceMetadataURL() in production.
	ResourceMetadataURL string
	// Version is the server version advertised to clients.
	Version string
	// Logger defaults to slog.Default().
	Logger *slog.Logger
}

func (d Deps) logger() *slog.Logger {
	if d.Logger == nil {
		return slog.Default()
	}
	return d.Logger
}

// NewServer builds the MCP server with every contact tool registered.
func NewServer(deps Deps) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: deps.Version}, &mcp.ServerOptions{
		Logger: deps.logger(),
		SupportedProtocolVersions: []string{
			protocolVersion20260728,
			protocolVersion20251125,
			protocolVersion20250618,
		},
	})
	registerTools(srv, deps)
	return srv
}

// NewHandler returns the POST /mcp handler: bearer check, then a stateless
// Streamable HTTP handler sharing one server across requests.
func NewHandler(deps Deps) http.Handler {
	srv := NewServer(deps)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{
		Stateless: true,
		Logger:    deps.logger(),
	})
	return auth.RequireBearerToken(deps.Verifier, &auth.RequireBearerTokenOptions{
		ResourceMetadataURL: deps.ResourceMetadataURL,
	})(h)
}

// toolFunc is a tool body: it runs with the caller's People client once the
// middleware has admitted the call.
type toolFunc[In, Out any] func(ctx context.Context, p contacts.People, in In) (summary string, out Out, err error)

// handle wraps a tool body with the per-call middleware, in order: resolve
// the user, touch it, take a rate-limit token, take the write lock for
// writes, run, then map errors to assistant-facing messages.
func handle[In, Out any](deps Deps, write bool, fn toolFunc[In, Out]) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		var zero Out
		userID, ok := oauth.UserID(ctx)
		if !ok {
			// Unreachable behind RequireBearerToken with our verifier.
			deps.logger().ErrorContext(ctx, "tools: call without authenticated user")
			return nil, zero, errors.New(msgNoUser)
		}
		if err := deps.Store.TouchUser(ctx, userID); err != nil {
			// last_seen only drives retention; a failed update must not
			// block the user's call.
			deps.logger().WarnContext(ctx, "tools: touch user", slog.Any("error", err))
		}
		// Rate limit before the write lock, so a refused write never waits.
		if allowed, retry := deps.Limiter.Allow(userID, write); !allowed {
			return nil, zero, mapError(contacts.RateLimitedError{RetryAfter: retry}, msgContactNotFound)
		}
		if write {
			unlock := deps.Limiter.WriteLock(userID)
			defer unlock()
		}
		p, err := deps.People.People(ctx, userID)
		if err != nil {
			return nil, zero, mapError(err, msgContactNotFound)
		}
		summary, out, err := fn(ctx, p, in)
		if err != nil {
			return nil, zero, mapError(err, msgContactNotFound)
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: summary}}}, out, nil
	}
}

// toolError is an error whose message is final and safe for the assistant.
type toolError struct{ msg string }

func (e *toolError) Error() string { return e.msg }

func newToolError(msg string) error { return &toolError{msg: msg} }

// mapError turns a contacts or People error into the message the assistant
// sees. notFound is the wording for a bare contacts.ErrNotFound.
func mapError(err error, notFound string) error {
	var te *toolError
	var rl contacts.RateLimitedError
	switch {
	case errors.As(err, &te):
		return te
	case errors.As(err, &rl):
		return newToolError(rl.Error())
	case errors.Is(err, contacts.ErrUpstreamAuth):
		return newToolError(msgUpstreamAuth)
	case errors.Is(err, contacts.ErrConflict):
		return newToolError(contacts.ErrConflict.Error())
	case errors.Is(err, contacts.ErrNotFound):
		return newToolError(notFound)
	case errors.Is(err, contacts.ErrInvalidArgument):
		return newToolError(msgInvalidArgument)
	}
	// Remaining errors are built by internal/google without request data
	// (op name and HTTP status, or a transport error), or by
	// contacts.ResolveGroup, which names only the group the caller gave.
	return err
}
