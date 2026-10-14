package msg

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ffois/mailfmt/internal/email"
	"github.com/ffois/mailfmt/internal/eml"
	"github.com/ffois/mailfmt/internal/rtf"
)

const (
	// msPropertiesStream lists the properties of a message, attachment or
	// recipient: a header, then one 16-byte entry per property.
	msPropertiesStream = "__properties_version1.0"
	msNameIDStorage    = "__nameid_version1.0"
	msAttachPrefix     = "__attach_version1.0_#"
	msRecipPrefix      = "__recip_version1.0_#"

	// The property stream header is 32 bytes for the top-level message, 24
	// for an attached message and 8 for an attachment or recipient. A
	// message header holds the next recipient ID at offset 8 and the next
	// attachment ID at offset 12.
	msTopLevelHeaderSize  = 32
	msEmbeddedHeaderSize  = 24
	msSubObjectHeaderSize = 8
	msPropertyEntrySize   = 16

	msTagImportance         = 0x00170003
	msTagMessageFlags       = 0x0E070003
	msTagHasAttach          = 0x0E1B000B
	msTagClientSubmitTime   = 0x00390040
	msTagMessageDeliverTime = 0x0E060040
	msTagInternetCPID       = 0x3FDE0003
	msTagStoreSupportMask   = 0x340D0003

	msMsgFlagUnsent    = 0x8
	msMsgFlagHasAttach = 0x10
	msStoreUnicodeOK   = 0x00040000

	msTagAttachMethod     = 0x37050003
	msTagAttachFlags      = 0x37140003
	msTagAttachmentHidden = 0x7FFE000B
	msTagAttachDataObject = 0x3701000D
	msAttMHTMLRef         = 0x4 // ATT_MHTML_REF
	msAttachOLE           = 6

	msTagRecipientType = 0x0C150003
	msRecipCc          = 2
	msRecipBcc         = 3

	msPidTagAttachContentLocation = 0x3713
	msPidTagDisplayName           = 0x3001
	msPidTagEmailAddress          = 0x3003
	msPidTagSMTPAddress           = 0x39FE

	msClassMultipartSigned = "IPM.Note.SMIME.MultipartSigned"
	msClassSMIME           = "IPM.Note.SMIME"
)

// The errors below are the ways Outlook 2010 refuses a .msg; each names the
// message Outlook shows.
var (
	// errMSDirectorySectors: StgOpenStorage fails with STG_E_DOCFILECORRUPT
	// ("Impossibile aprire il file").
	errMSDirectorySectors = errors.New("msg: a version 3 compound file must declare 0 directory sectors (STG_E_DOCFILECORRUPT)")

	// errMSNoProperties: "Impossibile leggere l'elemento".
	errMSNoProperties = errors.New("msg: the message has no property stream (Outlook cannot read the item)")

	// errMSCorruptProperties is MAPI_E_CORRUPT_DATA: a property stream too
	// short for its header.
	errMSCorruptProperties = errors.New("msg: corrupt property stream (MAPI_E_CORRUPT_DATA)")

	// errMSAttachmentProperties: "Autorizzazione non idonea per eseguire
	// l'operazione".
	errMSAttachmentProperties = errors.New("msg: an attachment has no property stream (Outlook: access denied)")

	// errMSNoNameID: "Operazione non riuscita. Impossibile trovare un
	// oggetto".
	errMSNoNameID = errors.New("msg: no __nameid_version1.0 storage (Outlook cannot find an object)")

	// errMSNoMessageClass: "Impossibile leggere l'elemento".
	errMSNoMessageClass = errors.New("msg: PR_MESSAGE_CLASS is not in the property stream (Outlook cannot read the item)")

	// errMSStringType: "Impossibile leggere l'elemento", for a string
	// property of the message or of an attachment that is PT_STRING8 in a
	// Unicode message or PT_UNICODE in an ANSI one.
	errMSStringType = errors.New("msg: a string property does not match the message's ANSI or Unicode mode (Outlook cannot read the item)")
)

// MS_ReadFile parses the Outlook MSG file at path. See MS_Read.
func MS_ReadFile(path string) (*email.MSData, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func(f *os.File) {
		_ = f.Close()
	}(f)
	return MS_Read(f)
}

// MS_Read parses an Outlook MSG message from r the way Outlook 2010 opens a
// .msg (OUTLOOK.EXE /f: StgOpenStorage, then OLMAPI32 OpenIMsgOnIStg).
//
// A message is Unicode when its PR_STORE_SUPPORT_MASK has STORE_UNICODE_OK,
// ANSI otherwise, and its string properties must all be of that kind. ANSI
// strings are read in the ANSI code page of Windows (msANSICodePage), PR_BODY
// in PR_INTERNET_CPID when the message has it.
//
// The file is refused, as Outlook refuses it, when the compound file is
// version 3 with a non-zero directory sector count in its header, when the
// message has no property stream (or one shorter than its header), when an
// attachment it loads has no property stream or a string of the other kind,
// when there is no __nameid_version1.0 storage, when PR_MESSAGE_CLASS is not
// listed, or when the message has a string of the other kind.
//
// A storage has exactly the properties its property stream lists: a
// __substg1.0_ stream that is not listed is ignored. Attachments and
// recipients are the storages numbered below the next IDs of the message's
// header; the counts beside them are not used. If one recipient has no
// property stream, or a string of the other kind, the message has no
// recipients. Attachments are only shown when the message says it has
// some: PR_HASATTACH, or MSGFLAG_HASATTACH in PR_MESSAGE_FLAGS.
//
// From the result:
//
//   - To, Cc and Bcc come from the recipient table, each `"Name" <address>`
//     with PR_SMTP_ADDRESS, else PR_EMAIL_ADDRESS; From is formatted the same;
//   - Body is PR_HTML (PT_BINARY only, decoded in PR_INTERNET_CPID); else,
//     when PR_BODY is listed, PR_RTF_COMPRESSED: the HTML encapsulated in it,
//     decoded with each font's charset, or the text of any other RTF
//     (BodyFormat MSBodyRTF); else the PR_BODY text. cid: references are
//     resolved, and RTFBody is the decompressed RTF;
//   - an attachment is hidden when PR_ATTACHMENT_HIDDEN is set, when its
//     PR_ATTACH_FLAGS carry ATT_MHTML_REF, or when an HTML body refers to it
//     (email.MSHideReferenced);
//   - an attached message is Embedded, named PR_DISPLAY_NAME, with the file
//     name Outlook derives from that (".msg" when it has no name); MS_Open
//     and AttachedMessage open it;
//   - an IPM.Note.SMIME.MultipartSigned message takes body and attachments
//     from the multipart/signed entity its attachment carries, an
//     IPM.Note.SMIME one from the entity its smime.p7m's SignedData
//     encapsulates, read as eml.MS_Parse reads a .eml; an encrypted one is
//     refused;
//   - Importance, Unsent (MSGFLAG_UNSENT) and Date, from PR_CLIENT_SUBMIT_TIME
//     or else PR_MESSAGE_DELIVERY_TIME, come from the fixed properties;
//     Subject is PR_SUBJECT alone and ConversationTopic the stored
//     PR_CONVERSATION_TOPIC.
func MS_Read(r io.ReaderAt) (*email.MSData, error) {
	m, err := MS_Open(r)
	if err != nil {
		return nil, err
	}
	return m.Data, nil
}

// MSMessage is a message read from a .msg the way Outlook opens it, with
// what it takes to open the messages attached to it.
type MSMessage struct {
	Data *email.MSData

	// Entity is the MIME entity an S/MIME message shows: the whole
	// multipart/signed entity of an IPM.Note.SMIME.MultipartSigned message,
	// the content of an IPM.Note.SMIME one's SignedData; nil for any other.
	// Its attached messages are MIME: eml.MS_ParseAttached reads them
	// against it.
	Entity []byte

	cfb      *cfbReader
	attached []*msObject // the attachment object of each MSAttachments entry
}

// MS_Open reads a .msg as MS_Read does and keeps it open for
// AttachedMessage.
func MS_Open(r io.ReaderAt) (*MSMessage, error) {
	cfb, err := newCFBReader(r)
	if err != nil {
		return nil, err
	}
	if cfb.header.MajorVersion == 3 && cfb.header.DirectorySectors != 0 {
		return nil, errMSDirectorySectors
	}
	if cfb.root == nil {
		return nil, errors.New("no root directory")
	}

	m, err := msLoadMessage(cfb, cfb.root, msTopLevelHeaderSize, false)
	if err != nil {
		return nil, err
	}
	if msChild(cfb.root, msNameIDStorage, 1) == nil {
		return nil, errMSNoNameID
	}
	if !m.hasClass() {
		return nil, errMSNoMessageClass
	}
	if m.mixed(m.unicodeMode()) {
		return nil, errMSStringType
	}
	return msBuild(cfb, m, m.hasAttachments())
}

// errMSNotStored is returned by AttachedMessage for an attachment that is
// not a message stored in the .msg.
var errMSNotStored = errors.New("msg: the attachment is not an attached message stored in the file")

// errMSAttachedMessage is how Outlook fails to open an attached message:
// "Impossibile salvare l'allegato" when saving it, measured on the rules
// AttachedMessage applies.
var errMSAttachedMessage = errors.New("msg: Outlook cannot open the attached message")

// AttachedMessage opens MSAttachments[i], an attached message stored in the
// .msg, the way Outlook opens it from the attachment list. Measured on
// Outlook 2010, it cannot be opened when:
//
//   - the attachment's PR_ATTACH_DATA_OBJ entry does not hold 1 in its high
//     dword, as Outlook writes it;
//   - the message has no property stream, no PR_MESSAGE_FLAGS (0 will do) or
//     no PR_MESSAGE_CLASS;
//   - a string of the message, of an attachment or of a recipient is not of
//     the kind its own PR_STORE_SUPPORT_MASK sets: unlike a top-level
//     message, a recipient of the wrong kind fails the whole message.
//
// Attachments and recipients follow the next IDs of its 24-byte header, its
// attachments are shown without PR_HASATTACH, and without a stored
// PR_CONVERSATION_TOPIC the topic is derived from the subject. (Outlook was
// measured saving the attached message, which may itself set those two.) An
// S/MIME message carries MIME attached messages instead: see Entity.
func (m *MSMessage) AttachedMessage(i int) (*MSMessage, error) {
	if m.Entity != nil || i < 0 || i >= len(m.attached) {
		return nil, errMSNotStored
	}
	o := m.attached[i]
	node := o.objects[msTagAttachDataObject]
	if node == nil || o.fixed[msTagAttachMethod] == msAttachOLE {
		return nil, errMSNotStored
	}
	if o.objectEntries[msTagAttachDataObject]>>32 != 1 {
		return nil, fmt.Errorf("%w: its PR_ATTACH_DATA_OBJ entry is not marked as Outlook marks it", errMSAttachedMessage)
	}
	inner, err := msLoadMessage(m.cfb, node, msEmbeddedHeaderSize, true)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errMSAttachedMessage, err)
	}
	if _, ok := inner.fixed[msTagMessageFlags]; !ok {
		return nil, fmt.Errorf("%w: it has no PR_MESSAGE_FLAGS", errMSAttachedMessage)
	}
	if !inner.hasClass() {
		return nil, fmt.Errorf("%w: %v", errMSAttachedMessage, errMSNoMessageClass)
	}
	if inner.mixed(inner.unicodeMode()) {
		return nil, fmt.Errorf("%w: %v", errMSAttachedMessage, errMSStringType)
	}
	opened, err := msBuild(m.cfb, inner, true)
	if err != nil {
		return nil, err
	}
	_, unicodeTopic := inner.props[pidTagConversationTopic<<16|propTypeString]
	_, ansiTopic := inner.props[pidTagConversationTopic<<16|propTypeString8]
	if !unicodeTopic && !ansiTopic {
		opened.Data.ConversationTopic = email.MSConversationTopic(opened.Data.Subject)
	}
	return opened, nil
}

// msBuild turns a loaded message into the MSData Outlook shows; shown says
// whether its attachments are listed.
func msBuild(cfb *cfbReader, m *msMessage, shown bool) (*MSMessage, error) {
	p := m.props
	result := &email.MSData{
		Importance:        email.MSImportanceNormal,
		ConversationTopic: msString(p, pidTagConversationTopic),
	}
	result.Subject = msString(p, pidTagSubject)
	result.From = msSender(p)
	result.To, result.Cc, result.Bcc = msRecipients(m.recipients)

	opened := &MSMessage{Data: result, cfb: cfb}
	class := msString(p, pidTagMessageClass)
	signed := strings.EqualFold(class, msClassMultipartSigned) || strings.EqualFold(class, msClassSMIME)
	inner, entity, err := msSMIME(m, class)
	if err != nil {
		return nil, err
	}
	var atts []email.MSAttachment
	if inner != nil {
		result.Body, result.BodyFormat, result.RTFBody = inner.Body, inner.BodyFormat, inner.RTFBody
		atts = inner.MSAttachments
		opened.Entity = entity
	} else {
		result.Body, result.BodyFormat, result.RTFBody = msBody(p, m.fixed)
		isHTML := result.BodyFormat == email.MSBodyHTML
		if shown {
			atts = msAttachments(m.attachments)
			opened.attached = m.attachments
		}
		if isHTML {
			email.MSHideReferenced(result.Body, atts)
			result.Body = email.MSResolveCIDs(result.Body, atts)
		}
	}
	result.MSAttachments = atts
	result.Attachments = email.MSVisible(atts)
	result.IsPec, result.HasInnerEmail = email.MSPecFlags(atts, signed)

	if v, ok := m.fixed[msTagImportance]; ok {
		result.Importance = email.MSImportance(int32(v))
	}
	if v, ok := m.fixed[msTagMessageFlags]; ok {
		result.Unsent = uint32(v)&msMsgFlagUnsent != 0
	}
	for _, tag := range []uint32{msTagClientSubmitTime, msTagMessageDeliverTime} {
		if v, ok := m.fixed[tag]; ok && v != 0 {
			result.Date = msFiletime(v).Format(time.RFC1123Z)
			break
		}
	}
	return opened, nil
}

// msObject is a message, attachment or recipient storage as OLMAPI32 loads
// it: it has the properties its property stream lists, and no others. A
// listed property whose stream is missing is empty.
type msObject struct {
	fixed   map[uint32]uint64   // fixed-length values, held in the entry
	props   map[uint32][]byte   // variable-length values, from __substg1.0_ streams
	objects map[uint32]*dirNode // PT_OBJECT values: storages, nil when missing

	// objectEntries holds the 8-byte value of each PT_OBJECT entry: the size
	// 0xFFFFFFFF, and in the high dword the 1 Outlook writes there.
	objectEntries map[uint32]uint64

	ansi, unicode bool // whether PT_STRING8 / PT_UNICODE properties are listed

	nextRecip, nextAttach uint32 // messages only
}

// mixed reports whether o lists a string of the kind a message in the given
// mode cannot hold.
func (o *msObject) mixed(unicode bool) bool {
	if unicode {
		return o.ansi
	}
	return o.unicode
}

// msLoadObject reads the property stream of node, whose header is
// headerSize bytes. A trailing partial entry is ignored, as OLMAPI32 sizes
// the read in whole entries.
func msLoadObject(cfb *cfbReader, node *dirNode, headerSize int) (*msObject, error) {
	stream := msChild(node, msPropertiesStream, 2)
	if stream == nil {
		return nil, errMSNoProperties
	}
	data, err := cfb.readNodeStream(stream)
	if err != nil {
		return nil, err
	}
	if len(data) < headerSize {
		return nil, errMSCorruptProperties
	}

	o := &msObject{
		fixed:         make(map[uint32]uint64),
		props:         make(map[uint32][]byte),
		objects:       make(map[uint32]*dirNode),
		objectEntries: make(map[uint32]uint64),
	}
	if headerSize >= msEmbeddedHeaderSize {
		o.nextRecip = binary.LittleEndian.Uint32(data[8:])
		o.nextAttach = binary.LittleEndian.Uint32(data[12:])
	}
	for off := headerSize; off+msPropertyEntrySize <= len(data); off += msPropertyEntrySize {
		tag := binary.LittleEndian.Uint32(data[off:])
		name := fmt.Sprintf("__substg1.0_%08X", tag)
		switch tag & 0xEFFF { // single- and multi-valued alike
		case propTypeString8:
			o.ansi = true
		case propTypeString:
			o.unicode = true
		}
		switch typ := tag & 0xFFFF; {
		case msFixedLength(typ):
			o.fixed[tag] = binary.LittleEndian.Uint64(data[off+8:])
		case typ == 0x000D: // PT_OBJECT
			o.objects[tag] = msChild(node, name, 1)
			o.objectEntries[tag] = binary.LittleEndian.Uint64(data[off+8:])
		case typ&0x1000 != 0:
			// Multi-valued: no property read here is one.
		default:
			var v []byte
			if s := msChild(node, name, 2); s != nil {
				v, _ = cfb.readNodeStream(s)
			}
			o.props[tag] = v
		}
	}
	return o, nil
}

// msFixedLength reports whether a property type is stored in its 16-byte
// entry rather than in a stream of its own.
func msFixedLength(typ uint32) bool {
	switch typ {
	case 0x0002, 0x0003, 0x0004, 0x0005, 0x0006, 0x0007, 0x000A, 0x000B, 0x0014, 0x0040:
		return true
	}
	return false
}

// msMessage is a loaded message with its attachments and recipients.
type msMessage struct {
	*msObject
	attachments []*msObject
	recipients  []*msObject
}

// msLoadMessage loads the message stored in node, then the attachments and
// recipients numbered below the next IDs in its header. An attachment
// without a property stream, or with a string of the wrong kind, fails the
// whole message; such a recipient leaves the message with no recipients, or
// fails it too when strictRecipients is set, as for an attached message.
func msLoadMessage(cfb *cfbReader, node *dirNode, headerSize int, strictRecipients bool) (*msMessage, error) {
	obj, err := msLoadObject(cfb, node, headerSize)
	if err != nil {
		return nil, err
	}
	m := &msMessage{msObject: obj}
	unicode := m.unicodeMode()
	for _, n := range msNumbered(node, msAttachPrefix, obj.nextAttach) {
		a, err := msLoadObject(cfb, n, msSubObjectHeaderSize)
		if errors.Is(err, errMSNoProperties) {
			return nil, errMSAttachmentProperties
		}
		if err != nil {
			return nil, err
		}
		if a.mixed(unicode) {
			return nil, errMSStringType
		}
		m.attachments = append(m.attachments, a)
	}
	for _, n := range msNumbered(node, msRecipPrefix, obj.nextRecip) {
		r, err := msLoadObject(cfb, n, msSubObjectHeaderSize)
		if err == nil && r.mixed(unicode) {
			err = errMSStringType
		}
		if err != nil && strictRecipients {
			return nil, err
		}
		if err != nil {
			m.recipients = nil
			break
		}
		m.recipients = append(m.recipients, r)
	}
	return m, nil
}

// hasClass reports whether PR_MESSAGE_CLASS is listed, in either kind.
func (m *msMessage) hasClass() bool {
	_, unicodeClass := m.props[pidTagMessageClass<<16|propTypeString]
	_, ansiClass := m.props[pidTagMessageClass<<16|propTypeString8]
	return unicodeClass || ansiClass
}

// unicodeMode reports whether the message is a Unicode one.
func (m *msMessage) unicodeMode() bool {
	return uint32(m.fixed[msTagStoreSupportMask])&msStoreUnicodeOK != 0
}

// hasAttachments reports whether the message says it has attachments; the
// object model shows none otherwise, though they are loaded.
func (m *msMessage) hasAttachments() bool {
	return byte(m.fixed[msTagHasAttach]) != 0 || uint32(m.fixed[msTagMessageFlags])&msMsgFlagHasAttach != 0
}

// msChild finds a child of node by name, case-insensitively as IStorage
// does, and by object type (1 storage, 2 stream).
func msChild(node *dirNode, name string, objectType uint8) *dirNode {
	for _, c := range node.Children {
		if c.Entry.ObjectType == objectType && strings.EqualFold(c.Name, name) {
			return c
		}
	}
	return nil
}

// msNumbered returns the storages of node named prefix plus an 8-digit hex
// number below next, in number order.
func msNumbered(node *dirNode, prefix string, next uint32) []*dirNode {
	type numbered struct {
		n    uint64
		node *dirNode
	}
	var found []numbered
	for _, c := range node.Children {
		if c.Entry.ObjectType != 1 || len(c.Name) != len(prefix)+8 || !strings.EqualFold(c.Name[:len(prefix)], prefix) {
			continue
		}
		n, err := strconv.ParseUint(c.Name[len(prefix):], 16, 32)
		if err == nil && n < uint64(next) {
			found = append(found, numbered{n, c})
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].n < found[j].n })
	out := make([]*dirNode, len(found))
	for i, f := range found {
		out[i] = f.node
	}
	return out
}

// msSender is the sender, else the represented sender, as an address.
func msSender(p map[uint32][]byte) string {
	name := msString(p, pidTagSenderName)
	if name == "" {
		name = msString(p, pidTagSentRepresentingName)
	}
	addr := msString(p, pidTagSenderEmailAddress)
	if addr == "" {
		addr = msString(p, pidTagSentRepresentingAddr)
	}
	return email.MSFormatAddress(name, addr)
}

// msRecipients splits the recipient table by PR_RECIPIENT_TYPE; a type other
// than Cc or Bcc counts as To.
func msRecipients(recips []*msObject) (to, cc, bcc []string) {
	for _, r := range recips {
		addr := msString(r.props, msPidTagSMTPAddress)
		if addr == "" {
			addr = msString(r.props, msPidTagEmailAddress)
		}
		entry := email.MSFormatAddress(msString(r.props, msPidTagDisplayName), addr)
		if entry == "" {
			continue
		}
		// MAPI_P1 and MAPI_SUBMITTED are flags on the type.
		switch uint32(r.fixed[msTagRecipientType]) &^ 0x90000000 {
		case msRecipCc:
			cc = append(cc, entry)
		case msRecipBcc:
			bcc = append(bcc, entry)
		default:
			to = append(to, entry)
		}
	}
	return to, cc, bcc
}

// msBody returns the body Outlook shows, its format and the decompressed RTF.
// Measured on Outlook 2010:
//
//   - PR_HTML, read as PT_BINARY only (a string PR_HTML is ignored), wins
//     over everything, whatever PR_NATIVE_BODY_INFO says;
//   - else PR_RTF_COMPRESSED, but only when PR_BODY is listed, even empty:
//     HTML encapsulated in it is an HTML body, any other RTF an RTF body whose
//     text is derived from the RTF, not taken from PR_BODY;
//   - else PR_BODY, as text. An RTF without PR_BODY is ignored.
func msBody(p map[uint32][]byte, fixed map[uint32]uint64) (string, email.MSBodyFormat, string) {
	var rtfBody []byte
	if data, ok := p[pidTagRtfCompressed<<16|propTypeBinary]; ok {
		if out, err := rtf.Decompress(data); err == nil {
			rtfBody = out
		}
	}
	if b := p[pidTagBodyHTML<<16|propTypeBinary]; len(b) > 0 {
		if cp, ok := fixed[msTagInternetCPID]; ok {
			return rtf.DecodeCodePage(b, int(int32(cp))), email.MSBodyHTML, string(rtfBody)
		}
		return email.ConvertToUTF8(string(b)), email.MSBodyHTML, string(rtfBody)
	}
	_, unicodeBody := p[pidTagBody<<16|propTypeString]
	_, ansiBody := p[pidTagBody<<16|propTypeString8]
	if rtfBody != nil && (unicodeBody || ansiBody) {
		if rtf.IsEncapsulatedHTML(rtfBody) {
			return rtf.MS_DeEncapsulateHTML(rtfBody), email.MSBodyHTML, string(rtfBody)
		}
		return rtf.MS_RTFToText(rtfBody), email.MSBodyRTF, string(rtfBody)
	}
	return msBodyText(p, fixed), email.MSBodyPlain, string(rtfBody)
}

// msSMIME reads the entity an S/MIME message keeps in its attachment, which
// Outlook opens in place of the message's own body and attachments: for
// IPM.Note.SMIME.MultipartSigned the whole multipart/signed MIME message,
// headers included; for IPM.Note.SMIME the MIME entity the smime.p7m's
// SignedData encapsulates (an encrypted one cannot be opened). It returns
// what Outlook shows and the entity, or nil for any other message or one
// with no such data.
func msSMIME(m *msMessage, class string) (*email.MSData, []byte, error) {
	multipartSigned := strings.EqualFold(class, msClassMultipartSigned)
	if !multipartSigned && !strings.EqualFold(class, msClassSMIME) || len(m.attachments) == 0 {
		return nil, nil, nil
	}
	entity := m.attachments[0].props[pidTagAttachData<<16|propTypeBinary]
	if len(entity) == 0 {
		return nil, nil, nil
	}
	if !multipartSigned {
		var err error
		if entity, err = eml.MS_SMIMEContent(entity); err != nil {
			return nil, nil, fmt.Errorf("msg: S/MIME content: %w", err)
		}
	}
	inner, err := eml.MS_Parse(entity)
	if err != nil {
		return nil, nil, fmt.Errorf("msg: S/MIME content: %w", err)
	}
	return inner, entity, nil
}

// msAttachments builds the attachments Outlook holds, in number order.
//
// An attachment is hidden, whatever the body's format, when
// PR_ATTACHMENT_HIDDEN is set or its PR_ATTACH_FLAGS carry ATT_MHTML_REF (the
// flags OUTLMIME.DLL sets on parts an HTML body refers to are
// ATT_MHTML_REF | ATT_INVISIBLE_IN_HTML; the latter alone hides nothing, as
// measured); an HTML body hides those it refers to too (MSHideReferenced).
// One whose PR_ATTACH_DATA_OBJ is a storage, and not an OLE object, is an
// attached message: Embedded, with no Data, named PR_DISPLAY_NAME alone (its
// subject is not used), and with the file name Outlook derives from that
// name unless it stores one. Any other attachment without a display name
// shows its file name.
func msAttachments(atts []*msObject) []email.MSAttachment {
	out := make([]email.MSAttachment, 0, len(atts))
	for _, o := range atts {
		p := o.props
		a := email.MSAttachment{
			DisplayName:     msString(p, msPidTagDisplayName),
			ContentID:       strings.Trim(msString(p, pidTagAttachContentId), "<>"),
			ContentLocation: msString(p, msPidTagAttachContentLocation),
		}
		a.Filename = msString(p, pidTagAttachLongFilename)
		if a.Filename == "" {
			a.Filename = msString(p, pidTagAttachFilename)
		}
		a.ContentType = msString(p, pidTagAttachMimeTag)
		a.Data = p[pidTagAttachData<<16|propTypeBinary]

		if o.objects[msTagAttachDataObject] != nil && o.fixed[msTagAttachMethod] != msAttachOLE {
			a.Embedded = true
			if a.Filename == "" {
				a.Filename = email.MSAttachedMessageFileName(a.DisplayName)
			}
		} else {
			if a.DisplayName == "" {
				a.DisplayName = a.Filename
			}
			if a.Filename == "" {
				a.Filename = a.DisplayName
			}
		}
		a.Hidden = byte(o.fixed[msTagAttachmentHidden]) != 0 || uint32(o.fixed[msTagAttachFlags])&msAttMHTMLRef != 0
		out = append(out, a)
	}
	return out
}

// msANSICodePage is the code page Outlook reads PT_STRING8 properties in:
// the ANSI code page of the Windows it runs on, measured on an Italian one.
// PR_MESSAGE_CODEPAGE plays no part, and UTF-8 bytes are not recognised.
const msANSICodePage = 1252

// msString reads a string property as Outlook reads it from a .msg:
// PT_UNICODE as is, PT_STRING8 in msANSICodePage.
func msString(p map[uint32][]byte, id uint32) string {
	if data, ok := p[id<<16|propTypeString]; ok {
		return decodeUTF16(data)
	}
	if data, ok := p[id<<16|propTypeString8]; ok {
		return rtf.DecodeCodePage([]byte(strings.TrimRight(string(data), "\x00")), msANSICodePage)
	}
	return ""
}

// msBodyText reads PR_BODY: a PT_STRING8 one is in PR_INTERNET_CPID when the
// message has it, unlike every other string (measured).
func msBodyText(p map[uint32][]byte, fixed map[uint32]uint64) string {
	if data, ok := p[pidTagBody<<16|propTypeString8]; ok {
		if cp, ok := fixed[msTagInternetCPID]; ok {
			return rtf.DecodeCodePage([]byte(strings.TrimRight(string(data), "\x00")), int(int32(cp)))
		}
	}
	return msString(p, pidTagBody)
}

// msFiletime converts a Windows FILETIME (100 ns ticks since 1601) to UTC.
func msFiletime(ft uint64) time.Time {
	const ticksPerSecond = 10_000_000
	const unixEpochOffset = 11_644_473_600 // seconds from 1601-01-01 to 1970-01-01
	secs := int64(ft/ticksPerSecond) - unixEpochOffset
	nsec := int64(ft%ticksPerSecond) * 100
	return time.Unix(secs, nsec).UTC()
}
