package service

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/tigerowo/infinite-canvas/config"
	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/repository"
)

func TestPersonalBridgeOwnerLifecycle(t *testing.T) {
	const marker = "BRIDGE_OWNER_AUTH_TEST"
	if os.Getenv(marker) != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPersonalBridgeOwnerLifecycle$", "-test.timeout=25s")
		cmd.Env = append(os.Environ(), marker+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated bridge lifecycle: %v\n%s", err, output)
		}
		return
	}
	config.Cfg = config.Config{StorageDriver: "sqlite", DatabaseDSN: ":memory:"}
	db, err := repository.DB()
	if err != nil {
		t.Fatal(err)
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	connection.SetMaxOpenConns(1)
	defer connection.Close()
	owner := model.User{ID: "bridge-owner", Username: "bridge-owner", Role: model.UserRoleUser, Status: model.UserStatusActive}
	if _, err := repository.SaveUser(owner); err != nil {
		t.Fatal(err)
	}
	personal, err := RegisterComfyBridge("personal", owner.ID, "Personal bridge")
	if err != nil {
		t.Fatal(err)
	}
	system, err := RegisterComfyBridge("system", "system", "System bridge")
	if err != nil {
		t.Fatal(err)
	}
	check := func(token string, valid bool) {
		t.Helper()
		_, err := AuthenticateComfyBridge(token)
		if (err == nil) != valid {
			t.Fatalf("bridge authentication valid=%v, want %v", err == nil, valid)
		}
	}
	check(personal.Token, true)
	check("invalid", false)
	owner.Status = model.UserStatusBan
	if _, err := repository.SaveUser(owner); err != nil {
		t.Fatal(err)
	}
	check(personal.Token, false)
	check(system.Token, true)
	owner.Status = model.UserStatusActive
	if _, err := repository.SaveUser(owner); err != nil {
		t.Fatal(err)
	}
	check(personal.Token, true)
	if err := db.Model(&model.ComfyBridge{}).Where("id = ?", personal.Bridge.ID).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	check(personal.Token, false)
	if err := db.Model(&model.ComfyBridge{}).Where("id = ?", personal.Bridge.ID).Update("enabled", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.DeleteUser(owner.ID); err != nil {
		t.Fatal(err)
	}
	check(personal.Token, false)
	check(system.Token, true)
}
