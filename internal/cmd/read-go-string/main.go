// Command read-go-string verifies a linker-injected Go string directly in an
// ELF or Mach-O executable. It is intentionally internal packaging machinery.
package main

import (
	"debug/elf"
	"debug/macho"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

const maxStringLength = 1 << 20

type executableSection interface {
	Open() io.ReadSeeker
}

type sectionRange struct {
	addr uint64
	size uint64
	data executableSection
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: read-go-string <binary> <symbol> <expected>")
		os.Exit(2)
	}

	actual, err := readGoString(os.Args[1], os.Args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "read Go string: %v\n", err)
		os.Exit(1)
	}

	if actual != os.Args[3] {
		fmt.Fprintln(os.Stderr, "linker-injected Go string does not match the expected value")
		os.Exit(1)
	}
}

func readGoString(path, symbol string) (string, error) {
	elfFile, elfErr := elf.Open(path)
	if elfErr == nil {
		defer func() { _ = elfFile.Close() }()
		return readELFGoString(elfFile, symbol)
	}

	machoFile, machoErr := macho.Open(path)
	if machoErr == nil {
		defer func() { _ = machoFile.Close() }()
		return readMachOGoString(machoFile, symbol)
	}

	return "", fmt.Errorf("unsupported executable format (ELF: %w; Mach-O: %w)", elfErr, machoErr)
}

func readELFGoString(file *elf.File, name string) (string, error) {
	symbols, err := file.Symbols()
	if err != nil {
		return "", fmt.Errorf("read ELF symbols: %w", err)
	}

	values := make(map[string][]uint64)

	for _, symbol := range symbols {
		if symbol.Name == name || symbol.Name == name+".str" {
			values[symbol.Name] = append(values[symbol.Name], symbol.Value)
		}
	}

	sections := make([]sectionRange, 0, len(file.Sections))
	for _, section := range file.Sections {
		sections = append(sections, sectionRange{section.Addr, section.Size, section})
	}

	return decodeGoString(values, name, file.ByteOrder, sections)
}

func readMachOGoString(file *macho.File, name string) (string, error) {
	if file.Symtab == nil {
		return "", errors.New("Mach-O executable has no symbol table")
	}

	values := make(map[string][]uint64)

	for _, symbol := range file.Symtab.Syms {
		normalized := symbol.Name
		if len(normalized) > 0 && normalized[0] == '_' {
			normalized = normalized[1:]
		}

		if normalized == name || normalized == name+".str" {
			values[normalized] = append(values[normalized], symbol.Value)
		}
	}

	sections := make([]sectionRange, 0, len(file.Sections))
	for _, section := range file.Sections {
		sections = append(sections, sectionRange{section.Addr, section.Size, section})
	}

	return decodeGoString(values, name, file.ByteOrder, sections)
}

func decodeGoString(
	values map[string][]uint64,
	name string,
	order binary.ByteOrder,
	sections []sectionRange,
) (string, error) {
	headerAddress, err := uniqueSymbol(values[name], name)
	if err != nil {
		return "", err
	}

	dataAddress, err := uniqueSymbol(values[name+".str"], name+".str")
	if err != nil {
		return "", err
	}

	header, err := readVirtual(sections, headerAddress, 16)
	if err != nil {
		return "", fmt.Errorf("read %s header: %w", name, err)
	}

	pointer := order.Uint64(header[:8])
	length := order.Uint64(header[8:])

	if pointer != dataAddress {
		return "", fmt.Errorf(
			"%s header points to %#x, not %s.str at %#x",
			name,
			pointer,
			name,
			dataAddress,
		)
	}

	if length > maxStringLength {
		return "", fmt.Errorf("%s length %d exceeds safety limit", name, length)
	}

	data, err := readVirtual(sections, pointer, length)
	if err != nil {
		return "", fmt.Errorf("read %s data: %w", name, err)
	}

	return string(data), nil
}

func uniqueSymbol(addresses []uint64, name string) (uint64, error) {
	if len(addresses) != 1 {
		return 0, fmt.Errorf("expected exactly one %s symbol, found %d", name, len(addresses))
	}

	return addresses[0], nil
}

func readVirtual(sections []sectionRange, address, size uint64) ([]byte, error) {
	for _, section := range sections {
		if address < section.addr || address-section.addr > section.size {
			continue
		}

		offset := address - section.addr
		if size > section.size-offset {
			continue
		}

		if offset > uint64(1<<63-1) {
			return nil, fmt.Errorf("section offset %#x exceeds the supported seek range", offset)
		}

		reader := section.data.Open()
		// The explicit bound above proves this conversion cannot overflow.
		if _, err := reader.Seek(int64(offset), io.SeekStart); err != nil {
			return nil, err
		}

		data := make([]byte, size)
		if _, err := io.ReadFull(reader, data); err != nil {
			return nil, err
		}

		return data, nil
	}

	return nil, fmt.Errorf("address %#x with size %d is outside file-backed sections", address, size)
}
