package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tigerowo/infinite-canvas/model"
)

func TestAPIKeyValidation(t *testing.T) {
	if _, err := ListCurrentUserAPIKeys(context.Background()); err == nil {
		t.Fatal("anonymous key listing should fail")
	}
	if _, err := CreateCurrentUserAPIKey(context.Background(), "key", nil); err == nil {
		t.Fatal("anonymous key creation should fail")
	}
	if err := DeleteCurrentUserAPIKey(context.Background(), "key"); err == nil {
		t.Fatal("anonymous key deletion should fail")
	}
	ctx := WithUser(context.Background(), model.AuthUser{ID: "owner", Role: model.UserRoleUser})
	past := time.Now().Add(-time.Second)
	for _, input := range []struct {
		name      string
		expiresAt *time.Time
	}{
		{name: "  "},
		{name: strings.Repeat("密", 101)},
		{name: "expired", expiresAt: &past},
	} {
		if _, err := CreateCurrentUserAPIKey(ctx, input.name, input.expiresAt); err == nil {
			t.Fatalf("invalid input accepted: %q", input.name)
		}
	}
	for _, token := range []string{"ic_", "ic_invalid", "ic_" + strings.Repeat("x", 100)} {
		if _, ok := CurrentAuthUser(token); ok {
			t.Fatal("invalid key accepted")
		}
	}
}
