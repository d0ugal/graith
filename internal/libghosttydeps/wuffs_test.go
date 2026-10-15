package libghosttydeps

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testWuffsCommit  = "7411f488fe2e2c205c3d3b3d28638b7356522930"
	testWuffsURL     = "https://deps.files.ghostty.org/wuffs-" + testWuffsCommit + ".tar.gz"
	testWuffsZigHash = "N-V-__8AAP5JWgCGP_AD0teWpa4krRvE9VPZzvviGdbmN4jI"
	testWuffsVersion = "0.4.0-alpha.10+3966.20260623"
)

func testWuffsEntry() *Wuffs {
	return &Wuffs{
		Repository:        wuffsRepository,
		Version:           testWuffsVersion,
		Commit:            testWuffsCommit,
		CSource:           "release/c/wuffs-v0.4.c",
		SourceURL:         testWuffsURL,
		ZigHash:           testWuffsZigHash,
		ArchiveSHA256:     strings.Repeat("a", 64),
		CSourceSHA256:     strings.Repeat("b", 64),
		LicenseSHA256:     strings.Repeat("c", 64),
		ApacheSHA256:      strings.Repeat("d", 64),
		MITSHA256:         strings.Repeat("e", 64),
		LicenseConclusion: "Apache-2.0 OR MIT",
		LicenseDeclared:   "Apache-2.0 OR MIT",
		LicenseReview:     pendingLicenseReview,
	}
}

// committedLock loads the committed lock with its Wuffs entry removed, so
// tests start from a lock that does not track Wuffs whether or not the
// committed lock currently does. Wuffs-tracking fixtures add their own entry.
func committedLock(t *testing.T) Lock {
	t.Helper()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	lock, err := LoadLock(filepath.Join(root, LockFilename))
	if err != nil {
		t.Fatal(err)
	}

	lock.Wuffs = nil

	return lock
}

func TestLockWithoutWuffsOmitsTheKey(t *testing.T) {
	data, err := json.Marshal(committedLock(t))
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(data), "wuffs") {
		t.Fatalf("a lock without Wuffs must not serialize a wuffs key: %s", data)
	}
}

func TestWuffsLockValidation(t *testing.T) {
	tests := map[string]struct {
		mutate  func(*Wuffs)
		wantErr string
	}{
		"complete entry":         {mutate: func(*Wuffs) {}},
		"short commit":           {mutate: func(w *Wuffs) { w.Commit = "7411f48" }, wantErr: "invalid Wuffs commit"},
		"uppercase commit":       {mutate: func(w *Wuffs) { w.Commit = strings.ToUpper(testWuffsCommit) }, wantErr: "invalid Wuffs commit"},
		"URL names other sha":    {mutate: func(w *Wuffs) { w.Commit = strings.Repeat("1", 40) }, wantErr: "source URL does not name the Wuffs commit"},
		"traversing C source":    {mutate: func(w *Wuffs) { w.CSource = "release/c/../../evil.c" }, wantErr: "invalid Wuffs C source path"},
		"absolute C source":      {mutate: func(w *Wuffs) { w.CSource = "/release/c/wuffs-v0.4.c" }, wantErr: "invalid Wuffs C source path"},
		"other directory":        {mutate: func(w *Wuffs) { w.CSource = "src/wuffs-v0.4.c" }, wantErr: "invalid Wuffs C source path"},
		"not a release series":   {mutate: func(w *Wuffs) { w.CSource = "release/c/wuffs-latest.c" }, wantErr: "invalid Wuffs C source path"},
		"bad archive digest":     {mutate: func(w *Wuffs) { w.ArchiveSHA256 = "xyz" }, wantErr: "invalid Wuffs archive SHA-256"},
		"bad MIT digest":         {mutate: func(w *Wuffs) { w.MITSHA256 = "" }, wantErr: "invalid Wuffs MIT license SHA-256"},
		"bad Apache digest":      {mutate: func(w *Wuffs) { w.ApacheSHA256 = "" }, wantErr: "invalid Wuffs Apache-2.0 license SHA-256"},
		"bad review digest":      {mutate: func(w *Wuffs) { w.LicenseReview = "accepted" }, wantErr: "invalid Wuffs license review SHA-256"},
		"missing conclusion":     {mutate: func(w *Wuffs) { w.LicenseConclusion = "" }, wantErr: "missing Wuffs license conclusion"},
		"missing declared":       {mutate: func(w *Wuffs) { w.LicenseDeclared = "" }, wantErr: "missing Wuffs declared license"},
		"missing Zig hash":       {mutate: func(w *Wuffs) { w.ZigHash = "" }, wantErr: "missing Wuffs Zig hash"},
		"missing version":        {mutate: func(w *Wuffs) { w.Version = "" }, wantErr: "missing Wuffs version"},
		"missing repository":     {mutate: func(w *Wuffs) { w.Repository = "" }, wantErr: "missing Wuffs repository"},
		"another release series": {mutate: func(w *Wuffs) { w.CSource = "release/c/wuffs-v0.5.c" }},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			lock := committedLock(t)
			lock.Wuffs = testWuffsEntry()
			test.mutate(lock.Wuffs)

			err := lock.Validate()
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v", err)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Validate() = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestWuffsArchiveMemberFollowsCSource(t *testing.T) {
	tests := map[string]string{
		"release/c/wuffs-v0.4.c":  "wuffs-v0.4.o",
		"release/c/wuffs-v0.10.c": "wuffs-v0.10.o",
	}
	for source, want := range tests {
		if got := (Wuffs{CSource: source}).ArchiveMember(); got != want {
			t.Errorf("ArchiveMember(%q) = %q, want %q", source, got, want)
		}
	}
}

func TestWuffsLicenseReviewIsPendingUntilAccepted(t *testing.T) {
	lock := committedLock(t)
	lock.Wuffs = testWuffsEntry()

	// The committed non-Wuffs reviews stay valid; only Wuffs is outstanding.
	err := VerifyLicenseReviews(lock)
	if err == nil || !strings.Contains(err.Error(), "Wuffs license evidence or conclusion changed") {
		t.Fatalf("new Wuffs entry review = %v, want pending review failure", err)
	}

	for _, other := range []string{"go-libghostty", "Ghostty", "Zig", "uucode", "Highway", "simdutf"} {
		if strings.Contains(err.Error(), other) {
			t.Errorf("unrelated component %s reported: %v", other, err)
		}
	}

	accepted := lock
	AcceptLicenseReviews(&accepted)

	if lock.Wuffs.LicenseReview != pendingLicenseReview {
		t.Fatal("accepting a copy of the lock mutated the original Wuffs review")
	}

	if err := VerifyLicenseReviews(accepted); err != nil {
		t.Fatalf("accepted Wuffs review = %v", err)
	}

	evidence := map[string]func(*Wuffs){
		"LICENSE":          func(w *Wuffs) { w.LicenseSHA256 = strings.Repeat("1", 64) },
		"LICENSE-APACHE":   func(w *Wuffs) { w.ApacheSHA256 = strings.Repeat("2", 64) },
		"LICENSE-MIT":      func(w *Wuffs) { w.MITSHA256 = strings.Repeat("3", 64) },
		"conclusion":       func(w *Wuffs) { w.LicenseConclusion = "MIT" },
		"declared license": func(w *Wuffs) { w.LicenseDeclared = "MIT" },
	}
	for name, change := range evidence {
		t.Run(name, func(t *testing.T) {
			changed := accepted
			entry := *accepted.Wuffs
			change(&entry)
			changed.Wuffs = &entry

			if err := VerifyLicenseReviews(changed); err == nil || !strings.Contains(err.Error(), "Wuffs") {
				t.Fatalf("changed %s review = %v, want Wuffs failure", name, err)
			}
		})
	}

	// Changes that are not legal evidence do not invalidate the review.
	moved := accepted
	entry := *accepted.Wuffs
	entry.ArchiveSHA256 = strings.Repeat("9", 64)
	moved.Wuffs = &entry

	if err := VerifyLicenseReviews(moved); err != nil {
		t.Fatalf("archive-only change invalidated the license review: %v", err)
	}
}

func TestWuffsRenderingIsLockDriven(t *testing.T) {
	sourceRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	without := committedLock(t)
	with := without
	with.Wuffs = testWuffsEntry()

	noticesWithout := RenderNoticesInventory(without)
	noticesWith := RenderNoticesInventory(with)

	spdxWithout, err := RenderSPDX(without)
	if err != nil {
		t.Fatal(err)
	}

	spdxWith, err := RenderSPDX(with)
	if err != nil {
		t.Fatal(err)
	}

	committedNotices, err := os.ReadFile(filepath.Join(sourceRoot, NoticesFilename))
	if err != nil {
		t.Fatal(err)
	}

	realLock, err := LoadLock(filepath.Join(sourceRoot, LockFilename))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(committedNotices), RenderNoticesInventory(realLock)) {
		t.Fatal("the committed lock no longer renders the committed notice inventory")
	}

	tests := []struct {
		name       string
		document   string
		wantAbsent bool
		wantAll    []string
	}{
		{"notices without Wuffs", noticesWithout, true, []string{"Wuffs"}},
		{"notices with Wuffs", noticesWith, false, []string{
			"| Wuffs | `" + testWuffsVersion + "`, mirror `" + testWuffsCommit + "`, Zig hash `" + testWuffsZigHash + "` | Apache-2.0 OR MIT, declared Apache-2.0 OR MIT |",
			"Wuffs archive `" + testWuffsURL + "` SHA-256",
			strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64),
			strings.Repeat("d", 64), strings.Repeat("e", 64), wuffsNotice,
		}},
		{"SPDX without Wuffs", string(spdxWithout), true, []string{"Wuffs"}},
		{"SPDX with Wuffs", string(spdxWith), false, []string{
			`"SPDXRef-Package-Wuffs"`, `"versionInfo": "` + testWuffsVersion + `"`,
			"pkg:github/google/wuffs-mirror-release-c@" + testWuffsCommit,
			`"licenseConcluded": "Apache-2.0 OR MIT"`, strings.Repeat("a", 64),
			`"relatedSpdxElement": "SPDXRef-Package-Wuffs"`,
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if test.wantAbsent {
				for _, absent := range test.wantAll {
					if strings.Contains(test.document, absent) {
						t.Fatalf("document mentions %q although the lock does not track it", absent)
					}
				}

				return
			}

			for _, want := range test.wantAll {
				if !strings.Contains(test.document, want) {
					t.Errorf("document is missing %q", want)
				}
			}
		})
	}

	var document struct {
		Packages []struct {
			SPDXID string `json:"SPDXID"`
		} `json:"packages"`
	}

	for name, rendered := range map[string][]byte{"without": spdxWithout, "with": spdxWith} {
		if err := json.Unmarshal(rendered, &document); err != nil {
			t.Fatal(err)
		}

		want := 6
		if name == "with" {
			want = 7
		}

		if len(document.Packages) != want {
			t.Errorf("SPDX %s Wuffs has %d packages, want %d", name, len(document.Packages), want)
		}
	}
}

func TestVerifyGatesWuffsLicenseReviewEndToEnd(t *testing.T) {
	sourceRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	root := copyCommittedUnit(t, sourceRoot)
	lockPath := filepath.Join(root, LockFilename)

	lock := committedLock(t)
	lock.Wuffs = testWuffsEntry()

	regenerate := func(lock Lock) {
		t.Helper()

		if err := WriteLock(lockPath, lock); err != nil {
			t.Fatal(err)
		}

		if err := writeGeneratedMetadata(root, lock); err != nil {
			t.Fatal(err)
		}
	}

	regenerate(lock)

	if err := VerifyGenerated(root); err != nil {
		t.Fatalf("mechanical projection of a new Wuffs entry = %v", err)
	}

	if err := Verify(root); err == nil || !strings.Contains(err.Error(), "Wuffs license evidence or conclusion changed") {
		t.Fatalf("merge gate on an unreviewed Wuffs entry = %v", err)
	}

	AcceptLicenseReviews(&lock)
	regenerate(lock)

	if err := Verify(root); err != nil {
		t.Fatalf("merge gate after explicit review = %v", err)
	}

	// Dropping Wuffs again restores the original projection.
	lock.Wuffs = nil
	regenerate(lock)

	if err := Verify(root); err != nil {
		t.Fatalf("merge gate after Wuffs is no longer linked = %v", err)
	}
}

const (
	testWuffsManifest = `.{
    .name = .wuffs,
    .version = "0.0.0",
    .fingerprint = 0x67c0c059de921c4f,
    .dependencies = .{
        .translate_c = .{ .path = "../translate-c" },

        // google/wuffs
        .wuffs = .{
            .url = "` + testWuffsURL + `",
            .hash = "` + testWuffsZigHash + `",
            .lazy = true,
        },

        // make-github-pseudonymous-again/pixels
        .pixels = .{
            .url = "https://deps.files.ghostty.org/pixels-12207ff340169c7d40c570b4b6a97db614fe47e0d83b5801a932dcd44917424c8806.tar.gz",
            .hash = "N-V-__8AADYiAAB_80AWnH1AxXC0tql9thT-R-DYO1gBqTLc",
            .lazy = true,
        },
    },
}
`
	testWuffsBuild = `        wuffs_c.mod.addCSourceFile(.{
            .file = wuffs_dep.path("release/c/wuffs-v0.4.c"),
            .flags = c_flags,
        });
`
	testWuffsCSource = `// Copyright 2017 The Wuffs Authors.
//
// SPDX-License-Identifier: Apache-2.0 OR MIT

#define WUFFS_VERSION_STRING "` + testWuffsVersion + `"
`
	testGhosttyZigLinking    = "    if (vt_options.kittyGraphics(cfg.target.result)) {\n        if (b.lazyDependency(\"wuffs\", .{})) |dep| {}\n    }\n"
	testGhosttyZigNotLinking = "    vt.addImport(\"uucode\", deps.uucode_mod);\n"
)

func writeWuffsSource(t *testing.T, linked bool) string {
	t.Helper()

	source := t.TempDir()

	wiring := testGhosttyZigNotLinking
	if linked {
		wiring = testGhosttyZigLinking
	}

	writeTestFile(t, filepath.Join(source, wuffsVTWiring), wiring)
	writeTestFile(t, filepath.Join(source, wuffsPackageManifest), testWuffsManifest)
	writeTestFile(t, filepath.Join(source, wuffsPackageBuild), testWuffsBuild)

	return source
}

func wuffsArchiveFixture(t *testing.T, omit string, overrideURL ...string) func(*http.Request) (*http.Response, error) {
	t.Helper()

	root := testWuffsZigHash + "/"
	members := map[string]string{
		root + "LICENSE":                   "both licenses",
		root + "LICENSE-APACHE":            "apache text",
		root + "LICENSE-MIT":               "mit text",
		root + "release/c/wuffs-v0.4.c":    testWuffsCSource,
		root + "release/c/wuffs-v0.3.c":    "older series",
		root + "release/c/README.md":       "readme",
		root + "docs/contributing.md":      "contributing",
		root + ".github/ISSUE_TEMPLATE.md": "issues",
	}
	delete(members, root+omit)

	archive := testTarGz(t, members, "")

	return func(request *http.Request) (*http.Response, error) {
		want := testWuffsURL
		if len(overrideURL) > 0 {
			want = overrideURL[0]
		}

		if request.URL.String() != want {
			return response(http.StatusNotFound, "unexpected URL "+request.URL.String()), nil
		}

		return response(http.StatusOK, string(archive)), nil
	}
}

func TestRefreshWuffsTracksOnlyWhatGhosttyLinks(t *testing.T) {
	tests := []struct {
		name     string
		linked   bool
		previous *Wuffs
		derive   bool
		omit     string
		wantErr  string
		wantNil  bool
		inspect  func(*testing.T, Wuffs)
	}{
		{
			name:    "not linked and not tracked stays untracked",
			linked:  false,
			wantNil: true,
		},
		{
			name:     "no longer linked drops the entry",
			linked:   false,
			previous: testWuffsEntry(),
			derive:   true,
			wantNil:  true,
		},
		{
			name:   "newly linked entry is generated with a pending review",
			linked: true,
			derive: true,
			inspect: func(t *testing.T, got Wuffs) {
				t.Helper()

				if got.LicenseReview != pendingLicenseReview {
					t.Errorf("new entry review = %q, want pending", got.LicenseReview)
				}

				if got.LicenseConclusion != "MIT" || got.LicenseDeclared != "Apache-2.0 OR MIT" {
					t.Errorf("conclusion/declared = %q/%q", got.LicenseConclusion, got.LicenseDeclared)
				}

				want := Wuffs{
					Repository: wuffsRepository, Version: testWuffsVersion, Commit: testWuffsCommit,
					CSource: "release/c/wuffs-v0.4.c", SourceURL: testWuffsURL, ZigHash: testWuffsZigHash,
					LicenseSHA256: bytesSHA256([]byte("both licenses")), ApacheSHA256: bytesSHA256([]byte("apache text")),
					MITSHA256: bytesSHA256([]byte("mit text")), CSourceSHA256: bytesSHA256([]byte(testWuffsCSource)),
					LicenseConclusion: "MIT", LicenseDeclared: "Apache-2.0 OR MIT",
					LicenseReview: pendingLicenseReview, ArchiveSHA256: got.ArchiveSHA256,
				}
				if got != want {
					t.Errorf("generated entry = %+v, want %+v", got, want)
				}

				if got.ArchiveSHA256 == "" || got.ArchiveSHA256 == pendingArtifactSHA256 {
					t.Errorf("archive digest not recorded: %q", got.ArchiveSHA256)
				}
			},
		},
		{
			name:   "refresh keeps the human conclusion and review",
			linked: true,
			derive: false,
			previous: func() *Wuffs {
				entry := testWuffsEntry()
				entry.LicenseConclusion = "MIT"
				entry.LicenseReview = strings.Repeat("7", 64)

				return entry
			}(),
			inspect: func(t *testing.T, got Wuffs) {
				t.Helper()

				if got.LicenseConclusion != "MIT" || got.LicenseReview != strings.Repeat("7", 64) {
					t.Errorf("refresh discarded the reviewed decision: %q %q", got.LicenseConclusion, got.LicenseReview)
				}
			},
		},
		{
			name:   "version drift without derivation is rejected",
			linked: true,
			derive: false,
			previous: func() *Wuffs {
				entry := testWuffsEntry()
				entry.Version = "0.4.0-alpha.9+1.20260101"

				return entry
			}(),
			wantErr: "Ghostty compiles Wuffs",
		},
		{
			name:    "missing MIT license is rejected",
			linked:  true,
			derive:  true,
			omit:    "LICENSE-MIT",
			wantErr: "does not contain /LICENSE-MIT",
		},
		{
			name:    "missing C source is rejected",
			linked:  true,
			derive:  true,
			omit:    "release/c/wuffs-v0.4.c",
			wantErr: "does not contain /release/c/wuffs-v0.4.c",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := writeWuffsSource(t, test.linked)

			withHTTPClient(t, &http.Client{Transport: roundTripFunc(wuffsArchiveFixture(t, test.omit))})

			lock := committedLock(t)
			lock.Wuffs = test.previous

			err := refreshWuffs(context.Background(), source, &lock, test.derive)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("refreshWuffs() = %v, want containing %q", err, test.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("refreshWuffs() = %v", err)
			}

			if test.wantNil {
				if lock.Wuffs != nil {
					t.Fatalf("lock tracks Wuffs: %+v", lock.Wuffs)
				}

				return
			}

			if lock.Wuffs == nil {
				t.Fatal("lock does not track Wuffs")
			}

			if test.inspect != nil {
				test.inspect(t, *lock.Wuffs)
			}
		})
	}
}

func TestRefreshWuffsRejectsUntrustedSources(t *testing.T) {
	const commit = testWuffsCommit

	tests := map[string]string{
		"other host":          "https://example.invalid/wuffs-" + commit + ".tar.gz",
		"GitHub archive":      "https://github.com/google/wuffs-mirror-release-c/archive/" + commit + ".tar.gz",
		"plain HTTP":          "http://deps.files.ghostty.org/wuffs-" + commit + ".tar.gz",
		"wrong file prefix":   "https://deps.files.ghostty.org/uucode-" + commit + ".tar.gz",
		"short commit":        "https://deps.files.ghostty.org/wuffs-7411f48.tar.gz",
		"old multihash style": "https://deps.files.ghostty.org/wuffs-122037b39d577ec2db3fd7b2130e7b69ef6cc1807d68607a7c232c958315d381b5cd.tar.gz",
	}

	for name, url := range tests {
		t.Run(name, func(t *testing.T) {
			source := writeWuffsSource(t, true)
			manifest := strings.Replace(testWuffsManifest, testWuffsURL, url, 1)
			writeTestFile(t, filepath.Join(source, wuffsPackageManifest), manifest)

			// Any request is a failure: an untrusted source must not be fetched.
			withHTTPClient(t, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				t.Errorf("fetched untrusted Wuffs source %s", request.URL)

				return response(http.StatusNotFound, ""), nil
			})})

			lock := committedLock(t)
			if err := refreshWuffs(context.Background(), source, &lock, true); err == nil {
				t.Fatal("refreshWuffs accepted an untrusted source")
			}

			if lock.Wuffs != nil {
				t.Fatalf("lock gained a Wuffs entry from an untrusted source: %+v", lock.Wuffs)
			}
		})
	}
}

func TestValidateRepositoriesPinsWuffs(t *testing.T) {
	lock := committedLock(t)
	if err := validateRepositories(lock); err != nil {
		t.Fatal(err)
	}

	lock.Wuffs = testWuffsEntry()
	if err := validateRepositories(lock); err != nil {
		t.Fatalf("expected Wuffs repository rejected: %v", err)
	}

	lock.Wuffs.Repository = "evil/wuffs"
	if err := validateRepositories(lock); err == nil || !strings.Contains(err.Error(), "unexpected Wuffs repository") {
		t.Fatalf("validateRepositories() = %v", err)
	}
}

func TestTarGzipMemberLimitBoundsLargeSources(t *testing.T) {
	archive := testTarGz(t, map[string]string{"root/big.c": strings.Repeat("x", 5<<20)}, "")

	if _, err := tarGzipMember(archive, "/big.c"); err == nil || !strings.Contains(err.Error(), "exceeded 4 MiB") {
		t.Fatalf("default limit error = %v", err)
	}

	if data, err := tarGzipMemberLimit(archive, "/big.c", 16<<20); err != nil || len(data) != 5<<20 {
		t.Fatalf("raised limit = %d bytes, %v", len(data), err)
	}

	if _, err := tarGzipMemberLimit(archive, "/big.c", 1<<20); err == nil || !strings.Contains(err.Error(), "exceeded 1 MiB") {
		t.Fatalf("explicit small limit error = %v", err)
	}
}
