package msg

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
	"unicode/utf16"
)

// The writer exists to feed the reader. This test pins that contract: a
// message built from a property map must come back out of Read with the same
// values, including a stream small enough to live in the mini stream.
func TestCFBWriterRoundTrip(t *testing.T) {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("Oggetto di prova"))
	root.addStream(substgName(pidTagBody, propTypeString), utf16LE("corpo"))

	raw := buildCFB(root)

	data, err := Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if data.Subject != "Oggetto di prova" {
		t.Errorf("expected subject %q, got %q", "Oggetto di prova", data.Subject)
	}
	if data.Body != "corpo" {
		t.Errorf("expected body %q, got %q", "corpo", data.Body)
	}
}

// A body above the mini-stream cutoff is stored in regular sectors, which is
// a different read path in the reader.
func TestCFBWriterLargeStream(t *testing.T) {
	big := strings.Repeat("a", 5000)

	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("Grande"))
	root.addStream(substgName(pidTagBodyHTML, propTypeString8), []byte(big))

	data, err := Read(bytes.NewReader(buildCFB(root)))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if data.Body != big {
		t.Errorf("expected a %d-byte body, got %d bytes", len(big), len(data.Body))
	}
}

const (
	cfbNoStream      = 0xFFFFFFFF // no such sibling, child, or sector
	cfbEndOfChain    = 0xFFFFFFFE
	cfbFATSector     = 0xFFFFFFFD
	cfbSectorSize    = 512
	cfbMiniSectSize  = 64
	cfbDirsPerSector = cfbSectorSize / directoryEntrySize // 4
)

// cfbEntry is a node in the compound file tree: either a storage (a
// directory) or a stream (a file).
type cfbEntry struct {
	name     string
	isStream bool
	data     []byte
	children []*cfbEntry

	// filled in by buildCFB
	index      int
	startSect  uint32
	streamSize uint64
}

func (e *cfbEntry) addStream(name string, data []byte) *cfbEntry {
	child := &cfbEntry{name: name, isStream: true, data: data}
	e.children = append(e.children, child)
	return child
}

func (e *cfbEntry) addStorage(name string) *cfbEntry {
	child := &cfbEntry{name: name}
	e.children = append(e.children, child)
	return child
}

// substgName renders a property stream name the way the reader's
// parsePropertyName expects: __substg1.0_ followed by the property ID and
// type as four uppercase hex digits each.
func substgName(propID uint32, propType uint16) string {
	return fmt.Sprintf("__substg1.0_%04X%04X", propID, propType)
}

// utf16LE encodes s the way MSG PT_UNICODE properties store it.
func utf16LE(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, len(units)*2)
	for i, u := range units {
		binary.LittleEndian.PutUint16(out[i*2:], u)
	}
	return out
}

// buildCFB assembles a complete OLE2 compound file holding the given tree.
//
// Layout, in sector order: regular streams (those at or above the 4096-byte
// mini-stream cutoff), the mini stream itself, the mini FAT, the directory,
// and finally the FAT. Sector count and FAT size depend on each other, so the
// allocation runs to a fixed point before anything is written.
func buildCFB(root *cfbEntry) []byte {
	// Flatten the tree depth-first; index 0 must be the root.
	var flat []*cfbEntry
	var walk func(e *cfbEntry)
	walk = func(e *cfbEntry) {
		e.index = len(flat)
		flat = append(flat, e)
		for _, c := range e.children {
			walk(c)
		}
	}
	walk(root)

	// Small streams go into the mini stream, 64 bytes per mini sector.
	var miniStream []byte
	var miniFAT []uint32
	for _, e := range flat {
		if !e.isStream || len(e.data) == 0 || len(e.data) >= miniStreamCutoff {
			continue
		}
		e.streamSize = uint64(len(e.data))
		e.startSect = uint32(len(miniStream) / cfbMiniSectSize)
		n := (len(e.data) + cfbMiniSectSize - 1) / cfbMiniSectSize
		for i := 0; i < n; i++ {
			next := uint32(len(miniFAT) + 1)
			if i == n-1 {
				next = cfbEndOfChain
			}
			miniFAT = append(miniFAT, next)
		}
		padded := make([]byte, n*cfbMiniSectSize)
		copy(padded, e.data)
		miniStream = append(miniStream, padded...)
	}

	// Regular sectors. sectors[i] is the payload of sector i; fat[i] is its
	// successor.
	var sectors [][]byte
	var fat []uint32

	// allocChain writes data across as many sectors as it needs and returns
	// the first sector number.
	allocChain := func(data []byte) uint32 {
		if len(data) == 0 {
			return cfbEndOfChain
		}
		first := uint32(len(sectors))
		n := (len(data) + cfbSectorSize - 1) / cfbSectorSize
		for i := 0; i < n; i++ {
			chunk := make([]byte, cfbSectorSize)
			copy(chunk, data[i*cfbSectorSize:min(len(data), (i+1)*cfbSectorSize)])
			sectors = append(sectors, chunk)
			next := uint32(len(sectors))
			if i == n-1 {
				next = cfbEndOfChain
			}
			fat = append(fat, next)
		}
		return first
	}

	// Streams at or above the cutoff live in regular sectors.
	for _, e := range flat {
		if !e.isStream || len(e.data) < miniStreamCutoff {
			continue
		}
		e.streamSize = uint64(len(e.data))
		e.startSect = allocChain(e.data)
	}

	miniStreamStart := allocChain(miniStream)

	miniFATBytes := make([]byte, len(miniFAT)*4)
	for i, v := range miniFAT {
		binary.LittleEndian.PutUint32(miniFATBytes[i*4:], v)
	}
	miniFATStart := allocChain(miniFATBytes)
	miniFATSectors := uint32((len(miniFATBytes) + cfbSectorSize - 1) / cfbSectorSize)

	// The directory: pad to a whole number of sectors.
	dirCount := len(flat)
	if r := dirCount % cfbDirsPerSector; r != 0 {
		dirCount += cfbDirsPerSector - r
	}
	dirBytes := make([]byte, dirCount*directoryEntrySize)
	for i, e := range flat {
		var startSect uint32 = cfbEndOfChain
		var size uint64
		if e.isStream {
			startSect, size = e.startSect, e.streamSize
		}
		objectType := uint8(1) // storage
		if e.isStream {
			objectType = 2
		}
		if i == 0 {
			objectType = 5 // root
			startSect, size = miniStreamStart, uint64(len(miniStream))
		}
		var childID uint32 = cfbNoStream
		if len(e.children) > 0 {
			childID = uint32(e.children[0].index)
		}
		// Siblings start unlinked and are chained below, once every index is
		// known.
		writeDirEntry(dirBytes[i*directoryEntrySize:], e.name, objectType,
			cfbNoStream, cfbNoStream, childID, startSect, size)
	}
	// Chain each entry's children as a right-only list. The reader walks left,
	// appends, then walks right, so this preserves insertion order without
	// needing a balanced tree.
	for _, e := range flat {
		for i := 0; i+1 < len(e.children); i++ {
			off := e.children[i].index*directoryEntrySize + dirOffRightSibling
			binary.LittleEndian.PutUint32(dirBytes[off:], uint32(e.children[i+1].index))
		}
	}
	// Padding entries stay zeroed: ObjectType 0 makes the reader skip them.
	dirStart := allocChain(dirBytes)

	// The FAT covers every sector, including its own. Its size depends on the
	// total sector count, which depends on its size, so iterate to a fixed
	// point.
	entriesPerSector := cfbSectorSize / 4
	fatSectorCount := 1
	for {
		total := len(sectors) + fatSectorCount
		needed := (total + entriesPerSector - 1) / entriesPerSector
		if needed <= fatSectorCount {
			break
		}
		fatSectorCount = needed
	}

	fatSectorNums := make([]uint32, fatSectorCount)
	for i := 0; i < fatSectorCount; i++ {
		fatSectorNums[i] = uint32(len(sectors) + i)
		fat = append(fat, cfbFATSector)
	}

	// Serialise the FAT, padding unused entries as free.
	fatBytes := make([]byte, fatSectorCount*cfbSectorSize)
	for i := range fatBytes {
		fatBytes[i] = 0xFF
	}
	for i, v := range fat {
		binary.LittleEndian.PutUint32(fatBytes[i*4:], v)
	}
	for i := 0; i < fatSectorCount; i++ {
		sectors = append(sectors, fatBytes[i*cfbSectorSize:(i+1)*cfbSectorSize])
	}

	// Header.
	header := make([]byte, cfbSectorSize)
	for i := 76; i < cfbSectorSize; i++ {
		header[i] = 0xFF // unused DIFAT slots
	}
	binary.LittleEndian.PutUint64(header[0:], cfbSignature)
	binary.LittleEndian.PutUint16(header[24:], 0x003E) // minor version
	binary.LittleEndian.PutUint16(header[26:], 3)      // major version
	binary.LittleEndian.PutUint16(header[28:], 0xFFFE) // little-endian
	binary.LittleEndian.PutUint16(header[30:], 9)      // 512-byte sectors
	binary.LittleEndian.PutUint16(header[32:], 6)      // 64-byte mini sectors
	binary.LittleEndian.PutUint32(header[40:], uint32(len(sectors)))
	binary.LittleEndian.PutUint32(header[44:], uint32(fatSectorCount))
	binary.LittleEndian.PutUint32(header[48:], dirStart)
	binary.LittleEndian.PutUint32(header[56:], miniStreamCutoff)
	binary.LittleEndian.PutUint32(header[60:], miniFATStart)
	binary.LittleEndian.PutUint32(header[64:], miniFATSectors)
	binary.LittleEndian.PutUint32(header[68:], cfbEndOfChain) // no DIFAT sectors
	binary.LittleEndian.PutUint32(header[72:], 0)
	for i, s := range fatSectorNums {
		if i >= difatInHeader {
			panic("fixture needs a DIFAT sector; keep test messages smaller")
		}
		binary.LittleEndian.PutUint32(header[76+i*4:], s)
	}

	out := make([]byte, 0, cfbSectorSize*(1+len(sectors)))
	out = append(out, header...)
	for _, s := range sectors {
		out = append(out, s...)
	}
	return out
}

// Byte offsets inside a 128-byte directory entry, matching the field order of
// the directoryEntry struct the reader decodes.
const (
	dirOffNameLen      = 64  // uint16
	dirOffObjectType   = 66  // uint8
	dirOffColorFlag    = 67  // uint8
	dirOffLeftSibling  = 68  // uint32
	dirOffRightSibling = 72  // uint32
	dirOffChild        = 76  // uint32
	dirOffStartSector  = 116 // uint32
	dirOffStreamSize   = 120 // uint64
)

// writeDirEntry lays out one 128-byte directory entry.
func writeDirEntry(dst []byte, name string, objectType uint8,
	left, right, child, startSect uint32, size uint64) {

	nameUTF16 := utf16LE(name)
	copy(dst[0:64], nameUTF16)
	// NameLen counts the trailing NUL, which utf16LE does not emit.
	binary.LittleEndian.PutUint16(dst[dirOffNameLen:], uint16(len(nameUTF16)+2))
	dst[dirOffObjectType] = objectType
	dst[dirOffColorFlag] = 1 // black
	binary.LittleEndian.PutUint32(dst[dirOffLeftSibling:], left)
	binary.LittleEndian.PutUint32(dst[dirOffRightSibling:], right)
	binary.LittleEndian.PutUint32(dst[dirOffChild:], child)
	binary.LittleEndian.PutUint32(dst[dirOffStartSector:], startSect)
	binary.LittleEndian.PutUint64(dst[dirOffStreamSize:], size)
}
