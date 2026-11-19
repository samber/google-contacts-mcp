package contacts

import (
	"errors"
	"fmt"
	"math"
	"time"
)

var (
	// ErrNotFound is returned when Google answers 404 for a resource name.
	ErrNotFound = errors.New("contact not found")
	// ErrConflict is returned when the etag sent on update is stale.
	ErrConflict = errors.New("contact changed, re-read it")
	// ErrUpstreamAuth is returned when the user's Google grant is revoked or
	// expired; callers answer HTTP 401 instead of a tool error.
	ErrUpstreamAuth = errors.New("google authorization is no longer valid")
	// ErrInvalidArgument is returned when Google rejects a request as
	// malformed (HTTP 400 other than a stale etag), e.g. a bad page token.
	ErrInvalidArgument = errors.New("invalid argument")
)

// RateLimitedError reports a Google 429 or a local rate-limit refusal.
// It uses a value receiver so errors.As works with a RateLimitedError target.
type RateLimitedError struct {
	RetryAfter time.Duration
}

func (e RateLimitedError) Error() string {
	// Round up so the caller never retries before the limit actually resets.
	return fmt.Sprintf("rate limited, retry after %ds", int64(math.Ceil(e.RetryAfter.Seconds())))
}
