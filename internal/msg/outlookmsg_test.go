package msg

import (
	"encoding/binary"
	"sort"
	"strconv"
	"strings"
)

// msgFixed holds fixed-length property values for outlookMSG, by storage.
type msgFixed map[*cfbEntry]map[uint32]uint64

const (
	tagAttachNum = 0x0E210003
	tagRowID     = 0x30000003
)

// outlookMSG completes the message in root so that Outlook 2010 opens it
// ([MS-OXMSG]): it adds the named-property storage and, in the message and in
// each attachment, recipient and attached message, a property stream listing
// every __substg1.0_ stream and storage there plus the values in fixed.
//
// The message is ANSI when some string in the tree is PT_STRING8, Unicode
// (STORE_UNICODE_OK) otherwise; a message without PR_MESSAGE_CLASS gets
// IPM.Note in that kind, one with attachments PR_HASATTACH. Attachments get
// PR_ATTACH_NUM and PR_ATTACH_METHOD, recipients PR_ROWID and
// PR_RECIPIENT_TYPE To. Values in fixed take precedence. A message header's
// next IDs follow the highest numbers used.
//
// Call it once the tree is complete: what is added afterwards is not listed,
// which is how a test makes a stream Outlook does not see.
func outlookMSG(root *cfbEntry, fixed msgFixed) *cfbEntry {
	if fixed == nil {
		fixed = msgFixed{}
	}
	nameid := root.addStorage(msNameIDStorage)
	for _, s := range []string{"00020102", "00030102", "00040102"} {
		nameid.addStream("__substg1.0_"+s, nil)
	}
	finishMessage(root, msTopLevelHeaderSize, fixed, !hasANSIString(root))
	return root
}

func finishMessage(e *cfbEntry, headerSize int, fixed msgFixed, unicode bool) {
	if !hasChildPrefix(e, "__substg1.0_001A") {
		if unicode {
			e.addStream(substgName(pidTagMessageClass, propTypeString), utf16LE("IPM.Note"))
		} else {
			e.addStream(substgName(pidTagMessageClass, propTypeString8), []byte("IPM.Note"))
		}
	}
	if unicode {
		setDefault(fixed, e, msTagStoreSupportMask, msStoreUnicodeOK)
	}
	var nextRecip, nextAttach uint32
	for _, c := range e.children {
		switch {
		case strings.HasPrefix(c.name, msAttachPrefix):
			n := storageNumber(c.name, msAttachPrefix)
			nextAttach = max(nextAttach, n+1)
			setDefault(fixed, e, msTagHasAttach, 1)
			method := uint64(1) // ATTACH_BY_VALUE
			if obj := findChild(c, substgName(0x3701, 0x000D)); obj != nil {
				method = 5 // ATTACH_EMBEDDED_MSG
				finishMessage(obj, msEmbeddedHeaderSize, fixed, unicode)
			}
			setDefault(fixed, c, tagAttachNum, uint64(n))
			setDefault(fixed, c, msTagAttachMethod, method)
			c.addStream(msPropertiesStream, propertyStream(c, msSubObjectHeaderSize, 0, 0, fixed[c]))
		case strings.HasPrefix(c.name, msRecipPrefix):
			n := storageNumber(c.name, msRecipPrefix)
			nextRecip = max(nextRecip, n+1)
			setDefault(fixed, c, tagRowID, uint64(n))
			setDefault(fixed, c, msTagRecipientType, 1) // MAPI_TO
			c.addStream(msPropertiesStream, propertyStream(c, msSubObjectHeaderSize, 0, 0, fixed[c]))
		}
	}
	e.addStream(msPropertiesStream, propertyStream(e, headerSize, nextRecip, nextAttach, fixed[e]))
}

// propertyStream lists the properties of e: the header, then a 16-byte entry
// per property, in tag order. A variable-length entry holds the size
// [MS-OXMSG] gives it: the stream length, plus the terminator of a string;
// an object entry holds 0xFFFFFFFF.
func propertyStream(e *cfbEntry, headerSize int, nextRecip, nextAttach uint32, values map[uint32]uint64) []byte {
	entries := make(map[uint32]uint64, len(values))
	for tag, v := range values {
		entries[tag] = v
	}
	for _, c := range e.children {
		if !strings.HasPrefix(c.name, "__substg1.0_") || len(c.name) != len("__substg1.0_")+8 {
			continue
		}
		tag64, err := strconv.ParseUint(c.name[len("__substg1.0_"):], 16, 32)
		if err != nil {
			continue
		}
		tag := uint32(tag64)
		switch size := uint64(len(c.data)); tag & 0xFFFF {
		case 0x000D:
			entries[tag] = 0xFFFFFFFF
		case propTypeString:
			entries[tag] = size + 2
		case propTypeString8:
			entries[tag] = size + 1
		default:
			entries[tag] = size
		}
	}

	out := make([]byte, headerSize)
	if headerSize >= msEmbeddedHeaderSize {
		binary.LittleEndian.PutUint32(out[8:], nextRecip)
		binary.LittleEndian.PutUint32(out[12:], nextAttach)
		binary.LittleEndian.PutUint32(out[16:], nextRecip)
		binary.LittleEndian.PutUint32(out[20:], nextAttach)
	}
	tags := make([]uint32, 0, len(entries))
	for tag := range entries {
		tags = append(tags, tag)
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i] < tags[j] })
	for _, tag := range tags {
		entry := make([]byte, msPropertyEntrySize)
		binary.LittleEndian.PutUint32(entry[0:], tag)
		binary.LittleEndian.PutUint32(entry[4:], 0x6) // PROPATTR_READABLE | PROPATTR_WRITABLE
		binary.LittleEndian.PutUint64(entry[8:], entries[tag])
		out = append(out, entry...)
	}
	return out
}

func setDefault(fixed msgFixed, e *cfbEntry, tag uint32, v uint64) {
	if fixed[e] == nil {
		fixed[e] = map[uint32]uint64{}
	}
	if _, ok := fixed[e][tag]; !ok {
		fixed[e][tag] = v
	}
}

func storageNumber(name, prefix string) uint32 {
	n, _ := strconv.ParseUint(name[len(prefix):], 16, 32)
	return uint32(n)
}

// hasANSIString reports whether a PT_STRING8 stream is anywhere under e.
func hasANSIString(e *cfbEntry) bool {
	if strings.HasPrefix(e.name, "__substg1.0_") && (strings.HasSuffix(e.name, "001E") || strings.HasSuffix(e.name, "101E")) {
		return true
	}
	for _, c := range e.children {
		if hasANSIString(c) {
			return true
		}
	}
	return false
}

func hasChildPrefix(e *cfbEntry, prefix string) bool {
	for _, c := range e.children {
		if strings.HasPrefix(c.name, prefix) {
			return true
		}
	}
	return false
}

func findChild(e *cfbEntry, name string) *cfbEntry {
	for _, c := range e.children {
		if c.name == name {
			return c
		}
	}
	return nil
}

// removeChild drops the child called name from e.
func (e *cfbEntry) removeChild(name string) {
	for i, c := range e.children {
		if c.name == name {
			e.children = append(e.children[:i], e.children[i+1:]...)
			return
		}
	}
}
