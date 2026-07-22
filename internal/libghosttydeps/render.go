package libghosttydeps

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	NoticesBeginMarker = "<!-- BEGIN GENERATED LIBGHOSTTY DEPENDENCY INVENTORY -->"
	NoticesEndMarker   = "<!-- END GENERATED LIBGHOSTTY DEPENDENCY INVENTORY -->"
)

func RenderNoticesInventory(lock Lock) string {
	return fmt.Sprintf(`This file applies only to Graith testing candidates built with the
%[1]clibghostty%[1]c build tag. Ordinary pure-Go builds do not contain this
native dependency closure. The committed %[1]clibghostty-native.spdx.json%[1]c is the
machine-readable dependency inventory; candidate packaging materializes from it
a document with the same filename that is bound to the binary's exact revision,
target, and SHA-256. %[1]clibghostty-native.lock.json%[1]c is the canonical update
source. The script, generated inventory, and this file must be rotated as one
dependency unit.

## Exact dependency and provenance inventory

| Component | Exact compiled pin | Distributed license conclusion |
|-----------|--------------------|--------------------------------|
| go-libghostty | %[1]c%[2]s%[1]c / %[1]c%[3]s%[1]c | %[4]s |
| Ghostty libghostty-vt | %[1]c%[5]s%[1]c / %[1]c%[6]s%[1]c | %[7]s |
| uucode | %[1]c%[8]s%[1]c, Zig hash %[1]c%[9]s%[1]c | %[10]s |
| Highway | %[1]c%[11]s%[1]c, upstream %[1]c%[12]s%[1]c, Zig hash %[1]c%[13]s%[1]c | %[14]s, elected from %[15]s |
| simdutf amalgamation | compiled version %[1]c%[16]s%[1]c, corresponding upstream %[1]c%[17]s%[1]c | %[18]s |
| Zig bundled runtime | %[1]c%[19]s%[1]c compiler runtime and UBSan runtime | %[20]s |

Ghostty's %[1]cpkg/simdutf/build.zig.zon%[1]c still says %[1]c%[21]s%[1]c, but that metadata is
stale: the exact vendored header identifies %[1]c%[16]s%[1]c. The source hashes below
bind this conclusion to the code that is compiled. Ghostty explicitly bundles
the Zig compiler and UBSan runtimes. Archive-member, string, and symbol checks
therefore treat those runtime objects as distributed content rather than as a
build-only tool.

The exact verified inputs are:

- go-libghostty module sum
  %[1]c%[22]s%[1]c, wrapper-tested Ghostty commit
  %[1]c%[42]s%[1]c, and LICENSE SHA-256
  %[1]c%[23]s%[1]c;
- Ghostty LICENSE SHA-256
  %[1]c%[24]s%[1]c and committed-header tree SHA-256
  %[1]c%[25]s%[1]c;
- Linux source-build configuration %[1]c-Demit-lib-vt=true%[1]c,
  %[1]c-Demit-xcframework=false%[1]c, %[1]c-Doptimize=ReleaseFast%[1]c, and the
  target-specific %[1]c-Dtarget=x86_64-linux-gnu%[1]c or
  %[1]c-Dtarget=aarch64-linux-gnu%[1]c; no Apple archive is used for Linux;
- Apple testing archive used by the macOS native candidate
  %[1]c%[26]s%[1]c
  SHA-256
  %[1]c%[27]s%[1]c;
- Zig source URL %[1]c%[28]s%[1]c,
  source SHA-256
  %[1]c%[29]s%[1]c,
  and LICENSE SHA-256
  %[1]c%[30]s%[1]c;
- uucode archive %[1]c%[31]s%[1]c SHA-256
  %[1]c%[32]s%[1]c,
  LICENSE SHA-256
  %[1]c%[33]s%[1]c,
  Bjoern Hoehrmann notice SHA-256
  %[1]c%[34]s%[1]c,
  and Unicode notice SHA-256
  %[1]c%[35]s%[1]c;
- Highway archive %[1]c%[36]s%[1]c SHA-256
  %[1]c%[37]s%[1]c and elected BSD license SHA-256
  %[1]c%[38]s%[1]c;
- vendored simdutf.cpp SHA-256
  %[1]c%[39]s%[1]c,
  simdutf.h SHA-256
  %[1]c%[40]s%[1]c,
  and upstream MIT license SHA-256
  %[1]c%[41]s%[1]c.

The uucode comparison resources excluded by that package's
%[1]cbuild.zig.zon%[1]c are not compiled. The notices below cover the uucode code,
UTF-8 decoder, and Unicode tables that are compiled. System frameworks and C
runtime libraries reported by executable-format inspection remain dynamically
provided by the operating system and are not copied into the candidate.
`, '`', lock.GoLibghostty.Version, lock.GoLibghostty.Commit,
		lock.GoLibghostty.LicenseConclusion, lock.Ghostty.Version, lock.Ghostty.Commit,
		lock.Ghostty.LicenseConclusion, lock.Uucode.Version, lock.Uucode.ZigHash,
		lock.Uucode.LicenseConclusion, lock.Highway.Version, lock.Highway.Commit,
		lock.Highway.ZigHash, lock.Highway.LicenseConclusion, lock.Highway.LicenseDeclared,
		lock.Simdutf.Version, lock.Simdutf.Commit, lock.Simdutf.LicenseConclusion,
		lock.Zig.Version, lock.Zig.LicenseConclusion, lock.Simdutf.ManifestVersion,
		lock.GoLibghostty.ModuleSum, lock.GoLibghostty.LicenseSHA256,
		lock.Ghostty.LicenseSHA256, lock.Ghostty.HeadersSHA256,
		lock.Ghostty.AppleArtifact.URL, lock.Ghostty.AppleArtifact.SHA256,
		lock.Zig.SourceURL, lock.Zig.SourceSHA256, lock.Zig.LicenseSHA256,
		lock.Uucode.SourceURL, lock.Uucode.ArchiveSHA256, lock.Uucode.LicenseSHA256,
		lock.Uucode.DecoderNoticeSHA256, lock.Uucode.UnicodeNoticeSHA256,
		lock.Highway.SourceURL, lock.Highway.ArchiveSHA256, lock.Highway.LicenseSHA256,
		lock.Simdutf.CppSHA256, lock.Simdutf.HeaderSHA256, lock.Simdutf.LicenseSHA256,
		lock.GoLibghostty.TestedGhosttyCommit)
}

func ReplaceNoticesInventory(document string, lock Lock) (string, error) {
	begin := strings.Index(document, NoticesBeginMarker)

	end := strings.Index(document, NoticesEndMarker)
	if begin < 0 || end < 0 || begin >= end {
		return "", errors.New("notice inventory markers are missing or out of order")
	}

	prefix := document[:begin+len(NoticesBeginMarker)]
	suffix := document[end:]

	return prefix + "\n" + RenderNoticesInventory(lock) + suffix, nil
}

func RenderSPDX(lock Lock) ([]byte, error) {
	packages := []any{
		map[string]any{
			"SPDXID":           "SPDXRef-Package-GoLibghostty",
			"copyrightText":    "Copyright (c) 2026 Mitchell Hashimoto",
			"downloadLocation": lock.GoLibghostty.Repository,
			"externalRefs":     []any{packageRef("pkg:golang/go.mitchellh.com/libghostty@" + lock.GoLibghostty.Version)},
			"filesAnalyzed":    false,
			"licenseComments": fmt.Sprintf("Exact revision %s; module sum %s; wrapper build tests pin Ghostty %s; LICENSE SHA-256 %s.",
				lock.GoLibghostty.Commit, lock.GoLibghostty.ModuleSum, lock.GoLibghostty.TestedGhosttyCommit, lock.GoLibghostty.LicenseSHA256),
			"licenseConcluded": lock.GoLibghostty.LicenseConclusion,
			"licenseDeclared":  lock.GoLibghostty.LicenseConclusion,
			"name":             "go-libghostty",
			"supplier":         "Person: Mitchell Hashimoto",
			"versionInfo":      lock.GoLibghostty.Version,
		},
		map[string]any{
			"SPDXID":           "SPDXRef-Package-Ghostty",
			"copyrightText":    "Copyright (c) 2024 Mitchell Hashimoto, Ghostty contributors",
			"downloadLocation": lock.Ghostty.Repository,
			"externalRefs":     []any{packageRef("pkg:github/ghostty-org/ghostty@" + lock.Ghostty.Commit)},
			"filesAnalyzed":    false,
			"licenseComments": fmt.Sprintf("Exact source LICENSE SHA-256 %s. The committed header tree SHA-256 is %s. The Apple archive SHA-256 is %s.",
				lock.Ghostty.LicenseSHA256, lock.Ghostty.HeadersSHA256, lock.Ghostty.AppleArtifact.SHA256),
			"licenseConcluded": lock.Ghostty.LicenseConclusion,
			"licenseDeclared":  lock.Ghostty.LicenseConclusion,
			"name":             "Ghostty libghostty-vt",
			"sourceInfo": fmt.Sprintf("Built from exact commit %s with Zig %s and -Demit-lib-vt=true -Demit-xcframework=true -Doptimize=ReleaseFast. No explicit SIMD override is passed by the pinned Apple build.",
				lock.Ghostty.Commit, lock.Zig.Version),
			"supplier":    "Organization: Ghostty contributors",
			"versionInfo": lock.Ghostty.Version + "+" + lock.Ghostty.Commit,
		},
		map[string]any{
			"SPDXID":           "SPDXRef-Package-Uucode",
			"checksums":        []any{checksum(lock.Uucode.ArchiveSHA256)},
			"copyrightText":    "Copyright (c) 2026 Jacob Sandlund; Copyright (c) 2008-2009 Bjoern Hoehrmann; Copyright (c) 1991-2025 Unicode, Inc.",
			"downloadLocation": lock.Uucode.SourceURL,
			"externalRefs":     []any{packageRef("pkg:github/jacobsandlund/uucode@" + lock.Uucode.Version)},
			"filesAnalyzed":    false,
			"licenseComments": fmt.Sprintf("LICENSE SHA-256 %s; Bjoern Hoehrmann decoder notice SHA-256 %s; Unicode notice SHA-256 %s.",
				lock.Uucode.LicenseSHA256, lock.Uucode.DecoderNoticeSHA256, lock.Uucode.UnicodeNoticeSHA256),
			"licenseConcluded": lock.Uucode.LicenseConclusion,
			"licenseDeclared":  lock.Uucode.LicenseConclusion,
			"name":             "uucode",
			"sourceInfo": fmt.Sprintf("Ghostty pins Zig content hash %s and compiles the UTF-8 decoder and Unicode tables into libghostty-vt.",
				lock.Uucode.ZigHash),
			"supplier":    "Person: Jacob Sandlund",
			"versionInfo": lock.Uucode.Version,
		},
		map[string]any{
			"SPDXID":           "SPDXRef-Package-Highway",
			"checksums":        []any{checksum(lock.Highway.ArchiveSHA256)},
			"copyrightText":    "Copyright (c) The Highway Project Authors",
			"downloadLocation": lock.Highway.SourceURL,
			"externalRefs":     []any{packageRef("pkg:github/google/highway@" + lock.Highway.Commit)},
			"filesAnalyzed":    false,
			"licenseComments": fmt.Sprintf("The source is dual-licensed. Graith elects %s for binary distribution. The elected LICENSE-BSD SHA-256 is %s.",
				lock.Highway.LicenseConclusion, lock.Highway.LicenseSHA256),
			"licenseConcluded": lock.Highway.LicenseConclusion,
			"licenseDeclared":  lock.Highway.LicenseDeclared,
			"name":             "Highway",
			"sourceInfo": fmt.Sprintf("Ghostty package version %s, exact upstream commit %s, Zig content hash %s.",
				lock.Highway.Version, lock.Highway.Commit, lock.Highway.ZigHash),
			"supplier":    "Organization: The Highway Project Authors",
			"versionInfo": lock.Highway.Version + "+" + lock.Highway.Commit,
		},
		map[string]any{
			"SPDXID":           "SPDXRef-Package-Simdutf",
			"copyrightText":    "Copyright 2021 The simdutf authors; Facebook, Inc.; Idiap Research Institute; Deepmind Technologies; NEC Laboratories America; NYU; Google Fuchsia contributors",
			"downloadLocation": fmt.Sprintf("https://github.com/ghostty-org/ghostty/tree/%s/pkg/simdutf/vendor", lock.Ghostty.Commit),
			"externalRefs":     []any{packageRef("pkg:github/simdutf/simdutf@" + lock.Simdutf.Commit)},
			"filesAnalyzed":    false,
			"licenseComments":  fmt.Sprintf("The simdutf project license is Apache-2.0 OR MIT; Graith elects MIT. Its amalgamation embeds BSD-3-Clause ISA detection derived from PyTorch and an Apache-2.0 UTF-8 validator derived from Google Fuchsia, so those licenses additionally apply. Upstream MIT LICENSE SHA-256 is %s.", lock.Simdutf.LicenseSHA256),
			"licenseConcluded": lock.Simdutf.LicenseConclusion,
			"licenseDeclared":  lock.Simdutf.LicenseDeclared,
			"name":             "simdutf vendored amalgamation",
			"sourceInfo": fmt.Sprintf("The compiled headers identify v%s and correspond to upstream commit %s. Ghostty's package manifest is stale at %s. Exact vendored simdutf.cpp SHA-256: %s. Exact vendored simdutf.h SHA-256: %s.",
				lock.Simdutf.Version, lock.Simdutf.Commit, lock.Simdutf.ManifestVersion,
				lock.Simdutf.CppSHA256, lock.Simdutf.HeaderSHA256),
			"supplier":    "Organization: The simdutf authors",
			"versionInfo": lock.Simdutf.Version + "+" + lock.Simdutf.Commit,
		},
		map[string]any{
			"SPDXID":           "SPDXRef-Package-ZigRuntime",
			"checksums":        []any{checksum(lock.Zig.SourceSHA256)},
			"copyrightText":    "Copyright (c) Zig contributors; Copyright (c) 2005-2020 Rich Felker et al.; LLVM Project contributors",
			"downloadLocation": lock.Zig.SourceURL,
			"externalRefs":     []any{packageRef("pkg:generic/zig-runtime@" + lock.Zig.Version)},
			"filesAnalyzed":    false,
			"licenseComments":  fmt.Sprintf("Ghostty explicitly bundles Zig compiler_rt and UBSan runtime objects. Zig's root LICENSE SHA-256 is %s. The runtime closure includes MIT code (including musl-derived code) and LLVM-derived code under Apache-2.0 WITH LLVM-exception.", lock.Zig.LicenseSHA256),
			"licenseConcluded": lock.Zig.LicenseConclusion,
			"licenseDeclared":  lock.Zig.LicenseConclusion,
			"name":             "Zig bundled compiler and UBSan runtime",
			"sourceInfo": fmt.Sprintf("Built from Zig %s source archive SHA-256 %s. Archive-member and symbol inspection confirms compiler_rt and UBSan runtime code in libghostty-vt.",
				lock.Zig.Version, lock.Zig.SourceSHA256),
			"supplier":    "Organization: Zig contributors",
			"versionInfo": lock.Zig.Version,
		},
	}
	document := map[string]any{
		"SPDXID": "SPDXRef-DOCUMENT",
		"creationInfo": map[string]any{
			"created":  "2026-07-18T00:00:00Z",
			"creators": []string{"Tool: graith libghostty native dependency audit"},
		},
		"dataLicense":       "CC0-1.0",
		"documentNamespace": fmt.Sprintf("https://github.com/d0ugal/graith/sbom/libghostty-native/%s/%s", lock.Ghostty.Commit, lock.GoLibghostty.Commit),
		"name":              "graith-libghostty-native-dependencies",
		"packages":          packages,
		"relationships": []any{
			relationship("SPDXRef-DOCUMENT", "DESCRIBES", "SPDXRef-Package-GoLibghostty"),
			relationship("SPDXRef-DOCUMENT", "DESCRIBES", "SPDXRef-Package-Ghostty"),
			relationship("SPDXRef-Package-Ghostty", "STATIC_LINK", "SPDXRef-Package-Uucode"),
			relationship("SPDXRef-Package-Ghostty", "STATIC_LINK", "SPDXRef-Package-Highway"),
			relationship("SPDXRef-Package-Ghostty", "STATIC_LINK", "SPDXRef-Package-Simdutf"),
			relationship("SPDXRef-Package-Ghostty", "STATIC_LINK", "SPDXRef-Package-ZigRuntime"),
		},
		"spdxVersion": "SPDX-2.3",
	}

	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render native SPDX inventory: %w", err)
	}

	return append(data, '\n'), nil
}

func checksum(value string) map[string]string {
	return map[string]string{"algorithm": "SHA256", "checksumValue": value}
}

func packageRef(locator string) map[string]string {
	return map[string]string{
		"referenceCategory": "PACKAGE-MANAGER",
		"referenceLocator":  locator,
		"referenceType":     "purl",
	}
}

func relationship(from, kind, to string) map[string]string {
	return map[string]string{
		"relatedSpdxElement": to,
		"relationshipType":   kind,
		"spdxElementId":      from,
	}
}
