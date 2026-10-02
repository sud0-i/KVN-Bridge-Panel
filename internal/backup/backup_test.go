package backup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type item struct {
	ID   int
	Name string
}

func openDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestSnapshotIsReadableCopy(t *testing.T) {
	dir := t.TempDir()
	db := openDB(t, filepath.Join(dir, "live.db"))
	db.AutoMigrate(&item{})
	db.Create(&item{ID: 1, Name: "user"})

	path := filepath.Join(dir, "backups", FileName(time.Now()))
	if err := Snapshot(db, path); err != nil {
		t.Fatal(err)
	}
	// Повторная копия в тот же файл не должна падать
	if err := Snapshot(db, path); err != nil {
		t.Fatalf("повторная копия: %v", err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("копия должна быть доступна только владельцу, права %v", st.Mode().Perm())
	}

	var got item
	if err := openDB(t, path).First(&got).Error; err != nil || got.Name != "user" {
		t.Fatalf("в копии нет данных: %+v, %v", got, err)
	}
}

func TestRotateKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		os.WriteFile(filepath.Join(dir, FileName(base.AddDate(0, 0, i))), nil, 0o600)
	}
	os.WriteFile(filepath.Join(dir, "чужой-файл.txt"), nil, 0o600)

	if err := Rotate(dir, 2); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{FileName(base.AddDate(0, 0, 3)), FileName(base.AddDate(0, 0, 4)), "чужой-файл.txt"}
	if len(names) != len(want) {
		t.Fatalf("ожидали %v, получили %v", want, names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("ожидали %v, получили %v", want, names)
		}
	}
}
