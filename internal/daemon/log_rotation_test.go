package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRotatingLogFileRotatesOversizedExistingLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")

	if err := os.WriteFile(path, []byte("dreich"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(logBackupPath(path, 1), []byte("braw"), 0o600); err != nil {
		t.Fatal(err)
	}

	logFile, err := openRotatingLogFile(path, 5, 2)
	if err != nil {
		t.Fatal(err)
	}

	if err := logFile.Close(); err != nil {
		t.Fatal(err)
	}

	assertFileContent(t, path, "")
	assertFileContent(t, logBackupPath(path, 1), "dreich")
	assertFileContent(t, logBackupPath(path, 2), "braw")

	if _, err := os.Stat(logBackupPath(path, 3)); !os.IsNotExist(err) {
		t.Fatalf("unexpected third backup: %v", err)
	}
}

func TestRotatingLogFileWriteRotatesAndRetainsBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")

	if err := os.WriteFile(path, []byte("braw\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	logFile, err := openRotatingLogFile(path, 10, 1)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := logFile.Write([]byte("canny\n")); err != nil {
		t.Fatal(err)
	}

	assertFileContent(t, path, "canny\n")
	assertFileContent(t, logBackupPath(path, 1), "braw\n")

	if _, err := logFile.Write([]byte("thrawn\n")); err != nil {
		t.Fatal(err)
	}

	if err := logFile.Close(); err != nil {
		t.Fatal(err)
	}

	assertFileContent(t, path, "thrawn\n")
	assertFileContent(t, logBackupPath(path, 1), "canny\n")

	if data, err := os.ReadFile(logBackupPath(path, 1)); err != nil {
		t.Fatal(err)
	} else if string(data) == "braw\n" {
		t.Fatal("oldest backup was retained past the configured count")
	}
}

func TestRotatingLogFileWithNoBackupsRemovesOldLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")

	if err := os.WriteFile(path, []byte("braw\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	logFile, err := openRotatingLogFile(path, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logFile.Close() }()

	assertFileContent(t, path, "")

	if _, err := os.Stat(logBackupPath(path, 1)); !os.IsNotExist(err) {
		t.Fatalf("backup retained despite maxBackups=0: %v", err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
