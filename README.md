# mailfmt

Go library for reading Outlook and MIME mail formats: EML, MSG (OLE2/CFB),
TNEF (`winmail.dat`), compressed RTF bodies, and Italian PEC envelopes.

Extracted from [EMLy](https://github.com/ffois/emly).

## Install

```bash
go get github.com/ffois/mailfmt
```

## Usage

Detect the format and parse in one call:

```go
email, format, err := mailfmt.ParseFile("message.eml")
if err != nil {
    log.Fatal(err)
}
fmt.Println(format, email.Subject, len(email.Attachments))
```

Or call a specific reader:

```go
email, err := mailfmt.ReadMsgFile("message.msg")   // Outlook .msg
email, err := mailfmt.ReadEmlFile("message.eml")   // plain MIME
email, err := mailfmt.ReadPecInnerEml("pec.eml")   // PEC: inner postacert.eml
```

## API

| Symbol | Purpose |
|---|---|
| `ParseFile(path) (*EmailData, EmailFormat, error)` | detect + parse, PEC-aware |
| `DetectEmailFormat(path) (EmailFormat, error)` | magic-byte detection |
| `FormatEML`, `FormatMSG`, `FormatUnknown` | `EmailFormat` values |
| `ReadEmlFile(path) (*EmailData, error)` | plain MIME message |
| `ReadPecInnerEml(path) (*EmailData, error)` | PEC envelope, returns inner message |
| `ReadMsgFile(path) (*EmailData, error)` | Outlook `.msg` |
| `Read(io.ReaderAt) (*EmailData, error)` | Outlook `.msg` from a reader |
| `Parse(io.Reader) (Email, error)` | low-level MIME parser, returns `Email` |
| `ConvertToUTF8(string) string` | best-effort CP1252 to UTF-8 |

`EmailData` is the flattened, JSON-friendly result type. `Email` is the
richer low-level type returned by `Parse`.

### Outlook-compatible readers (`MS_`)

The `MS_` functions mirror the readers above with the behaviour of Microsoft
Outlook 2010 (Office 14.0.7268.5000), as observed by reverse engineering
`OUTLOOK.EXE`, `OLMAPI32.DLL` and `OUTLMIME.DLL`, and checked against
Outlook itself opening each file. They return `MSEmailData`, which embeds
`EmailData` and adds `Importance`, `ConversationTopic`, `Unsent`,
`BodyFormat` (plain, HTML or RTF: `Body` is the HTML for an HTML body, the
text otherwise), `RTFBody`, `DroppedAttachments` and `MSAttachments`: every
attachment Outlook creates, with `DisplayName`, `ContentID`,
`ContentLocation`, `Hidden` and `Embedded`. `EmailData.Attachments` holds
the visible ones, as Outlook lists them. Addresses are written
`"Name" <address>`, names kept in UTF-8.

| Symbol | Purpose |
|---|---|
| `MS_ParseFile(path) (*MSEmailData, EmailFormat, error)` | detect by extension + parse; no PEC handling |
| `MS_DetectEmailFormat(path) (EmailFormat, error)` | `.msg` / `.eml` by extension, as Windows hands files to Outlook |
| `MS_ReadEmlFile(path) (*MSEmailData, error)` | MIME import as `IConverterSession::MIMEToMAPI` does it |
| `MS_ReadAttachedMessage(path, indexes...) (*MSEmailData, error)` | an attached message of a `.eml` or `.msg`, at any depth, opened from the attachment list |
| `MS_ReadEmlAttachedMessage(path, att) (*MSEmailData, error)` | an attached message of a `.eml`, opened from the attachment list |
| `MS_ReadPecInnerEml(path) (*MSEmailData, error)` | the PEC's `postacert.eml`, opened as an attached message |
| `MS_ReadMsgFile(path) (*MSEmailData, error)` | Outlook `.msg`, fixed-length properties included |
| `MS_Read(io.ReaderAt) (*MSEmailData, error)` | Outlook `.msg` from a reader |

Where they differ from the plain readers:

- **EML body**: the HTML body, else the plain one, chosen as Outlook's MIME
  engine chooses them: a `multipart/related` root names it with `start=`;
  otherwise the first non-attachment `text/html` (`text/enriched` as a
  fallback) or `text/plain`, and inside `multipart/mixed` only the first
  text-bearing part counts. Charsets are decoded, and the
  `<meta http-equiv="Content-Type">` of an HTML body is removed.
- **EML attachments**: every other non-empty part, in document order; the
  alternatives of the body are not attachments. A later text part in
  `multipart/mixed` is one. Read receipts are dropped, delivery reports are
  `details.txt`, `message/rfc822` parts are `Embedded`. With an HTML body, a
  part that has a `Content-ID` or `Content-Location` (its own or an
  ancestor's) and is not disposed `attachment` is flagged `ATT_MHTML_REF`
  and `Hidden`, and so is any part the body refers to (see Visibility).
- **EML names**: the declared filename, else the `Content-Type` name, given
  the extension Windows registers for the type when it has none; without a
  name the part's subject, else its `Content-Description`, else
  `Allegato senza titolo 00000` (the Italian template of this Office,
  numbered per message), plus the extension. An attached message is shown
  as `name (size)`, the size being three quarters of the encoded part
  formatted as Windows does (`postacert.eml (311 KB)`), cut to fit with
  `...`; its file name is that, without the characters Outlook drops, plus
  `.msg`.
- **EML headers**: encoded words are decoded in any charset;
  `Importance` from `Importance` / `X-Priority` / `X-MSMail-Priority` (a
  valid `Importance` always wins); `ConversationTopic` is the subject
  without a prefix of up to three characters ending in `:`; `X-Unsent` read
  as a number.
- **S/MIME, PEC**: in a `multipart/signed` message the signature is not an
  attachment, and it must be a PKCS #7 `SignedData` (DER or BER: version,
  digest algorithms, encapsulated content, signer infos) or the message is
  refused, as Outlook refuses it. An opaque one (`application/pkcs7-mime` or
  `x-pkcs7-mime`, a `.msg` of class `IPM.Note.SMIME`) shows the MIME entity
  its `SignedData` encapsulates; an encrypted one (`EnvelopedData`) cannot be
  opened without the recipient's key, and fails as in Outlook. Signatures
  are not verified. A signed message with `daticert.xml` is a PEC.
- **winmail.dat**: decoded only when typed `application/ms-tnef` or named
  `winmail.dat`, only the last such part, and only when its
  `PR_TNEF_CORRELATION_KEY` matches `X-MS-TNEF-Correlator` if the header is
  present; once decoded it is removed even if empty, and what it held
  follows the other attachments. Attribute IDs are matched with their type.
  A record without `PR_ATTACH_METHOD` is left out and counted in
  `DroppedAttachments`, for which Outlook warns. Attached messages stay one
  `Embedded` attachment; files take their MAPI long name (unnamed ones stay
  unnamed) and only their own `PR_ATTACH_MIME_TAG`. Its compressed RTF,
  kept in `RTFBody`, replaces the MIME body, correlator or not: the HTML
  encapsulated in it, or the text of any other RTF (`BodyFormat` RTF). For a
  `multipart/report` its recipient table replaces To/Cc/Bcc; without a
  correlator its `[[ name : n in winmail.dat ]]` tags are stripped from a
  plain text body.
- **Attached messages**: `MS_ReadAttachedMessage` opens one from what carries
  it. A MIME part is read as a `.eml`, except that inside a clear-signed
  message one whose `winmail.dat` was decoded keeps only its attached
  messages. A `.msg` storage cannot be opened unless its `PR_ATTACH_DATA_OBJ`
  entry holds 1 in its high dword (as Outlook writes it) and the message has
  a property stream, `PR_MESSAGE_FLAGS`, a class, and strings of the kind its
  own `PR_STORE_SUPPORT_MASK` sets, recipients included; without a stored
  topic, the topic is derived from the subject. A message in a `winmail.dat`
  is read from its `attMsgProps`, attributes standing in for missing
  properties (`attFrom`, `attDateSent`, `attPriority`, `attBody`).
- **Visibility**: an attachment is hidden when `PR_ATTACHMENT_HIDDEN` is set
  or its `PR_ATTACH_FLAGS` carry `ATT_MHTML_REF`, whatever the body
  (`ATT_INVISIBLE_IN_HTML` alone hides nothing), and when an HTML body refers
  to it in an attribute value: `cid:` with the scheme in lower case and the
  ID in any case, or its `Content-Location`. Measured on the attachment line
  Outlook writes when it renders a message.
- **MSG**: read as `OpenIMsgOnIStg` loads it. The file is refused when the
  version 3 header declares directory sectors, when the message has no
  `__properties_version1.0` (or one shorter than its 32-byte header),
  no `__nameid_version1.0`, no listed `PR_MESSAGE_CLASS`, when an
  attachment has no property stream, or when a string is of the wrong kind:
  a message is Unicode with `STORE_UNICODE_OK` in `PR_STORE_SUPPORT_MASK`,
  ANSI otherwise, and holds only strings of that kind. `PT_STRING8` strings
  are read in the ANSI code page of Windows (1252 here) whatever
  `PR_MESSAGE_CODEPAGE` says, except `PR_BODY`, read in `PR_INTERNET_CPID`.
  Only the properties the property stream lists exist. Attachments and
  recipients are those numbered below the next IDs of the header; a broken
  recipient leaves the message with none, and attachments are shown only
  with `PR_HASATTACH` or `MSGFLAG_HASATTACH`. To/Cc/Bcc come from the
  recipient table (`PR_SMTP_ADDRESS`, else `PR_EMAIL_ADDRESS`). The body is
  `PR_HTML` (as binary only, in `PR_INTERNET_CPID`); else, only when
  `PR_BODY` is listed, the RTF: the HTML encapsulated in it, decoded with
  each font's charset, or the text of any other RTF (`BodyFormat` RTF); else
  the `PR_BODY` text. `Importance`, `Unsent` (`MSGFLAG_UNSENT`) and `Date`
  (`PR_CLIENT_SUBMIT_TIME`, else `PR_MESSAGE_DELIVERY_TIME`) come from the
  fixed-length properties. Each attachment object is one attachment (a
  multipart one is not opened); an attached message is `Embedded`, named
  `PR_DISPLAY_NAME` with the file name derived from it. An
  `IPM.Note.SMIME.MultipartSigned` or `IPM.Note.SMIME` message shows the
  body and attachments of the signed entity it carries.
- **Not modelled**: what Outlook only does on screen, such as joining the
  lines of a plain text body; the strings Outlook generates are those of an
  Italian Office, and the ANSI code page that of an Italian Windows.

The fixtures under `testdata/` have `.ms.golden.json` snapshots of what the
`MS_` readers return, attached messages included (`TestMSGoldenFixtures`;
rerun with `-update` after a deliberate change). `FuzzMSParse`, `FuzzMSBER`,
`FuzzMSRead` and `FuzzMSDecode` fuzz the parsers of untrusted input
(`go test ./internal/msg -run '^$' -fuzz FuzzMSRead`).

## What it handles

- MSG bodies stored as `PidTagRtfCompressed`: decompressed per [MS-OXRTFCP]
  and de-encapsulated per [MS-OXRTFEX].
- `winmail.dat` TNEF containers, including nested ones.
- PEC (Posta Elettronica Certificata): the real message inside
  `postacert.eml`, with `daticert.xml` + S/MIME detection.
- Attachments and inline embedded files (by Content-ID).

### Malformed TNEF input

The upstream TNEF decoder ([github.com/teamwork/tnef](https://github.com/teamwork/tnef),
unmaintained) will dereference a nil attachment record on a crafted
`winmail.dat`. `internal/tnef` recovers that panic at its trust boundary and
returns it as an error instead, leaving the unparseable attachment in the
list unchanged. This contains the defect at the boundary; it does not fix it
upstream.

## Layout

The public API lives in the root package; each format is implemented in its
own package under `internal/`.

| Package | Responsibility |
|---|---|
| `internal/email` | Shared flat types and text helpers |
| `internal/mimeparse` | RFC 5322/MIME parser |
| `internal/eml` | EML reader |
| `internal/pec` | PEC envelope |
| `internal/msg` | OLE2 compound file and Outlook properties |
| `internal/rtf` | RTF decompression and de-encapsulation |
| `internal/tnef` | `winmail.dat` container |

## Test

```
go test ./...
```

Fixtures under `testdata/` are derived from real messages with the personal
data replaced, except the `.msg` ones, which are generated: see
`internal/fixtures/README.md`.

To regenerate the snapshots after an intended behavior change:

```
go test . -run TestGoldenFixtures -update
go test ./internal/msg/ -run TestGenerateMSGTestdata -update
```

An end-to-end test against a real `.msg` file, not checked in:

```
MSG_DEBUG_PATH="/path/to/file.msg" go test ./internal/msg/ -run TestReadMSGInlineImagesE2E -v
```

## Credits

`internal/mimeparse/mimeparse.go` is adapted from
[github.com/DusanKasan/parsemail](https://github.com/DusanKasan/parsemail),
MIT licensed, Copyright (c) 2017 Dusan Kasan. See [NOTICE](./NOTICE) for the
full upstream license text.

## License

MIT
