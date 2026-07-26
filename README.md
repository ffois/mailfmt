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

## What it handles

- MSG bodies stored as `PidTagRtfCompressed`: decompressed per [MS-OXRTFCP]
  and de-encapsulated per [MS-OXRTFEX].
- `winmail.dat` TNEF containers, including nested ones.
- PEC (Posta Elettronica Certificata): the real message inside
  `postacert.eml`, with `daticert.xml` + S/MIME detection.
- Attachments and inline embedded files (by Content-ID).

## License

MIT
