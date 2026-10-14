package libghosttydeps

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

const testUucodeSHA = "9d55524551411b493cca41ca06363625d90aff1e"

func TestRequireUucodeURL(t *testing.T) {
	good := "https://github.com/jacobsandlund/uucode/archive/" + testUucodeSHA + ".tar.gz"

	tests := []struct {
		name string
		url  string
		ok   bool
	}{
		{"github exact form", good, true},
		{"ghostty mirror", "https://deps.files.ghostty.org/uucode-2826a37a4562284fdacd8fa029d49509cc9bffcd.tar.gz", true},
		{"other github repo", "https://github.com/evil/uucode/archive/" + testUucodeSHA + ".tar.gz", false},
		{"other github owner repo", "https://github.com/jacobsandlund/other/archive/" + testUucodeSHA + ".tar.gz", false},
		{"other host", "https://gitlab.com/jacobsandlund/uucode/archive/" + testUucodeSHA + ".tar.gz", false},
		{"subdomain host", "https://www.github.com/jacobsandlund/uucode/archive/" + testUucodeSHA + ".tar.gz", false},
		{"http scheme", "http://github.com/jacobsandlund/uucode/archive/" + testUucodeSHA + ".tar.gz", false},
		{"refs path", "https://github.com/jacobsandlund/uucode/archive/refs/heads/main.tar.gz", false},
		{"release path", "https://github.com/jacobsandlund/uucode/releases/download/v1/" + testUucodeSHA + ".tar.gz", false},
		{"zip extension", "https://github.com/jacobsandlund/uucode/archive/" + testUucodeSHA + ".zip", false},
		{"short sha", "https://github.com/jacobsandlund/uucode/archive/9d55524.tar.gz", false},
		{"long sha", "https://github.com/jacobsandlund/uucode/archive/" + testUucodeSHA + "0.tar.gz", false},
		{"uppercase sha", "https://github.com/jacobsandlund/uucode/archive/" + strings.ToUpper(testUucodeSHA) + ".tar.gz", false},
		{"non-hex sha", "https://github.com/jacobsandlund/uucode/archive/" + strings.Repeat("g", 40) + ".tar.gz", false},
		{"query string", good + "?token=x", false},
		{"fragment", good + "#x", false},
		{"userinfo", "https://user@github.com/jacobsandlund/uucode/archive/" + testUucodeSHA + ".tar.gz", false},
		{"userinfo host confusion", "https://github.com@evil.example/jacobsandlund/uucode/archive/" + testUucodeSHA + ".tar.gz", false},
		{"port", "https://github.com:8443/jacobsandlund/uucode/archive/" + testUucodeSHA + ".tar.gz", false},
		{"traversal", "https://github.com/jacobsandlund/uucode/archive/../../evil/repo/archive/" + testUucodeSHA + ".tar.gz", false},
		{"encoded traversal", "https://github.com/jacobsandlund/uucode/archive/%2e%2e/" + testUucodeSHA + ".tar.gz", false},
		{"double slash", "https://github.com//jacobsandlund/uucode/archive/" + testUucodeSHA + ".tar.gz", false},
		{"trailing newline", good + "\n", false},
		{"empty", "", false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := requireUucodeURL(test.url)
			if test.ok && err != nil {
				t.Fatalf("requireUucodeURL(%q) = %v, want nil", test.url, err)
			}

			if !test.ok && err == nil {
				t.Fatalf("requireUucodeURL(%q) accepted, want rejection", test.url)
			}
		})
	}
}

func TestRequireDependencyURLRejectsGitHubUucodeForOtherDependencies(t *testing.T) {
	githubURL := "https://github.com/jacobsandlund/uucode/archive/" + testUucodeSHA + ".tar.gz"

	for _, prefix := range []string{"highway-", "uucode-", ""} {
		if err := requireDependencyURL(githubURL, prefix); err == nil {
			t.Fatalf("requireDependencyURL accepted the GitHub uucode URL with prefix %q", prefix)
		}
	}

	if err := requireDependencyURL("https://deps.files.ghostty.org/highway-66486a10623fa0d72fe91260f96c892e41aceb06.tar.gz", "highway-"); err != nil {
		t.Fatalf("existing Ghostty-hosted rule regressed: %v", err)
	}
}

func testTarGz(t *testing.T, members map[string]string, comment string) []byte {
	t.Helper()

	var buffer bytes.Buffer

	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)

	if comment != "" {
		header := &tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": comment}}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
	}

	for name, content := range members {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}

		if _, err := tarWriter.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}

	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}

	return buffer.Bytes()
}

func TestRequireUucodeArchiveBinding(t *testing.T) {
	githubURL := "https://github.com/jacobsandlund/uucode/archive/" + testUucodeSHA + ".tar.gz"
	root := "uucode-" + testUucodeSHA + "/"

	tests := []struct {
		name    string
		url     string
		archive []byte
		wantErr string
	}{
		{"matching root and comment", githubURL, testTarGz(t, map[string]string{root + "LICENSE.md": "x"}, testUucodeSHA), ""},
		{"matching root without header", githubURL, testTarGz(t, map[string]string{root + "LICENSE.md": "x"}, ""), ""},
		{"different commit root", githubURL, testTarGz(t, map[string]string{"uucode-" + strings.Repeat("a", 40) + "/LICENSE.md": "x"}, ""), "outside"},
		{"stray member", githubURL, testTarGz(t, map[string]string{root + "LICENSE.md": "x", "evil/file": "y"}, ""), "outside"},
		{"mismatched comment", githubURL, testTarGz(t, map[string]string{root + "LICENSE.md": "x"}, strings.Repeat("b", 40)), "does not match"},
		{"empty archive", githubURL, testTarGz(t, nil, ""), "empty"},
		{"not gzip", githubURL, []byte("nope"), "open uucode archive"},
		{"ghostty mirror unconstrained", "https://deps.files.ghostty.org/uucode-" + testUucodeSHA + ".tar.gz", testTarGz(t, map[string]string{"anything/LICENSE.md": "x"}, ""), ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := requireUucodeArchiveBinding(test.url, test.archive)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}
