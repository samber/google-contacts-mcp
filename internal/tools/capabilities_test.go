package tools

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/samber/google-contacts-mcp/internal/limits"
)

func TestServerAdvertisesToolListChanged(t *testing.T) {
	t.Parallel()

	srv := NewServer(Deps{People: &fakeFactory{people: newFakePeople()}, Store: &fakeToucher{}, Limiter: limits.New(10, 10), Version: "0.0.0"})
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()

	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}

	t.Cleanup(func() {
		if err := ss.Close(); err != nil {
			t.Logf("close: %v", err)
		}
	})

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}

	t.Cleanup(func() {
		if err := cs.Close(); err != nil {
			t.Logf("close: %v", err)
		}
	})

	caps := cs.InitializeResult().Capabilities
	if caps.Tools == nil || !caps.Tools.ListChanged {
		t.Fatalf("tools capability = %+v, want listChanged true", caps.Tools)
	}
}
