package libghosttyarchive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

//nolint:wsl_v5 // The assertions follow the archive lifecycle under test.
func TestPackCreatesDeterministicContractArchive(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "braw-source")
	mustWriteFixtureSource(t, source)
	archive := filepath.Join(root, "braw.tar.gz")

	if err := Pack(source, archive); err != nil {
		t.Fatal(err)
	}

	assertGzipHeader(t, archive, "braw.tar")
	first := mustReadFile(t, archive)
	if err := os.Remove(archive); err != nil {
		t.Fatal(err)
	}
	if err := Pack(source, archive); err != nil {
		t.Fatal(err)
	}

	assertGzipHeader(t, archive, "braw.tar")
	second := mustReadFile(t, archive)
	if !bytes.Equal(first, second) {
		t.Fatalf("archive bytes changed between identical packs: first=%x second=%x", sha256.Sum256(first), sha256.Sum256(second))
	}

	members, err := Members(archive)
	if err != nil {
		t.Fatal(err)
	}
	assertContractMembers(t, members)
	for _, member := range members {
		if string(member.Data) != member.Name {
			t.Fatalf("%s data = %q, want fixture name", member.Name, string(member.Data))
		}
	}
}

//nolint:wsl_v5 // The parity assertions are intentionally grouped around one fixture archive.
func TestPackMatchesPythonParityFixture(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "canny-source")
	mustWriteFixtureSource(t, source)
	goArchive := filepath.Join(root, "go", "canny.tar.gz")
	if err := Pack(source, goArchive); err != nil {
		t.Fatal(err)
	}

	goMembers, err := Members(goArchive)
	if err != nil {
		t.Fatal(err)
	}
	assertContractMembers(t, goMembers)

	goTar := mustGunzip(t, goArchive)
	if len(goTar) != pythonParityTarSize {
		t.Fatalf("uncompressed tar size = %d, want Python helper fixture size %d", len(goTar), pythonParityTarSize)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(goTar)); got != pythonParityTarSHA256 {
		t.Fatalf("uncompressed tar SHA-256 = %s, want Python helper fixture %s", got, pythonParityTarSHA256)
	}
}

//nolint:wsl_v5 // Each malformed fixture is written and inspected in one compact block.
func TestInspectRejectsMalformedArchives(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		write   func(t *testing.T, archive string)
		wantErr string
	}{
		"appledouble_contamination": {
			write: func(t *testing.T, archive string) {
				t.Helper()
				if err := writeContaminatedArchive(archive); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "Linux artifact has unexpected or incomplete archive members:",
		},
		"dreich_traversal": {
			write: func(t *testing.T, archive string) {
				t.Helper()
				writeArchive(t, archive, []testArchiveMember{{name: "../dreich", data: "dreich"}})
			},
			wantErr: "Linux artifact has unexpected or incomplete archive members:",
		},
		"pax_metadata": {
			write: func(t *testing.T, archive string) {
				t.Helper()
				members := fixtureArchiveMembers()
				members[0].pax = map[string]string{"SCHILY.xattr.com.apple.FinderInfo": "contaminated"}
				writeArchive(t, archive, members)
			},
			wantErr: "Linux artifact member contains metadata: libghostty-vt.a",
		},
		"symlink_member": {
			write: func(t *testing.T, archive string) {
				t.Helper()
				members := fixtureArchiveMembers()
				members[0].typeflag = tar.TypeSymlink
				members[0].linkname = "bothy"
				members[0].data = ""
				writeArchive(t, archive, members)
			},
			wantErr: "Linux artifact member is not a regular file: libghostty-vt.a",
		},
		"missing_member": {
			write: func(t *testing.T, archive string) {
				t.Helper()
				writeArchive(t, archive, fixtureArchiveMembers()[:len(AllowedMembers)-1])
			},
			wantErr: "Linux artifact has unexpected or incomplete archive members:",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			archive := filepath.Join(t.TempDir(), name+".tar.gz")
			test.write(t, archive)
			err := Inspect(archive)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Inspect err=%v, want %q", err, test.wantErr)
			}
		})
	}
}

//nolint:wsl_v5 // Source fixture mutation and expected failure stay together in each case.
func TestPackRejectsInvalidSourceMembers(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		arrange func(t *testing.T, root string) string
		wantErr string
	}{
		"absent_source": {
			arrange: func(t *testing.T, root string) string {
				t.Helper()
				return filepath.Join(root, "absent")
			},
			wantErr: "artifact source is not a directory:",
		},
		"missing_member": {
			arrange: func(t *testing.T, root string) string {
				t.Helper()
				source := filepath.Join(root, "thrawn-source")
				mustWriteFixtureSource(t, source)
				if err := os.Remove(filepath.Join(source, "manifest.json")); err != nil {
					t.Fatal(err)
				}
				return source
			},
			wantErr: "artifact source member is not a regular file: manifest.json",
		},
		"symlink_member": {
			arrange: func(t *testing.T, root string) string {
				t.Helper()
				source := filepath.Join(root, "strath-source")
				mustWriteFixtureSource(t, source)
				target := filepath.Join(source, "libghostty-vt.a")
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("manifest.json", target); err != nil {
					t.Fatal(err)
				}
				return source
			},
			wantErr: "artifact source member is not a regular file: libghostty-vt.a",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			source := test.arrange(t, root)
			err := Pack(source, filepath.Join(root, "out.tar.gz"))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Pack err=%v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestRegression(t *testing.T) {
	t.Parallel()

	if err := Regression(); err != nil {
		t.Fatal(err)
	}
}

// pythonParityTarSHA256 is the uncompressed tar stream produced by the retired
// Python helper for the Scots fixture source where each allowed member contains
// its own member name as bytes.
const (
	pythonParityTarSHA256 = "cfa9601e00688d7f478bf587ba9f3b48e8587ecb9d5f462a53f4d04b82fb8dbc"
	pythonParityTarSize   = 40960
)

//nolint:wsl_v5 // Contract-field assertions are intentionally grouped per member.
func assertContractMembers(t *testing.T, members []Member) {
	t.Helper()

	if len(members) != len(AllowedMembers) {
		t.Fatalf("member count = %d, want %d", len(members), len(AllowedMembers))
	}
	for index, member := range members {
		if member.Name != AllowedMembers[index] {
			t.Fatalf("member[%d] = %q, want %q", index, member.Name, AllowedMembers[index])
		}
		if !regularTypeflag(member.Typeflag) {
			t.Fatalf("%s typeflag = %q, want regular", member.Name, member.Typeflag)
		}
		if member.Mode != 0o644 {
			t.Fatalf("%s mode = %#o, want 0644", member.Name, member.Mode)
		}
		if member.UID != 0 || member.GID != 0 || member.Uname != "" || member.Gname != "" {
			t.Fatalf("%s ownership = uid:%d gid:%d uname:%q gname:%q, want root numeric with empty names", member.Name, member.UID, member.GID, member.Uname, member.Gname)
		}
		if !member.ModTime.Equal(time.Unix(0, 0)) {
			t.Fatalf("%s mtime = %s, want Unix epoch", member.Name, member.ModTime)
		}
		if len(member.PAXRecords) != 0 {
			t.Fatalf("%s PAX records = %#v, want none", member.Name, member.PAXRecords)
		}
		if member.Size != int64(len(member.Data)) {
			t.Fatalf("%s size = %d, data length = %d", member.Name, member.Size, len(member.Data))
		}
	}
}

func mustWriteFixtureSource(t *testing.T, source string) {
	t.Helper()

	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := materializeFixtureSource(source); err != nil {
		t.Fatal(err)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func mustGunzip(t *testing.T, archive string) []byte {
	t.Helper()

	file, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	compressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = compressed.Close() }()

	data, err := io.ReadAll(compressed)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

//nolint:wsl_v5 // Fixed gzip header assertions are clearer as a compact sequence.
func assertGzipHeader(t *testing.T, archive, wantName string) {
	t.Helper()

	data := mustReadFile(t, archive)
	if len(data) < 10 {
		t.Fatalf("gzip archive is too short: %d bytes", len(data))
	}
	if !bytes.Equal(data[:10], []byte{0x1f, 0x8b, 0x08, 0x08, 0, 0, 0, 0, 0x02, 0xff}) {
		t.Fatalf("gzip fixed header = %x, want gzip deflate with FNAME, mtime=0, best-compression XFL, unknown OS", data[:10])
	}

	file, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	compressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = compressed.Close() }()

	if compressed.Name != wantName {
		t.Fatalf("gzip header name = %q, want %q", compressed.Name, wantName)
	}
	if !compressed.ModTime.IsZero() {
		t.Fatalf("gzip header mtime = %s, want raw zero mtime", compressed.ModTime)
	}
	if compressed.OS != 255 {
		t.Fatalf("gzip header OS = %d, want 255", compressed.OS)
	}
}

type testArchiveMember struct {
	name     string
	data     string
	typeflag byte
	linkname string
	pax      map[string]string
}

func fixtureArchiveMembers() []testArchiveMember {
	members := make([]testArchiveMember, 0, len(AllowedMembers))
	for _, name := range AllowedMembers {
		members = append(members, testArchiveMember{name: name, data: name, typeflag: tar.TypeReg})
	}

	return members
}

//nolint:wsl_v5 // Archive fixture construction keeps header fields and writes together.
func writeArchive(t *testing.T, archive string, members []testArchiveMember) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(archive), 0o700); err != nil {
		t.Fatal(err)
	}

	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	compressed, err := gzip.NewWriterLevel(file, gzip.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	compressed.Name = gzipHeaderName(archive)
	compressed.ModTime = time.Unix(0, 0)
	compressed.OS = 255

	tarball := tar.NewWriter(compressed)

	for _, member := range members {
		typeflag := member.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}

		header := &tar.Header{
			Name:       member.name,
			Size:       int64(len(member.data)),
			Mode:       0o644,
			Typeflag:   typeflag,
			Linkname:   member.linkname,
			ModTime:    time.Unix(0, 0),
			Uid:        0,
			Gid:        0,
			PAXRecords: member.pax,
			Format:     tar.FormatUSTAR,
		}
		if len(member.pax) > 0 {
			header.Format = tar.FormatPAX
		}
		if typeflag == tar.TypeSymlink {
			header.Size = 0
		}

		if err := tarball.WriteHeader(header); err != nil {
			t.Fatal(err)
		}

		if typeflag != tar.TypeSymlink {
			if _, err := tarball.Write([]byte(member.data)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarball.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
}

func Example_jsonList() {
	fmt.Println(jsonList([]string{"braw", "canny"}))
	// Output: ["braw", "canny"]
}
