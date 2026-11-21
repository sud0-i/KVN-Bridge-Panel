package api

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/backup"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
)

func TestRestoreBackupKeepsLocalBridgeAndLogin(t *testing.T) {
	live, old := newEnv(t), newEnv(t)
	live.s.cfg.BackupDir = t.TempDir()
	const currentIP = "203.0.113.40"
	const oldIP = "203.0.113.41"
	token := live.addNode(currentIP, "bridge")
	old.addNode(oldIP, "bridge")
	for _, row := range []struct {
		env *env
		ip  string
	}{
		{live, currentIP},
		{old, oldIP},
	} {
		if err := row.env.db.Model(&models.Node{}).Where("ip = ?", row.ip).Update("local_bridge", true).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := live.db.Create(&models.Setting{Key: "panel_channel", Value: "beta"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := old.db.Create(&models.Setting{Key: "panel_channel", Value: "stable"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := old.db.Create(&models.User{ID: "restored-user", Name: "Restored", SubToken: "restored-sub"}).Error; err != nil {
		t.Fatal(err)
	}
	const exitIP = "203.0.113.42"
	old.addNode(exitIP, "exit")
	if err := old.db.Model(&models.Node{}).Where("ip = ?", exitIP).Update("pending_action", "reboot").Error; err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "old.sqlite")
	if err := backup.Snapshot(old.db, path); err != nil {
		t.Fatal(err)
	}
	input, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	auth := live.adminToken()
	if rec := live.do("POST", "/api/backup/restore", "invalid", auth); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid backup accepted: %d %s", rec.Code, rec.Body)
	}
	if rec := live.do("POST", "/api/backup/restore", string(input), auth); rec.Code != http.StatusOK {
		t.Fatalf("restore failed: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.ReadDir(live.s.cfg.BackupDir); err != nil {
		t.Fatal(err)
	}
	copies, err := filepath.Glob(filepath.Join(live.s.cfg.BackupDir, "kvn-before-restore-*.sqlite"))
	if err != nil || len(copies) != 1 {
		t.Fatalf("previous backup: %v (%v)", copies, err)
	}
	if got := live.syncAs(token).PanelChannel; got != "beta" {
		t.Fatalf("local token or panel channel was not preserved: %q", got)
	}
	var nodes []models.Node
	if err := live.db.Order("ip").Find(&nodes).Error; err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 || nodes[0].IP != currentIP || nodes[1].IP != exitIP || nodes[1].PendingAction != "" {
		t.Fatalf("unexpected restored nodes: %+v", nodes)
	}
	var found models.User
	if err := live.db.First(&found, "id = ?", "restored-user").Error; err != nil || found.SubToken != "restored-sub" {
		t.Fatalf("restored user: %s, %v", found.SubToken, err)
	}
}
