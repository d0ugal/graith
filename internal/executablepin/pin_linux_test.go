//go:build linux

package executablepin

import (
	"os"
	"os/exec"
	"testing"
)

func TestSealedCopyIsImmutableAndExecutesWithoutTempMount(t *testing.T) {
	if os.Getenv("GRAITH_TEST_SEALED_EXEC_CHILD") == "1" {
		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := source.Close(); err != nil {
			t.Errorf("close source executable: %v", err)
		}
	})

	info, err := source.Stat()
	if err != nil {
		t.Fatal(err)
	}

	pinned, err := SealedCopy(source, info.Size(), "graith-canny-test")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := pinned.Close(); err != nil {
			t.Errorf("close pinned executable: %v", err)
		}
	})

	if err := Validate(pinned, info.Size()); err != nil {
		t.Fatal(err)
	}

	if _, err := pinned.WriteAt([]byte("dreich"), 0); err == nil {
		t.Fatal("sealed executable accepted an in-place write")
	}

	if err := pinned.Truncate(info.Size() + 1); err == nil {
		t.Fatal("sealed executable accepted growth")
	}

	cmd := exec.Command("/proc/self/fd/3", "-test.run=^TestSealedCopyIsImmutableAndExecutesWithoutTempMount$")
	cmd.Path = "/proc/self/fd/3"
	cmd.ExtraFiles = []*os.File{pinned}

	cmd.Env = append(os.Environ(), "GRAITH_TEST_SEALED_EXEC_CHILD=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("exec sealed image: %v (%s)", err, output)
	}
}
