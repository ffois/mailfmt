package msg

import (
	"bytes"
	"encoding/binary"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ffois/mailfmt/internal/email"
)

// The tests in this file feed Read compound files that no writer would ever
// produce: sizes that cannot fit, chains that point at themselves, sector
// shifts outside the format. They are all things a hostile .msg can say, and
// mailfmt.ParseFile reaches this reader with bytes nobody has vetted. Each test
// pins one guard; without it the failure is a panic, a hang, or a stack
// overflow in the calling process, not a parse error.

// craftCFB assembles a 3-sector compound file: the header, one FAT sector, one
// directory sector. It writes whatever it is given, valid or not - that is the
// point.
func craftCFB(h cfbHeader, fat []uint32, dirs []directoryEntry) []byte {
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, &h)
	for buf.Len() < 512 {
		buf.WriteByte(0)
	}

	var fatBuf bytes.Buffer
	for _, e := range fat {
		_ = binary.Write(&fatBuf, binary.LittleEndian, e)
	}
	for fatBuf.Len() < 512 {
		fatBuf.WriteByte(0xFF)
	}
	buf.Write(fatBuf.Bytes()[:512])

	var dirBuf bytes.Buffer
	for _, d := range dirs {
		_ = binary.Write(&dirBuf, binary.LittleEndian, &d)
	}
	for dirBuf.Len() < 512 {
		dirBuf.WriteByte(0)
	}
	buf.Write(dirBuf.Bytes()[:512])

	return buf.Bytes()
}

func craftedHeader() cfbHeader {
	return cfbHeader{
		Signature:            cfbSignature,
		SectorShift:          9,
		MiniSectorShift:      6,
		FATSectors:           1,
		FirstDirectorySector: 1,
		FirstMiniFATSector:   cfbEndOfChain,
		FirstDIFATSector:     cfbEndOfChain,
		MiniStreamCutoff:     miniStreamCutoff,
	}
}

func craftedRoot(size uint64) directoryEntry {
	e := directoryEntry{
		NameLen:           12,
		ObjectType:        5,
		LeftSiblingID:     noStream,
		RightSiblingID:    noStream,
		ChildID:           noStream,
		StartingSectorLoc: cfbEndOfChain,
		StreamSize:        size,
	}
	copy(e.Name[:], []byte{'R', 0, 'o', 0, 'o', 0, 't', 0, 0, 0})
	return e
}

// readCrafted calls Read and MS_Read on hostile bytes and insists they come
// back - promptly,
// and without panicking. A recovered panic is reported as a normal failure so
// one broken guard does not abort the whole test binary. A stack overflow
// cannot be recovered and will still take the binary down, which is itself the
// signal that the recursion guard is gone.
func readCrafted(t *testing.T, data []byte) error {
	t.Helper()

	type result struct {
		err   error
		panic any
	}
	done := make(chan result, 1)

	go func() {
		var res result
		defer func() {
			res.panic = recover()
			done <- res
		}()
		_, res.err = Read(bytes.NewReader(data))
		// MS_Read walks the same tree, then more of it: it must come back too.
		_, _ = MS_Read(bytes.NewReader(data))
	}()

	select {
	case res := <-done:
		if res.panic != nil {
			t.Fatalf("Read panicked on crafted input: %v", res.panic)
		}
		return res.err
	case <-time.After(5 * time.Second):
		t.Fatal("Read did not return within 5s on crafted input: the sector chain loops")
		return nil
	}
}

// A root directory entry declaring a stream far larger than any file can hold
// must not be believed. The size flows into the mini-stream allocation; taken
// at face value, 1<<62 panics with "makeslice: cap out of range" and a merely
// large value exhausts memory before a byte is read.
func TestCraftedRootStreamSizeIsNotAllocated(t *testing.T) {
	data := craftCFB(
		craftedHeader(),
		[]uint32{cfbFATSector, cfbEndOfChain},
		[]directoryEntry{craftedRoot(1 << 62)},
	)
	readCrafted(t, data)
}

// A stream entry can lie about its size just as the root can, and it reaches a
// different allocation - readStream sends anything under the 4096-byte cutoff
// to the mini path and everything else to the regular one. Both are checked.
func TestCraftedStreamSizesAreNotAllocated(t *testing.T) {
	for _, size := range []uint64{1 << 62, miniStreamCutoff - 1 + (1 << 40)} {
		root := craftedRoot(0)
		root.ChildID = 1

		child := directoryEntry{
			NameLen:           10,
			ObjectType:        2,
			LeftSiblingID:     noStream,
			RightSiblingID:    noStream,
			ChildID:           noStream,
			StartingSectorLoc: 2,
			StreamSize:        size,
		}
		copy(child.Name[:], utf16LE("big"))

		data := craftCFB(
			craftedHeader(),
			[]uint32{cfbFATSector, cfbEndOfChain, cfbEndOfChain},
			[]directoryEntry{root, child},
		)
		readCrafted(t, data)
	}
}

// A FAT entry that points the directory chain back at its own sector makes the
// walk repeat forever. A hang is worse than a panic: nothing recovers from it,
// and one message stalls the process that parsed it.
func TestCraftedFATCycleTerminates(t *testing.T) {
	data := craftCFB(
		craftedHeader(),
		[]uint32{cfbFATSector, 1}, // sector 1 chains to sector 1
		[]directoryEntry{craftedRoot(0)},
	)
	readCrafted(t, data)
}

// The mini-FAT chain is walked by separate code from the directory chain and
// needs its own guard.
func TestCraftedMiniFATCycleTerminates(t *testing.T) {
	h := craftedHeader()
	h.FirstMiniFATSector = 2

	data := craftCFB(
		h,
		[]uint32{cfbFATSector, cfbEndOfChain, 2}, // sector 2 chains to sector 2
		[]directoryEntry{craftedRoot(0)},
	)
	readCrafted(t, data)
}

// A directory entry naming itself as its own left sibling used to send the
// sibling walk into unbounded recursion. Recursion depth is not something a
// recover can save you from, so the walk has to refuse to revisit a node.
func TestCraftedSiblingLoopTerminates(t *testing.T) {
	root := craftedRoot(0)
	root.ChildID = 1

	child := directoryEntry{
		NameLen:        6,
		ObjectType:     2,
		LeftSiblingID:  1, // itself
		RightSiblingID: noStream,
		ChildID:        noStream,
	}
	copy(child.Name[:], utf16LE("ab"))

	data := craftCFB(
		craftedHeader(),
		[]uint32{cfbFATSector, cfbEndOfChain},
		[]directoryEntry{root, child},
	)
	readCrafted(t, data)
}

// Two entries naming each other close the same loop without either one naming
// itself.
func TestCraftedSiblingPairTerminates(t *testing.T) {
	root := craftedRoot(0)
	root.ChildID = 1

	left := directoryEntry{
		NameLen:        6,
		ObjectType:     2,
		LeftSiblingID:  2,
		RightSiblingID: noStream,
		ChildID:        noStream,
	}
	copy(left.Name[:], utf16LE("ab"))

	right := directoryEntry{
		NameLen:        6,
		ObjectType:     2,
		LeftSiblingID:  1,
		RightSiblingID: noStream,
		ChildID:        noStream,
	}
	copy(right.Name[:], utf16LE("cd"))

	data := craftCFB(
		craftedHeader(),
		[]uint32{cfbFATSector, cfbEndOfChain},
		[]directoryEntry{root, left, right},
	)
	readCrafted(t, data)
}

// A directory entry's name length is a file value too: one beyond the 64-byte
// name field, or an odd one, must not take the name read out of bounds.
// Found by FuzzMSRead.
func TestCraftedNameLengthIsBounded(t *testing.T) {
	for _, nameLen := range []uint16{0xFFFF, 66, 3} {
		root := craftedRoot(0)
		root.NameLen = nameLen
		data := craftCFB(craftedHeader(), []uint32{cfbFATSector, cfbEndOfChain}, []directoryEntry{root})
		_ = readCrafted(t, data)
	}
}

// [MS-CFB] permits sector shifts of 9 and 12 and nothing else. Rejecting the
// rest at the header is cheap and closes a class of arithmetic faults at once:
// a shift under 2 gives a sector smaller than a FAT entry, and readFAT then
// indexes a slice with a negative bound.
func TestCraftedSectorShiftRejected(t *testing.T) {
	for _, shift := range []uint16{0, 1, 8, 13, 40} {
		h := craftedHeader()
		h.SectorShift = shift
		h.DIFATSectors = 1
		h.FirstDIFATSector = 0

		data := craftCFB(h, []uint32{cfbFATSector, cfbEndOfChain}, []directoryEntry{craftedRoot(0)})

		err := readCrafted(t, data)
		if err == nil || !strings.Contains(err.Error(), "sector shift") {
			t.Errorf("sector shift %d: got err %v, want a rejection naming the sector shift", shift, err)
		}
	}
}

// The mini sector size is fixed at 64 bytes by the format. An inflated shift
// overflows the offset arithmetic that indexes the mini stream.
func TestCraftedMiniSectorShiftRejected(t *testing.T) {
	h := craftedHeader()
	h.MiniSectorShift = 40

	data := craftCFB(h, []uint32{cfbFATSector, cfbEndOfChain}, []directoryEntry{craftedRoot(0)})

	err := readCrafted(t, data)
	if err == nil || !strings.Contains(err.Error(), "mini sector shift") {
		t.Errorf("got err %v, want a rejection naming the mini sector shift", err)
	}
}

// nestMultipart wraps a single named attachment in depth layers of
// multipart/mixed, the way a sender nests parts inside a MSG attachment.
func nestMultipart(depth int) []byte {
	body := "Content-Type: application/octet-stream\r\n" +
		"Content-Disposition: attachment; filename=\"deep.bin\"\r\n\r\n" +
		"payload\r\n"

	for i := range depth {
		boundary := "bound" + strconv.Itoa(i)
		body = "Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n\r\n" +
			"--" + boundary + "\r\n" + body + "--" + boundary + "--\r\n"
	}
	return []byte(body)
}

// A multipart attachment can nest inside another multipart attachment as deeply
// as the sender cares to write it out, and parseMIMEParts recurses on that
// nesting. Ordinary nesting is followed; past the bound the walk stops instead
// of descending on the sender's word.
func TestDeepMIMENestingIsBounded(t *testing.T) {
	if got := extractMIMEAttachments(nestMultipart(3)); len(got) != 1 {
		t.Fatalf("3 levels: got %d attachments, want the one at the bottom", len(got))
	}

	deep := nestMultipart(maxMIMEDepth + 5)

	done := make(chan []email.Attachment, 1)
	go func() { done <- extractMIMEAttachments(deep) }()

	select {
	case got := <-done:
		if len(got) != 0 {
			t.Errorf("%d levels: got %d attachments, want none - the walk should stop at maxMIMEDepth",
				maxMIMEDepth+5, len(got))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("extractMIMEAttachments did not return within 5s on deeply nested input")
	}
}
