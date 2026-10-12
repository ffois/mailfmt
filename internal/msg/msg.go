package msg

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"os"
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/ffois/mailfmt/internal/email"
)

const (
	cfbSignature       = 0xE11AB1A1E011CFD0
	miniStreamCutoff   = 4096
	maxRegularSector   = 0xFFFFFFFA
	noStream           = 0xFFFFFFFF
	difatInHeader      = 109
	directoryEntrySize = 128

	// maxPrealloc caps how much memory a declared stream size may reserve up
	// front. Every size this reader acts on comes out of the file itself and is
	// therefore attacker-controlled: a directory entry claiming a StreamSize of
	// 1<<62 makes an unclamped make() panic with "makeslice: cap out of range",
	// and a merely large one exhausts memory before a single byte is read.
	// Streams bigger than the cap still read correctly - append grows the slice
	// as sectors arrive, which the real file length bounds.
	maxPrealloc = 8 << 20

	// maxMIMEDepth bounds the nesting of multipart parts inside a MSG
	// attachment. parseMIMEParts recurses into nested multiparts and into
	// message/rfc822 payloads, both of which an attacker can nest arbitrarily.
	maxMIMEDepth = 20
)

// prealloc returns an empty slice whose capacity is size, clamped to
// maxPrealloc. See that constant for why the clamp is not optional.
func prealloc(size uint64) []byte {
	if size > maxPrealloc {
		size = maxPrealloc
	}
	return make([]byte, 0, size)
}

// chainWalker follows a FAT or mini-FAT sector chain, refusing to visit the
// same sector twice. A crafted file can point a chain back at itself; without
// this guard the walk never terminates and one malformed message hangs the
// calling process. A cycle is not recoverable the way a panic is, so it has to
// be prevented rather than caught.
type chainWalker map[uint32]bool

// visit reports whether sector is a new link in the chain. It returns false
// once the chain either ends, runs past the last real sector, or loops.
func (w chainWalker) visit(sector uint32) bool {
	if sector >= maxRegularSector || w[sector] {
		return false
	}
	w[sector] = true
	return true
}

const (
	pidTagSubject              = 0x0037
	pidTagConversationTopic    = 0x0070
	pidTagMessageClass         = 0x001A
	pidTagBody                 = 0x1000
	pidTagRtfCompressed        = 0x1009
	pidTagBodyHTML             = 0x1013
	pidTagSenderName           = 0x0C1A
	pidTagSenderEmailAddress   = 0x0C1F
	pidTagSentRepresentingName = 0x0042
	pidTagSentRepresentingAddr = 0x0065
	pidTagDisplayTo            = 0x0E04
	pidTagDisplayCc            = 0x0E03
	pidTagDisplayBcc           = 0x0E02
	pidTagAttachFilename       = 0x3704
	pidTagAttachLongFilename   = 0x3707
	pidTagAttachData           = 0x3701
	pidTagAttachMimeTag        = 0x370E
	pidTagAttachContentId      = 0x3712
	propTypeString8            = 0x001E
	propTypeString             = 0x001F
	propTypeBinary             = 0x0102
)

type cfbHeader struct {
	Signature            uint64
	CLSID                [16]byte
	MinorVersion         uint16
	MajorVersion         uint16
	ByteOrder            uint16
	SectorShift          uint16
	MiniSectorShift      uint16
	Reserved1            [6]byte
	TotalSectors         uint32
	FATSectors           uint32
	FirstDirectorySector uint32
	TransactionSignature uint32
	MiniStreamCutoff     uint32
	FirstMiniFATSector   uint32
	MiniFATSectors       uint32
	FirstDIFATSector     uint32
	DIFATSectors         uint32
	DIFAT                [109]uint32
}

type directoryEntry struct {
	Name              [64]byte
	NameLen           uint16
	ObjectType        uint8
	ColorFlag         uint8
	LeftSiblingID     uint32
	RightSiblingID    uint32
	ChildID           uint32
	CLSID             [16]byte
	StateBits         uint32
	CreationTime      uint64
	ModifiedTime      uint64
	StartingSectorLoc uint32
	StreamSize        uint64
}

type dirNode struct {
	Index    int
	Entry    directoryEntry
	Name     string
	Children []*dirNode
}

type cfbReader struct {
	reader     io.ReaderAt
	header     cfbHeader
	sectorSize int
	fat        []uint32
	miniFAT    []uint32
	dirEntries []directoryEntry
	root       *dirNode
	nodesByIdx map[int]*dirNode
	miniStream []byte
}

func ReadFile(path string) (*email.Data, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func(f *os.File) {
		_ = f.Close()
	}(f)
	_, err = f.Stat()
	if err != nil {
		return nil, err
	}
	return Read(f)
}

func Read(r io.ReaderAt) (*email.Data, error) {
	cfb, err := newCFBReader(r)
	if err != nil {
		return nil, err
	}
	return parseMessage(cfb)
}

func newCFBReader(r io.ReaderAt) (*cfbReader, error) {
	cfb := &cfbReader{reader: r, nodesByIdx: make(map[int]*dirNode)}

	headerData := make([]byte, 512)
	if _, err := r.ReadAt(headerData, 0); err != nil {
		return nil, err
	}

	buf := bytes.NewReader(headerData)
	if err := binary.Read(buf, binary.LittleEndian, &cfb.header); err != nil {
		return nil, err
	}

	if cfb.header.Signature != cfbSignature {
		return nil, errors.New("invalid MSG file")
	}

	// [MS-CFB] allows exactly two sector sizes, and exactly one mini sector
	// size. Rejecting everything else here is worth more than the validation
	// itself: a SectorShift below 2 gives a sector smaller than a FAT entry,
	// which makes readFAT compute entriesPerSector = -1 and panic on a negative
	// slice index, and an inflated MiniSectorShift overflows the offset
	// arithmetic in readMiniStreamData. One check at the header closes both.
	if cfb.header.SectorShift != 9 && cfb.header.SectorShift != 12 {
		return nil, fmt.Errorf("msg: invalid sector shift %d", cfb.header.SectorShift)
	}
	if cfb.header.MiniSectorShift != 6 {
		return nil, fmt.Errorf("msg: invalid mini sector shift %d", cfb.header.MiniSectorShift)
	}

	cfb.sectorSize = 1 << cfb.header.SectorShift

	if err := cfb.readFAT(); err != nil {
		return nil, err
	}
	if err := cfb.readDirectories(); err != nil {
		return nil, err
	}
	cfb.buildTree()

	if cfb.header.FirstMiniFATSector < maxRegularSector {
		_ = cfb.readMiniFAT()
	}
	if len(cfb.dirEntries) > 0 && cfb.dirEntries[0].StreamSize > 0 {
		_ = cfb.readMiniStream()
	}

	return cfb, nil
}

func (cfb *cfbReader) sectorOffset(sector uint32) int64 {
	return int64(sector+1) * int64(cfb.sectorSize)
}

func (cfb *cfbReader) readSector(sector uint32) ([]byte, error) {
	data := make([]byte, cfb.sectorSize)
	_, err := cfb.reader.ReadAt(data, cfb.sectorOffset(sector))
	return data, err
}

func (cfb *cfbReader) readFAT() error {
	var fatSectors []uint32
	for i := 0; i < difatInHeader && i < int(cfb.header.FATSectors); i++ {
		if cfb.header.DIFAT[i] < maxRegularSector {
			fatSectors = append(fatSectors, cfb.header.DIFAT[i])
		}
	}

	if cfb.header.DIFATSectors > 0 && cfb.header.FirstDIFATSector < maxRegularSector {
		difatSector := cfb.header.FirstDIFATSector
		chain := chainWalker{}
		for i := uint32(0); i < cfb.header.DIFATSectors && chain.visit(difatSector); i++ {
			data, err := cfb.readSector(difatSector)
			if err != nil {
				return err
			}
			entriesPerSector := cfb.sectorSize/4 - 1
			for j := 0; j < entriesPerSector && len(fatSectors) < int(cfb.header.FATSectors); j++ {
				sector := binary.LittleEndian.Uint32(data[j*4:])
				if sector < maxRegularSector {
					fatSectors = append(fatSectors, sector)
				}
			}
			difatSector = binary.LittleEndian.Uint32(data[entriesPerSector*4:])
		}
	}

	entriesPerSector := cfb.sectorSize / 4
	cfb.fat = make([]uint32, 0, len(fatSectors)*entriesPerSector)
	for _, sector := range fatSectors {
		data, err := cfb.readSector(sector)
		if err != nil {
			return err
		}
		for i := 0; i < entriesPerSector; i++ {
			cfb.fat = append(cfb.fat, binary.LittleEndian.Uint32(data[i*4:]))
		}
	}
	return nil
}

func (cfb *cfbReader) readDirectories() error {
	entriesPerSector := cfb.sectorSize / directoryEntrySize
	sector := cfb.header.FirstDirectorySector
	chain := chainWalker{}

	for chain.visit(sector) {
		data, err := cfb.readSector(sector)
		if err != nil {
			return err
		}
		for i := 0; i < entriesPerSector; i++ {
			var entry directoryEntry
			buf := bytes.NewReader(data[i*directoryEntrySize:])
			if err := binary.Read(buf, binary.LittleEndian, &entry); err != nil {
				return err
			}
			cfb.dirEntries = append(cfb.dirEntries, entry)
		}
		if int(sector) >= len(cfb.fat) {
			break
		}
		sector = cfb.fat[sector]
	}
	return nil
}

func (cfb *cfbReader) buildTree() {
	for i := range cfb.dirEntries {
		entry := &cfb.dirEntries[i]
		if entry.ObjectType == 0 {
			continue
		}
		node := &dirNode{Index: i, Entry: *entry, Name: getDirName(entry)}
		cfb.nodesByIdx[i] = node
	}

	if node, ok := cfb.nodesByIdx[0]; ok {
		cfb.root = node
	}

	for _, node := range cfb.nodesByIdx {
		if node.Entry.ChildID != noStream {
			cfb.collectChildren(node, int(node.Entry.ChildID))
		}
	}
}

// collectChildren appends every node of a directory entry's red-black sibling
// tree to parent, in the in-order sequence the format defines.
//
// The walk is iterative and keeps a visited set on purpose. An entry in a
// crafted file can name itself as its own left sibling, or two entries can
// point at each other; recursion then descends until the goroutine stack is
// exhausted, which no recover can catch.
func (cfb *cfbReader) collectChildren(parent *dirNode, startIdx int) {
	type frame struct {
		idx  int
		emit bool // the node's left subtree is done; append it now
	}

	visited := make(map[int]bool, len(cfb.dirEntries))
	stack := []frame{{idx: startIdx}}

	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if f.idx < 0 || f.idx >= len(cfb.dirEntries) || uint32(f.idx) == noStream {
			continue
		}
		child, ok := cfb.nodesByIdx[f.idx]
		if !ok {
			continue
		}
		if f.emit {
			parent.Children = append(parent.Children, child)
			continue
		}
		if visited[f.idx] {
			continue
		}
		visited[f.idx] = true

		// Pushed in reverse, so they pop as left, self, right.
		if child.Entry.RightSiblingID != noStream {
			stack = append(stack, frame{idx: int(child.Entry.RightSiblingID)})
		}
		stack = append(stack, frame{idx: f.idx, emit: true})
		if child.Entry.LeftSiblingID != noStream {
			stack = append(stack, frame{idx: int(child.Entry.LeftSiblingID)})
		}
	}
}

func (cfb *cfbReader) readMiniFAT() error {
	entriesPerSector := cfb.sectorSize / 4
	sector := cfb.header.FirstMiniFATSector
	chain := chainWalker{}
	for chain.visit(sector) {
		data, err := cfb.readSector(sector)
		if err != nil {
			return err
		}
		for i := 0; i < entriesPerSector; i++ {
			cfb.miniFAT = append(cfb.miniFAT, binary.LittleEndian.Uint32(data[i*4:]))
		}
		if int(sector) >= len(cfb.fat) {
			break
		}
		sector = cfb.fat[sector]
	}
	return nil
}

func (cfb *cfbReader) readMiniStream() error {
	root := cfb.dirEntries[0]
	cfb.miniStream = prealloc(root.StreamSize)
	sector := root.StartingSectorLoc
	remaining := int64(root.StreamSize)
	chain := chainWalker{}

	for remaining > 0 && chain.visit(sector) {
		data, err := cfb.readSector(sector)
		if err != nil {
			return err
		}
		toRead := int64(cfb.sectorSize)
		if toRead > remaining {
			toRead = remaining
		}
		cfb.miniStream = append(cfb.miniStream, data[:toRead]...)
		remaining -= toRead
		if int(sector) >= len(cfb.fat) {
			break
		}
		sector = cfb.fat[sector]
	}
	return nil
}

func (cfb *cfbReader) readStream(entry *directoryEntry) ([]byte, error) {
	if entry.StreamSize == 0 {
		return nil, nil
	}
	if entry.StreamSize < miniStreamCutoff {
		return cfb.readMiniStreamData(entry)
	}
	return cfb.readRegularStream(entry)
}

func (cfb *cfbReader) readMiniStreamData(entry *directoryEntry) ([]byte, error) {
	miniSectorSize := 1 << cfb.header.MiniSectorShift
	data := prealloc(entry.StreamSize)
	sector := entry.StartingSectorLoc
	remaining := int64(entry.StreamSize)
	chain := chainWalker{}

	for remaining > 0 && chain.visit(sector) {
		offset := int(sector) * miniSectorSize
		if offset >= len(cfb.miniStream) {
			break
		}
		toRead := miniSectorSize
		if int64(toRead) > remaining {
			toRead = int(remaining)
		}
		end := offset + toRead
		if end > len(cfb.miniStream) {
			end = len(cfb.miniStream)
		}
		data = append(data, cfb.miniStream[offset:end]...)
		remaining -= int64(toRead)
		if int(sector) >= len(cfb.miniFAT) {
			break
		}
		sector = cfb.miniFAT[sector]
	}
	return data, nil
}

func (cfb *cfbReader) readRegularStream(entry *directoryEntry) ([]byte, error) {
	data := prealloc(entry.StreamSize)
	sector := entry.StartingSectorLoc
	remaining := int64(entry.StreamSize)
	chain := chainWalker{}

	for remaining > 0 && chain.visit(sector) {
		sectorData, err := cfb.readSector(sector)
		if err != nil {
			return nil, err
		}
		toRead := int64(cfb.sectorSize)
		if toRead > remaining {
			toRead = remaining
		}
		data = append(data, sectorData[:toRead]...)
		remaining -= toRead
		if int(sector) >= len(cfb.fat) {
			break
		}
		sector = cfb.fat[sector]
	}
	return data, nil
}

func (cfb *cfbReader) readNodeStream(node *dirNode) ([]byte, error) {
	return cfb.readStream(&node.Entry)
}

func getDirName(entry *directoryEntry) string {
	if entry.NameLen <= 2 {
		return ""
	}
	nameBytes := entry.Name[:entry.NameLen-2]
	runes := make([]rune, 0, len(nameBytes)/2)
	for i := 0; i < len(nameBytes); i += 2 {
		r := rune(binary.LittleEndian.Uint16(nameBytes[i:]))
		if r != 0 {
			runes = append(runes, r)
		}
	}
	return string(runes)
}

func parseMessage(cfb *cfbReader) (*email.Data, error) {
	if cfb.root == nil {
		return nil, errors.New("no root directory")
	}
	return buildMessage(cfb, rootProps(cfb)), nil
}

// rootProps reads the message's variable-length properties, one
// __substg1.0_ stream each, keyed by property tag.
func rootProps(cfb *cfbReader) map[uint32][]byte {
	props := make(map[uint32][]byte)

	for _, child := range cfb.root.Children {
		if child.Entry.ObjectType == 2 && strings.HasPrefix(child.Name, "__substg1.0_") {
			propID, propType := parsePropertyName(child.Name)
			if propID != 0 {
				data, _ := cfb.readNodeStream(child)
				if data != nil {
					props[(propID<<16)|uint32(propType)] = data
				}
			}
		}
	}
	return props
}

func buildMessage(cfb *cfbReader, props map[uint32][]byte) *email.Data {
	data := &email.Data{}

	data.Subject = getPropString(props, pidTagSubject)
	if data.Subject == "" {
		data.Subject = getPropString(props, pidTagConversationTopic)
	}

	data.Body = getPropString(props, pidTagBodyHTML)
	if data.Body == "" {
		data.Body = getPropBinary(props, pidTagBodyHTML)
	}
	if data.Body == "" {
		// No plain HTML body: Outlook commonly stores HTML mail as compressed
		// RTF with the original markup encapsulated. Recovering it keeps the
		// inline cid: image references that are substituted below.
		data.Body = htmlFromCompressedRTF(props)
	}
	if data.Body == "" {
		data.Body = textToHTML(getPropString(props, pidTagBody))
	}

	from := getPropString(props, pidTagSenderName)
	if from == "" {
		from = getPropString(props, pidTagSentRepresentingName)
	}
	fromEmail := getPropString(props, pidTagSenderEmailAddress)
	if fromEmail == "" {
		fromEmail = getPropString(props, pidTagSentRepresentingAddr)
	}
	if fromEmail != "" {
		data.From = fmt.Sprintf("%s <%s>", from, fromEmail)
	} else {
		data.From = from
	}

	data.To = splitRecipients(getPropString(props, pidTagDisplayTo))
	data.Cc = splitRecipients(getPropString(props, pidTagDisplayCc))
	data.Bcc = splitRecipients(getPropString(props, pidTagDisplayBcc))

	msgClass := getPropString(props, pidTagMessageClass)
	data.IsPec = strings.Contains(strings.ToLower(msgClass), "smime") ||
		strings.Contains(strings.ToLower(data.Subject), "posta certificata")

	// cid -> data URI map for inline image substitution
	cidToDataURI := make(map[string]string)

	for _, child := range cfb.root.Children {
		if strings.HasPrefix(child.Name, "__attach_version1.0_#") {
			att, cid := parseAttachment(cfb, child)
			if att != nil {
				if strings.HasPrefix(att.ContentType, "multipart/") {
					innerAtts := extractMIMEAttachments(att.Data)
					if len(innerAtts) > 0 {
						data.HasInnerEmail = true
						data.Attachments = append(data.Attachments, innerAtts...)
					}
				} else {
					if cid != "" {
						mimeType := att.ContentType
						if parts := strings.Split(mimeType, ";"); len(parts) > 0 {
							mimeType = strings.TrimSpace(parts[0])
						}
						if mimeType == "" {
							mimeType = "application/octet-stream"
						}
						b64 := base64.StdEncoding.EncodeToString(att.Data)
						cidToDataURI[cid] = fmt.Sprintf("data:%s;base64,%s", mimeType, b64)
					}
					data.Attachments = append(data.Attachments, *att)
				}
			}
		}
	}

	// Replace cid: references in the HTML body with data URIs
	for cid, dataURI := range cidToDataURI {
		re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta("cid:"+cid))
		data.Body = re.ReplaceAllLiteralString(data.Body, dataURI)
	}

	return data
}

func parsePropertyName(name string) (uint32, uint16) {
	if len(name) < 20 {
		return 0, 0
	}
	hexPart := name[12:]
	if len(hexPart) < 8 {
		return 0, 0
	}
	var propID uint32
	var propType uint16
	_, _ = fmt.Sscanf(hexPart[:4], "%04X", &propID)
	_, _ = fmt.Sscanf(hexPart[4:8], "%04X", &propType)
	return propID, propType
}

func getPropString(props map[uint32][]byte, propID uint32) string {
	if data, ok := props[(propID<<16)|propTypeString]; ok {
		return decodeUTF16(data)
	}
	if data, ok := props[(propID<<16)|propTypeString8]; ok {
		return email.DecodeString8(data)
	}
	return ""
}

func getPropBinary(props map[uint32][]byte, propID uint32) string {
	if data, ok := props[(propID<<16)|propTypeBinary]; ok {
		return email.ConvertToUTF8(string(data))
	}
	return ""
}

func textToHTML(text string) string {
	if text == "" {
		return ""
	}
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	text = strings.ReplaceAll(text, "\r\n", "<br>")
	text = strings.ReplaceAll(text, "\n", "<br>")
	return text
}

func decodeUTF16(data []byte) string {
	if len(data) < 2 {
		return ""
	}
	u16s := make([]uint16, len(data)/2)
	for i := range u16s {
		u16s[i] = binary.LittleEndian.Uint16(data[i*2:])
	}
	for len(u16s) > 0 && u16s[len(u16s)-1] == 0 {
		u16s = u16s[:len(u16s)-1]
	}
	return string(utf16.Decode(u16s))
}

func splitRecipients(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ";")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func parseAttachment(cfb *cfbReader, node *dirNode) (*email.Attachment, string) {
	att := &email.Attachment{}
	var contentId string

	for _, child := range node.Children {
		if child.Entry.ObjectType != 2 || !strings.HasPrefix(child.Name, "__substg1.0_") {
			continue
		}
		propID, propType := parsePropertyName(child.Name)
		data, _ := cfb.readNodeStream(child)
		if data == nil {
			continue
		}

		switch propID {
		case pidTagAttachLongFilename:
			if fn := decodePropertyString(data, propType); fn != "" {
				att.Filename = fn
			}
		case pidTagAttachFilename:
			if att.Filename == "" {
				att.Filename = decodePropertyString(data, propType)
			}
		case pidTagAttachMimeTag:
			att.ContentType = decodePropertyString(data, propType)
		case pidTagAttachData:
			att.Data = data
		case pidTagAttachContentId:
			contentId = strings.Trim(decodePropertyString(data, propType), "<>")
		}
	}

	if att.Filename == "" && att.Data == nil {
		return nil, ""
	}
	return att, contentId
}

func decodePropertyString(data []byte, propType uint16) string {
	switch propType {
	case propTypeString:
		return decodeUTF16(data)
	case propTypeString8:
		return email.DecodeString8(data)
	}
	return ""
}

func extractMIMEAttachments(data []byte) []email.Attachment {
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	data = bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))

	reader := bufio.NewReader(bytes.NewReader(data))
	tp := textproto.NewReader(reader)
	headers, err := tp.ReadMIMEHeader()
	if err != nil {
		return nil
	}

	contentType := headers.Get("Content-Type")
	mediaType, params, _ := mime.ParseMediaType(contentType)

	if !strings.HasPrefix(mediaType, "multipart/") {
		return nil
	}

	boundary := params["boundary"]
	if boundary == "" {
		return nil
	}

	body, _ := io.ReadAll(reader)
	return parseMIMEParts(body, boundary, 0)
}

// parseMIMEParts collects the attachments of a multipart body. depth bounds the
// two ways it re-enters itself - a nested multipart part, and a message/rfc822
// payload that is itself multipart - both of which an attacker nests as deeply
// as they like inside a single .msg attachment.
func parseMIMEParts(body []byte, boundary string, depth int) []email.Attachment {
	if depth > maxMIMEDepth {
		return nil
	}

	var attachments []email.Attachment
	mr := multipart.NewReader(bytes.NewReader(body), boundary)

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}

		partBody, _ := io.ReadAll(part)
		contentType := part.Header.Get("Content-Type")
		mediaType, params, _ := mime.ParseMediaType(contentType)
		encoding := part.Header.Get("Content-Transfer-Encoding")

		if strings.HasPrefix(mediaType, "multipart/") {
			if b := params["boundary"]; b != "" {
				attachments = append(attachments, parseMIMEParts(partBody, b, depth+1)...)
			}
			continue
		}

		if mediaType == "message/rfc822" {
			filename := getFilename(part.Header, params)
			if filename == "" {
				filename = "email.eml"
			}
			attachments = append(attachments, email.Attachment{
				Filename:    filename,
				ContentType: "message/rfc822",
				Data:        partBody,
			})
			innerAtts := extractFromRFC822(partBody, depth+1)
			attachments = append(attachments, innerAtts...)
			continue
		}

		filename := getFilename(part.Header, params)
		if filename == "" && mediaType == "application/pkcs7-signature" {
			filename = "smime.p7s"
		}

		if filename != "" {
			decoded := decodeBody(partBody, encoding)
			attachments = append(attachments, email.Attachment{
				Filename:    filename,
				ContentType: mediaType,
				Data:        decoded,
			})
		}
	}

	return attachments
}

func extractFromRFC822(data []byte, depth int) []email.Attachment {
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	data = bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))

	reader := bufio.NewReader(bytes.NewReader(data))
	tp := textproto.NewReader(reader)
	headers, err := tp.ReadMIMEHeader()
	if err != nil {
		return nil
	}

	contentType := headers.Get("Content-Type")
	mediaType, params, _ := mime.ParseMediaType(contentType)

	if !strings.HasPrefix(mediaType, "multipart/") {
		return nil
	}

	boundary := params["boundary"]
	if boundary == "" {
		return nil
	}

	body, _ := io.ReadAll(reader)
	return parseMIMEParts(body, boundary, depth)
}

func getFilename(header textproto.MIMEHeader, params map[string]string) string {
	if cd := header.Get("Content-Disposition"); cd != "" {
		_, dispParams, _ := mime.ParseMediaType(cd)
		if fn := dispParams["filename"]; fn != "" {
			return fn
		}
	}
	return params["name"]
}

func decodeBody(body []byte, encoding string) []byte {
	switch strings.ToLower(encoding) {
	case "base64":
		decoded := make([]byte, base64.StdEncoding.DecodedLen(len(body)))
		n, err := base64.StdEncoding.Decode(decoded, bytes.TrimSpace(body))
		if err == nil {
			return decoded[:n]
		}
	case "quoted-printable":
		decoded, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(body)))
		if err == nil {
			return decoded
		}
	}
	return body
}
