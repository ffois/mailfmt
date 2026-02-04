package internal

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
	"strings"
	"unicode/utf16"
)

const (
	cfbSignature       = 0xE11AB1A1E011CFD0
	miniStreamCutoff   = 4096
	maxRegularSector   = 0xFFFFFFFA
	noStream           = 0xFFFFFFFF
	difatInHeader      = 109
	directoryEntrySize = 128
)

const (
	pidTagSubject              = 0x0037
	pidTagConversationTopic    = 0x0070
	pidTagMessageClass         = 0x001A
	pidTagBody                 = 0x1000
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

func ReadMsgFile(path string) (*EmailData, error) {
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

func Read(r io.ReaderAt) (*EmailData, error) {
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
		for i := uint32(0); i < cfb.header.DIFATSectors && difatSector < maxRegularSector; i++ {
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

	for sector < maxRegularSector {
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

func (cfb *cfbReader) collectChildren(parent *dirNode, startIdx int) {
	var traverse func(idx int)
	traverse = func(idx int) {
		if idx < 0 || idx >= len(cfb.dirEntries) || uint32(idx) == noStream {
			return
		}
		child, ok := cfb.nodesByIdx[idx]
		if !ok {
			return
		}
		if child.Entry.LeftSiblingID != noStream {
			traverse(int(child.Entry.LeftSiblingID))
		}
		parent.Children = append(parent.Children, child)
		if child.Entry.RightSiblingID != noStream {
			traverse(int(child.Entry.RightSiblingID))
		}
	}
	traverse(startIdx)
}

func (cfb *cfbReader) readMiniFAT() error {
	entriesPerSector := cfb.sectorSize / 4
	sector := cfb.header.FirstMiniFATSector
	for sector < maxRegularSector {
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
	cfb.miniStream = make([]byte, 0, root.StreamSize)
	sector := root.StartingSectorLoc
	remaining := int64(root.StreamSize)

	for sector < maxRegularSector && remaining > 0 {
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
	data := make([]byte, 0, entry.StreamSize)
	sector := entry.StartingSectorLoc
	remaining := int64(entry.StreamSize)

	for sector < maxRegularSector && remaining > 0 {
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
	data := make([]byte, 0, entry.StreamSize)
	sector := entry.StartingSectorLoc
	remaining := int64(entry.StreamSize)

	for sector < maxRegularSector && remaining > 0 {
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

func parseMessage(cfb *cfbReader) (*EmailData, error) {
	if cfb.root == nil {
		return nil, errors.New("no root directory")
	}

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

	email := &EmailData{}

	email.Subject = getPropString(props, pidTagSubject)
	if email.Subject == "" {
		email.Subject = getPropString(props, pidTagConversationTopic)
	}

	email.Body = getPropString(props, pidTagBodyHTML)
	if email.Body == "" {
		email.Body = getPropBinary(props, pidTagBodyHTML)
	}
	if email.Body == "" {
		email.Body = textToHTML(getPropString(props, pidTagBody))
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
		email.From = fmt.Sprintf("%s <%s>", from, fromEmail)
	} else {
		email.From = from
	}

	email.To = splitRecipients(getPropString(props, pidTagDisplayTo))
	email.Cc = splitRecipients(getPropString(props, pidTagDisplayCc))
	email.Bcc = splitRecipients(getPropString(props, pidTagDisplayBcc))

	msgClass := getPropString(props, pidTagMessageClass)
	email.IsPec = strings.Contains(strings.ToLower(msgClass), "smime") ||
		strings.Contains(strings.ToLower(email.Subject), "posta certificata")

	for _, child := range cfb.root.Children {
		if strings.HasPrefix(child.Name, "__attach_version1.0_#") {
			att := parseAttachment(cfb, child)
			if att != nil {
				if strings.HasPrefix(att.ContentType, "multipart/") {
					innerAtts := extractMIMEAttachments(att.Data)
					if len(innerAtts) > 0 {
						email.HasInnerEmail = true
						email.Attachments = append(email.Attachments, innerAtts...)
					}
				} else {
					email.Attachments = append(email.Attachments, *att)
				}
			}
		}
	}

	return email, nil
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
		return strings.TrimRight(string(data), "\x00")
	}
	return ""
}

func getPropBinary(props map[uint32][]byte, propID uint32) string {
	if data, ok := props[(propID<<16)|propTypeBinary]; ok {
		return string(data)
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

func parseAttachment(cfb *cfbReader, node *dirNode) *EmailAttachment {
	att := &EmailAttachment{}

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
		}
	}

	if att.Filename == "" && att.Data == nil {
		return nil
	}
	return att
}

func decodePropertyString(data []byte, propType uint16) string {
	switch propType {
	case propTypeString:
		return decodeUTF16(data)
	case propTypeString8:
		return strings.TrimRight(string(data), "\x00")
	}
	return ""
}

func extractMIMEAttachments(data []byte) []EmailAttachment {
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
	return parseMIMEParts(body, boundary)
}

func parseMIMEParts(body []byte, boundary string) []EmailAttachment {
	var attachments []EmailAttachment
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
				attachments = append(attachments, parseMIMEParts(partBody, b)...)
			}
			continue
		}

		if mediaType == "message/rfc822" {
			filename := getFilename(part.Header, params)
			if filename == "" {
				filename = "email.eml"
			}
			attachments = append(attachments, EmailAttachment{
				Filename:    filename,
				ContentType: "message/rfc822",
				Data:        partBody,
			})
			innerAtts := extractFromRFC822(partBody)
			attachments = append(attachments, innerAtts...)
			continue
		}

		filename := getFilename(part.Header, params)
		if filename == "" && mediaType == "application/pkcs7-signature" {
			filename = "smime.p7s"
		}

		if filename != "" {
			decoded := decodeBody(partBody, encoding)
			attachments = append(attachments, EmailAttachment{
				Filename:    filename,
				ContentType: mediaType,
				Data:        decoded,
			})
		}
	}

	return attachments
}

func extractFromRFC822(data []byte) []EmailAttachment {
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
	return parseMIMEParts(body, boundary)
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
