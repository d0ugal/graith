package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"testing"
)

type memorySection []byte

func (section memorySection) Open() io.ReadSeeker {
	return bytes.NewReader(section)
}

func TestDecodeGoString(t *testing.T) {
	const (
		name          = "github.com/d0ugal/graith/internal/version.CommitSHA"
		sectionAddr   = 0x1000
		headerAddress = 0x1010
		dataAddress   = 0x1040
	)

	section := make(memorySection, 128)
	binary.LittleEndian.PutUint64(section[0x10:0x18], dataAddress)
	binary.LittleEndian.PutUint64(section[0x18:0x20], 4)
	copy(section[0x40:], "braw")

	values := map[string][]uint64{
		name:          {headerAddress},
		name + ".str": {dataAddress},
	}

	got, err := decodeGoString(values, name, binary.LittleEndian, []sectionRange{{
		addr: sectionAddr,
		size: uint64(len(section)),
		data: section,
	}})
	if err != nil {
		t.Fatalf("decodeGoString() error = %v", err)
	}

	if got != "braw" {
		t.Fatalf("decodeGoString() = %q, want braw", got)
	}
}

func TestDecodeGoStringRejectsUnrelatedBackingSymbol(t *testing.T) {
	const (
		name          = "github.com/d0ugal/graith/internal/version.CommitSHA"
		sectionAddr   = 0x1000
		headerAddress = 0x1010
		dataAddress   = 0x1040
	)

	section := make(memorySection, 128)
	binary.LittleEndian.PutUint64(section[0x10:0x18], dataAddress+8)
	binary.LittleEndian.PutUint64(section[0x18:0x20], 5)
	copy(section[0x48:], "canny")

	values := map[string][]uint64{
		name:          {headerAddress},
		name + ".str": {dataAddress},
	}

	_, err := decodeGoString(values, name, binary.LittleEndian, []sectionRange{{
		addr: sectionAddr,
		size: uint64(len(section)),
		data: section,
	}})
	if err == nil || !strings.Contains(err.Error(), "header points to") {
		t.Fatalf("decodeGoString() error = %v, want backing-symbol rejection", err)
	}
}

func TestUniqueSymbolRejectsDuplicates(t *testing.T) {
	_, err := uniqueSymbol([]uint64{0x1000, 0x2000}, "braw")
	if err == nil {
		t.Fatal("uniqueSymbol() accepted duplicate symbols")
	}
}

func TestReadVirtualRejectsUnrepresentableOffset(t *testing.T) {
	_, err := readVirtual([]sectionRange{{
		addr: 0,
		size: ^uint64(0),
		data: memorySection(nil),
	}}, uint64(1)<<63, 0)
	if err == nil || !strings.Contains(err.Error(), "seek range") {
		t.Fatalf("readVirtual() error = %v, want seek-range rejection", err)
	}
}
