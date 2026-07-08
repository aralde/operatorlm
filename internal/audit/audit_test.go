package audit

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRotateBackup(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "audit_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	logPath := filepath.Join(tmpDir, "audit.log")

	// Case 1: File does not exist
	if err := RotateBackup(logPath); err != nil {
		t.Errorf("expected no error for non-existent file, got: %v", err)
	}

	// Case 2: File exists but is empty
	err = os.WriteFile(logPath, []byte(""), 0o600)
	if err != nil {
		t.Fatalf("failed to write empty file: %v", err)
	}
	if err := RotateBackup(logPath); err != nil {
		t.Errorf("expected no error for empty file, got: %v", err)
	}
	// Ensure it wasn't renamed/deleted
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("expected empty file to still exist, got err: %v", err)
	}

	// Case 3: File exists and has content
	content := []byte("audit line 1\n")
	err = os.WriteFile(logPath, content, 0o600)
	if err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	// Set modification time to yesterday to have a deterministic date
	yesterday := time.Now().AddDate(0, 0, -1)
	if err := os.Chtimes(logPath, yesterday, yesterday); err != nil {
		t.Fatalf("failed to change times: %v", err)
	}

	dateStr := yesterday.Format("2006-01-02")
	expectedBackupPath := filepath.Join(tmpDir, "audit-"+dateStr+".log")

	if err := RotateBackup(logPath); err != nil {
		t.Fatalf("failed to rotate: %v", err)
	}

	// Original file should be gone (so we start a fresh one)
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Errorf("expected original log file to be removed, got err: %v", err)
	}

	// Backup file should exist and have the correct content
	backupContent, err := os.ReadFile(expectedBackupPath)
	if err != nil {
		t.Fatalf("failed to read backup file: %v", err)
	}
	if string(backupContent) != string(content) {
		t.Errorf("expected backup content %q, got %q", content, backupContent)
	}

	// Case 4: Rotate again when the backup file already exists (append case)
	newContent := []byte("audit line 2\n")
	err = os.WriteFile(logPath, newContent, 0o600)
	if err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	if err := os.Chtimes(logPath, yesterday, yesterday); err != nil {
		t.Fatalf("failed to change times: %v", err)
	}

	if err := RotateBackup(logPath); err != nil {
		t.Fatalf("failed to rotate: %v", err)
	}

	// Backup file should now contain both lines appended
	backupContent, err = os.ReadFile(expectedBackupPath)
	if err != nil {
		t.Fatalf("failed to read backup file: %v", err)
	}
	expectedContent := string(content) + string(newContent)
	if string(backupContent) != expectedContent {
		t.Errorf("expected appended backup content %q, got %q", expectedContent, backupContent)
	}
}
