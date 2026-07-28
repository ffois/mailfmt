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
