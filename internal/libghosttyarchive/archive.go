// Package libghosttyarchive creates and inspects the Linux libghostty artifact
// archive contract used by the native build workflow.
package libghosttyarchive

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// AllowedMembers is the exact Linux libghostty artifact member order.
var AllowedMembers = []string{
	"libghostty-vt.a",
	"pkgconfig/libghostty-vt-static.pc",
	"include/module.modulemap",
	"include/ghostty/vt.h",
	"include/ghostty/vt/allocator.h",
	"include/ghostty/vt/build_info.h",
	"include/ghostty/vt/color.h",
	"include/ghostty/vt/color_scheme.h",
	"include/ghostty/vt/device.h",
	"include/ghostty/vt/focus.h",
	"include/ghostty/vt/formatter.h",
	"include/ghostty/vt/grid_ref.h",
	"include/ghostty/vt/grid_ref_tracked.h",
	"include/ghostty/vt/key.h",
	"include/ghostty/vt/key/encoder.h",
	"include/ghostty/vt/key/event.h",
	"include/ghostty/vt/kitty_graphics.h",
	"include/ghostty/vt/modes.h",
	"include/ghostty/vt/mouse.h",
	"include/ghostty/vt/mouse/encoder.h",
	"include/ghostty/vt/mouse/event.h",
	"include/ghostty/vt/osc.h",
	"include/ghostty/vt/paste.h",
	"include/ghostty/vt/point.h",
	"include/ghostty/vt/render.h",
	"include/ghostty/vt/screen.h",
	"include/ghostty/vt/selection.h",
	"include/ghostty/vt/sgr.h",
	"include/ghostty/vt/size_report.h",
	"include/ghostty/vt/style.h",
	"include/ghostty/vt/sys.h",
	"include/ghostty/vt/terminal.h",
	"include/ghostty/vt/types.h",
	"include/ghostty/vt/unicode.h",
	"include/ghostty/vt/wasm.h",
	"manifest.json",
	"libghostty-native.spdx.json",
	"THIRD_PARTY_NOTICES.libghostty.md",
}

const tarTypeRegA byte = 0

// Member describes the archive table fields that are part of the artifact
// contract.
type Member struct {
	Name       string
	Size       int64
	Mode       int64
	Typeflag   byte
	ModTime    time.Time
	UID        int
	GID        int
	Uname      string
	Gname      string
	PAXRecords map[string]string
	Data       []byte
}

// Inspect validates that archive matches the Linux artifact member contract.
//
//nolint:wsl_v5 // Member-order and per-member validation are kept in contract order.
func Inspect(archive string) error {
	members, err := Members(archive)
	if err != nil {
		return err
	}

	names := make([]string, 0, len(members))
	for _, member := range members {
		names = append(names, member.Name)
	}
	if !sameStrings(names, AllowedMembers) {
		//nolint:staticcheck // Preserve the retired Python helper's caller-visible diagnostic.
		return fmt.Errorf("Linux artifact has unexpected or incomplete archive members: %s", jsonList(names))
	}

	for _, member := range members {
		if unsafeArchivePath(member.Name) {
			//nolint:staticcheck // Preserve the retired Python helper's caller-visible diagnostic.
			return fmt.Errorf("Linux artifact member has unsafe path: %s", member.Name)
		}
		if len(member.PAXRecords) > 0 {
			//nolint:staticcheck // Preserve the retired Python helper's caller-visible diagnostic.
			return fmt.Errorf("Linux artifact member contains metadata: %s", member.Name)
		}
		if !regularTypeflag(member.Typeflag) {
			//nolint:staticcheck // Preserve the retired Python helper's caller-visible diagnostic.
			return fmt.Errorf("Linux artifact member is not a regular file: %s", member.Name)
		}
	}

	return nil
}

// Pack writes archive from source using the exact allowed member order and
// deterministic tar metadata.
//
//nolint:wsl_v5 // The writer lifecycle is kept linear so close/inspection ordering is obvious.
func Pack(source, archive string) error {
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("artifact source is not a directory: %s", source)
	}

	for _, name := range AllowedMembers {
		if err := requireRegularSourceMember(source, name); err != nil {
			return err
		}
	}

	parent := filepath.Dir(archive)
	if err := os.MkdirAll(parent, 0o755); err != nil { //nolint:gosec // Match Python helper parent creation under the caller's umask.
		return err
	}

	file, err := os.Create(archive)
	if err != nil {
		return err
	}
	closeFile := true
	defer func() {
		if closeFile {
			_ = file.Close()
		}
	}()

	compressed, err := gzip.NewWriterLevel(file, gzip.BestCompression)
	if err != nil {
		return err
	}
	compressed.Name = gzipHeaderName(archive)
	compressed.ModTime = time.Unix(0, 0)
	compressed.OS = 255

	if err := writeUSTAR(compressed, source); err != nil {
		_ = compressed.Close()
		return err
	}
	if err := compressed.Close(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	closeFile = false

	return Inspect(archive)
}

// Regression runs the helper's self-test contract.
//
//nolint:wsl_v5 // The self-test mirrors the retired helper's setup/check sequence.
func Regression() error {
	root, err := os.MkdirTemp("", "libghostty-archive-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(root) }()

	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		return err
	}
	if err := materializeFixtureSource(source); err != nil {
		return err
	}

	contaminated := filepath.Join(root, "contaminated.tar.gz")
	if err := writeContaminatedArchive(contaminated); err != nil {
		return err
	}
	if err := Inspect(contaminated); err == nil {
		return errors.New("contaminated archive unexpectedly passed inspection")
	}

	corrected := filepath.Join(root, "corrected.tar.gz")
	if err := Pack(source, corrected); err != nil {
		return err
	}
	if err := Inspect(corrected); err != nil {
		return err
	}

	members, err := Members(corrected)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(members))
	for _, member := range members {
		names = append(names, member.Name)
	}
	if !sameStrings(names, AllowedMembers) {
		return fmt.Errorf("corrected archive member order changed: %s", jsonList(names))
	}

	return nil
}

// Members reads archive members after gzip and tar header processing.
//
//nolint:wsl_v5 // Sequential stream setup and read loop are clearer grouped here.
func Members(archive string) ([]Member, error) {
	file, err := os.Open(archive)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	compressed, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = compressed.Close() }()

	tarball := tar.NewReader(compressed)
	var members []Member
	for {
		header, err := tarball.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}

		data, err := io.ReadAll(tarball)
		if err != nil {
			return nil, err
		}
		members = append(members, Member{
			Name:       header.Name,
			Size:       header.Size,
			Mode:       header.Mode,
			Typeflag:   header.Typeflag,
			ModTime:    header.ModTime,
			UID:        header.Uid,
			GID:        header.Gid,
			Uname:      header.Uname,
			Gname:      header.Gname,
			PAXRecords: cloneMap(header.PAXRecords),
			Data:       data,
		})
	}

	return members, nil
}

//nolint:wsl_v5 // Path derivation and validation stay adjacent for this guard.
func requireRegularSourceMember(source, name string) error {
	path := filepath.Join(source, filepath.FromSlash(name))
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("artifact source member is not a regular file: %s", name)
	}

	return nil
}

//nolint:wsl_v5 // Byte-for-byte tar construction keeps write accounting adjacent.
func writeUSTAR(writer io.Writer, source string) error {
	var written int64
	for _, name := range AllowedMembers {
		data, err := os.ReadFile(filepath.Join(source, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		header, err := ustarHeader(name, int64(len(data)))
		if err != nil {
			return err
		}
		if _, err := writer.Write(header[:]); err != nil {
			return err
		}
		written += int64(len(header))
		if _, err := writer.Write(data); err != nil {
			return err
		}
		written += int64(len(data))
		if padding := tarBlockPadding(int64(len(data))); padding > 0 {
			if _, err := writer.Write(make([]byte, padding)); err != nil {
				return err
			}
			written += padding
		}
	}

	if _, err := writer.Write(make([]byte, 1024)); err != nil {
		return err
	}
	written += 1024
	if padding := recordPadding(written); padding > 0 {
		_, err := writer.Write(make([]byte, padding))
		return err
	}

	return nil
}

//nolint:wsl_v5 // USTAR field writes intentionally remain in header order.
func ustarHeader(name string, size int64) ([512]byte, error) {
	var header [512]byte
	if err := writeUSTARName(header[:], name); err != nil {
		return header, err
	}
	if err := writeOctal(header[100:108], 0o644); err != nil {
		return header, err
	}
	if err := writeOctal(header[108:116], 0); err != nil {
		return header, err
	}
	if err := writeOctal(header[116:124], 0); err != nil {
		return header, err
	}
	if err := writeOctal(header[124:136], size); err != nil {
		return header, err
	}
	if err := writeOctal(header[136:148], 0); err != nil {
		return header, err
	}
	for i := 148; i < 156; i++ {
		header[i] = ' '
	}
	header[156] = tar.TypeReg
	copy(header[257:263], "ustar\x00")
	copy(header[263:265], "00")

	var checksum int64
	for _, value := range header {
		checksum += int64(value)
	}
	checksumField := fmt.Sprintf("%06o\x00 ", checksum)
	copy(header[148:156], checksumField)

	return header, nil
}

//nolint:wsl_v5 // Prefix/suffix calculation is kept together for USTAR limits.
func writeUSTARName(header []byte, name string) error {
	nameBytes := []byte(name)
	if len(nameBytes) <= 100 {
		copy(header[0:100], nameBytes)
		return nil
	}

	for index := len(name) - 1; index >= 0; index-- {
		if name[index] != '/' {
			continue
		}
		prefix := name[:index]
		suffix := name[index+1:]
		if len([]byte(prefix)) <= 155 && len([]byte(suffix)) <= 100 {
			copy(header[0:100], suffix)
			copy(header[345:500], prefix)
			return nil
		}
	}

	return fmt.Errorf("artifact member name exceeds USTAR limits: %s", name)
}

//nolint:wsl_v5 // Fixed-width octal field construction is easier to read as one block.
func writeOctal(field []byte, value int64) error {
	if value < 0 {
		return fmt.Errorf("negative USTAR numeric field: %d", value)
	}
	digits := fmt.Sprintf("%0*o", len(field)-1, value)
	if len(digits) != len(field)-1 {
		return fmt.Errorf("USTAR numeric field overflows: %d", value)
	}
	copy(field, digits)
	field[len(field)-1] = 0

	return nil
}

func tarBlockPadding(size int64) int64 {
	if remainder := size % 512; remainder != 0 {
		return 512 - remainder
	}

	return 0
}

func recordPadding(size int64) int64 {
	if remainder := size % 10240; remainder != 0 {
		return 10240 - remainder
	}

	return 0
}

//nolint:wsl_v5 // Fixture paths and writes are intentionally adjacent.
func materializeFixtureSource(source string) error {
	for _, name := range AllowedMembers {
		path := filepath.Join(source, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			return err
		}
	}

	return nil
}

//nolint:wsl_v5 // Contaminated fixture construction mirrors the retired helper.
func writeContaminatedArchive(archive string) error {
	file, err := os.Create(archive)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	compressed, err := gzip.NewWriterLevel(file, gzip.BestCompression)
	if err != nil {
		return err
	}
	compressed.Name = gzipHeaderName(archive)
	compressed.ModTime = time.Unix(0, 0)
	compressed.OS = 255

	tarball := tar.NewWriter(compressed)
	names := append([]string{}, AllowedMembers...)
	names = append(names,
		"._libghostty-vt.a",
		"pkgconfig/._libghostty-vt-static.pc",
		"._manifest.json",
		"._libghostty-native.spdx.json",
		"._THIRD_PARTY_NOTICES.libghostty.md",
	)
	for _, name := range names {
		data := []byte(name)
		header := &tar.Header{
			Name:       name,
			Size:       int64(len(data)),
			Typeflag:   tar.TypeReg,
			PAXRecords: map[string]string{"SCHILY.xattr.com.apple.FinderInfo": "contaminated"},
			Format:     tar.FormatPAX,
		}
		if err := tarball.WriteHeader(header); err != nil {
			_ = tarball.Close()
			_ = compressed.Close()
			return err
		}
		if _, err := tarball.Write(data); err != nil {
			_ = tarball.Close()
			_ = compressed.Close()
			return err
		}
	}
	if err := tarball.Close(); err != nil {
		_ = compressed.Close()
		return err
	}

	return compressed.Close()
}

//nolint:wsl_v5 // The absolute and traversal checks are intentionally adjacent.
func unsafeArchivePath(name string) bool {
	if strings.HasPrefix(name, "/") {
		return true
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return true
		}
	}

	return false
}

func gzipHeaderName(archive string) string {
	name := path.Base(filepath.ToSlash(archive))
	return strings.TrimSuffix(name, ".gz")
}

//nolint:wsl_v5 // Simple slice comparison keeps the loop and return together.
func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}

	return true
}

//nolint:wsl_v5 // Per-value JSON encoding keeps diagnostics identical to Python json.dumps.
func jsonList(values []string) string {
	encoded := make([]string, 0, len(values))
	for _, value := range values {
		var builder strings.Builder
		encoder := json.NewEncoder(&builder)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(value); err != nil {
			panic(err)
		}
		encoded = append(encoded, strings.TrimSpace(builder.String()))
	}

	return "[" + strings.Join(encoded, ", ") + "]"
}

//nolint:wsl_v5 // Deterministic clone order is kept explicit for tests.
func cloneMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result[key] = values[key]
	}

	return result
}

func regularTypeflag(typeflag byte) bool {
	return typeflag == tar.TypeReg || typeflag == tarTypeRegA
}
