//go:build linux

package executablepin

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

const requiredSeals = unix.F_SEAL_SEAL | unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK

// SealedCopy copies exactly size bytes into an anonymous, kernel-sealed
// executable object. It does not depend on TMPDIR or any executable filesystem
// mount, and the returned content cannot be written, grown, or truncated.
func SealedCopy(source *os.File, size int64, name string) (_ *os.File, returnErr error) {
	if source == nil || size < 0 {
		return nil, errors.New("invalid executable image source")
	}

	flags := unix.MFD_CLOEXEC | unix.MFD_ALLOW_SEALING | unix.MFD_EXEC

	fd, err := unix.MemfdCreate(name, flags)
	if errors.Is(err, unix.EINVAL) {
		// MFD_EXEC is newer than executable memfd support. Older kernels create
		// executable memfds by default; the caller's actual probe/exec remains
		// the fail-closed capability check on noexec policies.
		fd, err = unix.MemfdCreate(name, unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	}

	if err != nil {
		return nil, fmt.Errorf("create sealed executable image: %w", err)
	}

	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("create sealed executable image file")
	}

	keep := false
	defer func() {
		if !keep {
			returnErr = errors.Join(returnErr, file.Close())
		}
	}()

	written, err := io.Copy(file, io.NewSectionReader(source, 0, size))
	if err != nil {
		return nil, fmt.Errorf("copy sealed executable image: %w", err)
	}

	if written != size {
		return nil, io.ErrShortWrite
	}

	if err := file.Chmod(0o500); err != nil {
		return nil, fmt.Errorf("secure sealed executable mode: %w", err)
	}

	if _, err := unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, requiredSeals); err != nil {
		return nil, fmt.Errorf("seal executable image: %w", err)
	}

	if err := Validate(file, size); err != nil {
		return nil, err
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	keep = true

	return file, nil
}

// Validate performs bounded metadata/seal validation suitable for the final
// exec barrier; it never re-reads the executable content.
func Validate(file *os.File, size int64) error {
	if file == nil {
		return errors.New("sealed executable image is closed")
	}

	seals, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
	if err != nil || seals&requiredSeals != requiredSeals {
		return errors.New("executable image seals are incomplete")
	}

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != size || info.Mode().Perm()&0o111 == 0 {
		return errors.New("sealed executable image metadata changed")
	}

	return nil
}
