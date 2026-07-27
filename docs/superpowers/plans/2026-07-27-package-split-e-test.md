# Package Split per Formato + Suite di Test — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Spezzare il package piatto `mailfmt` in un package `internal/` per formato, mantenendo invariata l'API pubblica, e aggiungere una suite di test con fixture versionati per ogni formato.

**Architecture:** I lettori si spostano sotto `internal/` (`email`, `mimeparse`, `rtf`, `tnef`, `msg`, `eml`, `pec`). La root resta una facade: `ParseFile` e `DetectEmailFormat` come logica di dispatch, più type alias e wrapper sottili per tutto il resto. Lo spostamento avviene un package alla volta, in un ordine che lascia l'albero compilabile a ogni commit.

**Tech Stack:** Go 1.26, `github.com/teamwork/tnef`, `golang.org/x/text`. Nessuna dipendenza nuova. Test con `testing` di stdlib, nessun framework.

## Global Constraints

- Modulo: `github.com/ffois/mailfmt`. Gli import interni sono `github.com/ffois/mailfmt/internal/<nome>`.
- Go 1.26 (`go.mod`). Non alzare né abbassare la versione.
- **L'API pubblica non cambia.** Gli identificatori esportati dalla root prima e dopo devono essere identici, con gli stessi tipi. La lista completa è nella Task 8.
- **Nessun cambio di comportamento del parser.** Se un test scopre un bug reale, va annotato con `t.Skip` più un commento che spiega il bug, e riportato a fine lavoro. Non correggerlo dentro questo piano.
- I test esistenti si spostano, non si riscrivono. Le asserzioni restano identiche.
- **Nessun file da `C:\Users\LyzCoote\Desktop\Test mail` entra nel repository.** Contengono dati personali reali. Solo fixture derivati e anonimizzati.
- Ogni task finisce con `go vet ./... && go build ./... && go test ./...` verde, come la CI in `.github/workflows/ci.yml`.
- Line endings LF: il repo ha `.gitattributes` che forza LF sui `.go`.
- `msg_reader.go` inizia con un BOM UTF-8. Va rimosso durante lo spostamento.

---

## File Structure

**Nuovi:**

| File | Responsabilità |
|---|---|
| `internal/email/email.go` | `Data`, `Attachment`, `ConvertToUTF8` — tipi piatti condivisi da tutti i formati |
| `internal/mimeparse/mimeparse.go` | Parser RFC5322/MIME (ex `mailparser.go`) |
| `internal/rtf/rtf.go` | Decompressione e de-encapsulation RTF, puro `[]byte` in/out |
| `internal/tnef/tnef.go` | Estrazione ed espansione `winmail.dat` |
| `internal/msg/msg.go` | CFB reader e parsing property MSG |
| `internal/msg/rtfbody.go` | Glue MSG↔RTF: `HTMLFromCompressedRTF(props)` |
| `internal/eml/eml.go` | `ReadFile`, `Build` |
| `internal/pec/pec.go` | `ReadInner` |
| `alias.go` | Type alias e wrapper che ricostruiscono l'API pubblica |
| `example_test.go` | Tocca ogni identificatore pubblico: guardia di compatibilità |
| `golden_test.go` | Golden test table-driven su `testdata/` |
| `internal/msg/cfbwriter_test.go` | Writer CFB minimale per generare fixture `.msg` |
| `internal/msg/gen_testdata_test.go` | Genera i file in `testdata/msg/` |

**Eliminati** (contenuto spostato): `eml_reader.go`, `mailparser.go`, `msg_reader.go`, `rtf.go`, `tnef_reader.go`, `mailparser_test.go`, `rtf_test.go`.

**Modificati:** `doc.go`, `parse.go`, `format_detector.go` (solo import), `parse_test.go` (resta com'è).

---

### Task 1: `internal/email` — tipi condivisi

Primo spostamento. `EmailData` e `EmailAttachment` sono definiti in `eml_reader.go` ma usati da `msg_reader.go`, `tnef_reader.go` e `parse.go`; `ConvertToUTF8` è definita in `msg_reader.go` e usata da `eml_reader.go`. Vanno in un package che tutti possono importare.

Dopo questo task la root usa i tipi tramite alias, quindi tutti gli altri file continuano a compilare senza modifiche.

**Files:**
- Create: `internal/email/email.go`
- Modify: `eml_reader.go:14-31` (rimuovi le definizioni dei tipi)
- Modify: `msg_reader.go:1` (rimuovi BOM), `msg_reader.go:563-578` (rimuovi `ConvertToUTF8` e `decodeString8`)
- Create: `alias.go`

**Interfaces:**
- Produces: `email.Data`, `email.Attachment`, `email.ConvertToUTF8(string) string`, `email.DecodeString8([]byte) string`

- [ ] **Step 1: Crea `internal/email/email.go`**

```go
// Package email holds the flat, JSON-friendly representation that every
// format reader in this module produces, plus the text helpers shared
// between them.
package email

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
)

// Attachment is a single file carried by a message, already decoded.
type Attachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Data        []byte `json:"data"`
}

// Data is a message flattened into the shape callers consume: addresses as
// strings, a single body, attachments already decoded.
type Data struct {
	From          string       `json:"from"`
	To            []string     `json:"to"`
	Cc            []string     `json:"cc"`
	Bcc           []string     `json:"bcc"`
	Subject       string       `json:"subject"`
	Body          string       `json:"body"`
	Attachments   []Attachment `json:"attachments"`
	IsPec         bool         `json:"isPec"`
	HasInnerEmail bool         `json:"hasInnerEmail"`
	Date          string       `json:"date"`
}

// ConvertToUTF8 returns s unchanged if it is already valid UTF-8, otherwise
// decodes it from Windows-1252. On decode failure it returns s unchanged.
func ConvertToUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	decoded, _, err := transform.String(charmap.Windows1252.NewDecoder(), s)
	if err != nil {
		return s
	}
	return decoded
}

// DecodeString8 decodes a NUL-terminated single-byte string, as stored in
// MSG PT_STRING8 properties.
func DecodeString8(data []byte) string {
	return ConvertToUTF8(strings.TrimRight(string(data), "\x00"))
}
```

- [ ] **Step 2: Crea `alias.go` con i primi due alias**

```go
package mailfmt

import (
	"github.com/ffois/mailfmt/internal/email"
)

// EmailData is the flat representation returned by every reader.
type EmailData = email.Data

// EmailAttachment is a single decoded attachment.
type EmailAttachment = email.Attachment

// ConvertToUTF8 returns s unchanged if it is already valid UTF-8, otherwise
// decodes it from Windows-1252.
func ConvertToUTF8(s string) string { return email.ConvertToUTF8(s) }
```

- [ ] **Step 3: Rimuovi le definizioni duplicate dalla root**

In `eml_reader.go` cancella le righe 14-31 (i due `type ... struct`).

In `msg_reader.go`: rimuovi il BOM UTF-8 iniziale (il file inizia con `\xEF\xBB\xBF` prima di `package`), cancella `ConvertToUTF8` e `decodeString8` (righe 563-578), e sostituisci ogni chiamata a `decodeString8(x)` con `email.DecodeString8(x)`. Aggiungi `"github.com/ffois/mailfmt/internal/email"` agli import e rimuovi `charmap`, `transform` e `unicode/utf8` se non più usati.

Verifica quali import restano necessari:

```bash
go build ./... 2>&1 | head -20
```

- [ ] **Step 4: Verifica che tutto compili e i test passino**

```bash
go vet ./... && go build ./... && go test ./...
```

Atteso: PASS su tutti i test esistenti (`TestDeEncapsulateHTML`, `TestParseFilePlainEML`, ecc.). Se un test fallisce, lo spostamento ha cambiato comportamento: fermati e correggi prima di committare.

- [ ] **Step 5: Commit**

```bash
git add internal/email alias.go eml_reader.go msg_reader.go
git commit -m "refactor: move shared email types into internal/email

EmailData and EmailAttachment were defined in the EML reader but used by
the MSG and TNEF readers too, and ConvertToUTF8 was defined in the MSG
reader but used by the EML one. Both now live in internal/email, which is
the one package every format reader can import.

The root package re-exports them as type aliases, so the public API is
unchanged.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 2: `internal/mimeparse` — parser MIME

`mailparser.go` è codice derivato da `parsemail` e non dipende da nient'altro nel modulo. Si sposta per intero.

Il package si chiama `mimeparse` e non `mime` perché il file importa lo stdlib `mime`, `mime/multipart` e `mime/quotedprintable`: un package `mime` che importa `mime` non compila.

**Files:**
- Create: `internal/mimeparse/mimeparse.go` (da `mailparser.go`)
- Delete: `mailparser.go`
- Modify: `alias.go`
- Modify: `eml_reader.go` (usa `Parse` tramite il wrapper, nessuna modifica al corpo)

**Interfaces:**
- Consumes: niente
- Produces: `mimeparse.Parse(io.Reader) (mimeparse.Email, error)`, `mimeparse.Email`, `mimeparse.Attachment`, `mimeparse.EmbeddedFile`

- [ ] **Step 1: Sposta il file**

```bash
mkdir -p internal/mimeparse
git mv mailparser.go internal/mimeparse/mimeparse.go
```

Cambia la riga `package mailfmt` in `package mimeparse`. Il commento di attribuzione a `parsemail` in cima al file resta dov'è.

- [ ] **Step 2: Aggiungi alias e wrapper in `alias.go`**

```go
// Email is a parsed MIME message with all RFC5322 headers.
type Email = mimeparse.Email

// Attachment is an attachment part of a parsed MIME message.
type Attachment = mimeparse.Attachment

// EmbeddedFile is an inline part of a parsed MIME message, referenced from
// the body via its Content-ID.
type EmbeddedFile = mimeparse.EmbeddedFile

// Parse reads a MIME message from r.
func Parse(r io.Reader) (Email, error) { return mimeparse.Parse(r) }
```

Aggiungi `"io"` e `"github.com/ffois/mailfmt/internal/mimeparse"` agli import di `alias.go`.

- [ ] **Step 3: Sposta i test MIME**

`mailparser_test.go` contiene tre test. Due verificano `Parse` e vanno in `internal/mimeparse/mimeparse_test.go`: `TestParseAttachmentWithContentIdKeepsFilename` e `TestParseInlineImageStaysEmbeddedWithFilename`, con le rispettive costanti `yahooStyleEml` e `inlineImageEml`.

Il terzo, `TestEmbeddedFilename`, verifica `embeddedFilename` che sta in `eml_reader.go` e non si sposta ora: lascialo in `mailparser_test.go`, che rinomini in `eml_reader_test.go` (andrà in `internal/eml` alla Task 6).

```bash
git mv mailparser_test.go eml_reader_test.go
```

Poi crea `internal/mimeparse/mimeparse_test.go` con `package mimeparse`, i due test e le due costanti, e rimuovili da `eml_reader_test.go`. Nei test spostati `Parse` e `EmbeddedFile` sono già nel package, quindi le chiamate restano identiche.

- [ ] **Step 4: Verifica**

```bash
go vet ./... && go build ./... && go test ./...
```

Atteso: PASS. `go test ./...` ora esegue anche `internal/mimeparse`; controlla che quel package riporti `ok`, non `no test files`.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor: move the MIME parser into internal/mimeparse

The package is named mimeparse rather than mime because the parser
imports the standard library mime, mime/multipart and mime/quotedprintable.

Parse, Email, Attachment and EmbeddedFile stay reachable from the root
package via a wrapper and type aliases.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 3: `internal/rtf` — decompressione e de-encapsulation

`rtf.go` è quasi puro: opera su `[]byte`. L'unica eccezione è `htmlFromCompressedRTF`, che cerca la chiave `(pidTagRtfCompressed<<16)|propTypeBinary` in una mappa di property MSG. Quella funzione è glue MSG, non logica RTF: resta nella root per ora e si sposterà in `internal/msg` alla Task 5.

**Files:**
- Create: `internal/rtf/rtf.go` (da `rtf.go`, meno `htmlFromCompressedRTF`)
- Create: `internal/rtf/rtf_test.go` (da `rtf_test.go`, meno i due casi MSG)
- Modify: `rtf.go` → resta solo `htmlFromCompressedRTF`, rinominato `msg_rtfbody.go`
- Delete: `rtf_test.go`

**Interfaces:**
- Consumes: niente
- Produces: `rtf.Decompress([]byte) ([]byte, error)`, `rtf.IsEncapsulatedHTML([]byte) bool`, `rtf.DeEncapsulateHTML([]byte) string`

- [ ] **Step 1: Crea il package `internal/rtf`**

```bash
mkdir -p internal/rtf
git mv rtf.go internal/rtf/rtf.go
```

In `internal/rtf/rtf.go`:
- cambia `package mailfmt` in `package rtf`;
- rinomina `decompressRTF` → `Decompress`, `isEncapsulatedHTML` → `IsEncapsulatedHTML`, `deEncapsulateHTML` → `DeEncapsulateHTML`;
- **taglia** `htmlFromCompressedRTF` (righe 58-69 dell'originale) — va nel file nuovo dello step 2;
- lascia dov'è tutto il resto (`rtfDictInit`, `cp1252Table`, `init`, `rtfState`, `rtfDestSkip`, `rtfSymbolWord`, `detectCodePage`, `isAlpha`, `hexVal`, le costanti `rtfCompressedMagic` / `rtfUncompressedMagic`);
- esporta anche le due costanti magic: servono ai test. `RTFCompressedMagic` e `RTFUncompressedMagic`.

- [ ] **Step 2: Crea `msg_rtfbody.go` nella root con la glue**

```go
package mailfmt

import (
	"github.com/ffois/mailfmt/internal/rtf"
)

// htmlFromCompressedRTF returns the HTML body encapsulated in a message's
// PidTagRtfCompressed property, or "" when the property is missing, cannot be
// decompressed, or holds genuine RTF rather than encapsulated HTML.
func htmlFromCompressedRTF(props map[uint32][]byte) string {
	data, ok := props[(pidTagRtfCompressed<<16)|propTypeBinary]
	if !ok {
		return ""
	}
	decompressed, err := rtf.Decompress(data)
	if err != nil {
		return ""
	}
	if !rtf.IsEncapsulatedHTML(decompressed) {
		return ""
	}
	return rtf.DeEncapsulateHTML(decompressed)
}
```

Confronta con l'originale `rtf.go:58-69` e mantieni la stessa logica: se differisce, vince l'originale.

- [ ] **Step 3: Sposta i test RTF**

Da `rtf_test.go`, in `internal/rtf/rtf_test.go` (`package rtf`) vanno `TestDeEncapsulateHTML`, `TestDeEncapsulateHTMLSkipsMhtmltag` e `TestDecompressRTFUncompressed`, con le chiamate rinominate (`deEncapsulateHTML` → `DeEncapsulateHTML`, `decompressRTF` → `Decompress`, `rtfUncompressedMagic` → `RTFUncompressedMagic`).

`TestHTMLFromCompressedRTFNonHTML` e `TestReadMSGInlineImagesE2E` restano nella root, in un nuovo `msg_rtfbody_test.go`: usano property MSG e `ReadMsgFile`. Alla Task 5 si sposteranno in `internal/msg`.

```bash
git rm rtf_test.go
```

- [ ] **Step 4: Verifica**

```bash
go vet ./... && go build ./... && go test ./...
```

Atteso: PASS, con `internal/rtf` che riporta `ok` e tre test eseguiti.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor: move RTF decompression into internal/rtf

The RTF code operates on plain byte slices and depends on nothing else in
the module, so it moves out whole. htmlFromCompressedRTF stays behind: it
looks up MSG property IDs, so it is MSG-to-RTF glue rather than RTF logic,
and it follows the MSG reader in a later commit.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 4: `internal/tnef` — winmail.dat

`tnef_reader.go` dipende solo da `EmailAttachment`, ora `email.Attachment`. Attenzione al nome: il package importa `github.com/teamwork/tnef`, che dichiara anch'esso `package tnef`. L'import esterno va aliasato.

**Files:**
- Create: `internal/tnef/tnef.go` (da `tnef_reader.go`)
- Delete: `tnef_reader.go`
- Modify: `eml_reader.go:128,265` (chiamate a `expandTNEFAttachments`)

**Interfaces:**
- Consumes: `email.Attachment`
- Produces: `tnef.IsTNEFData([]byte) bool`, `tnef.Extract([]byte) ([]email.Attachment, error)`, `tnef.Expand([]email.Attachment) []email.Attachment`

- [ ] **Step 1: Sposta il file**

```bash
mkdir -p internal/tnef
git mv tnef_reader.go internal/tnef/tnef.go
```

In `internal/tnef/tnef.go`:
- `package mailfmt` → `package tnef`;
- aliasa l'import esterno: `tneflib "github.com/teamwork/tnef"`, e sostituisci ogni `tnef.` riferito alla libreria con `tneflib.` (in `extractTNEFRecursive` e `isEmbeddedMsgPlaceholder`, che prende `*tnef.Attachment` → `*tneflib.Attachment`);
- rinomina `isTNEFData` → `IsTNEFData`, `extractTNEFAttachments` → `Extract`, `expandTNEFAttachments` → `Expand`;
- sostituisci `EmailAttachment` con `email.Attachment` ovunque, e aggiungi l'import `"github.com/ffois/mailfmt/internal/email"`;
- lascia non esportate `extractTNEFRecursive`, `isTNEFAttachment`, `isEmbeddedMsgPlaceholder`, `inferFilename`, `looksLikeEML`, `findEmbeddedTNEFStreamsFromRaw`, `extractPRAttachDataObjDirect`, `skipCountedBlobs`, `skipMVFixed`, `padTo4`, `mimeTypeFromFilename`.

- [ ] **Step 2: Aggiorna i due call site in `eml_reader.go`**

Righe 128 e 265, entrambe:

```go
	attachments = expandTNEFAttachments(attachments)
```

diventano:

```go
	attachments = tnef.Expand(attachments)
```

Aggiungi `"github.com/ffois/mailfmt/internal/tnef"` agli import di `eml_reader.go`.

- [ ] **Step 3: Verifica**

```bash
go vet ./... && go build ./... && go test ./...
```

Atteso: PASS. `internal/tnef` riporta `no test files` — normale, i test arrivano alla Task 15.

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "refactor: move TNEF extraction into internal/tnef

The upstream github.com/teamwork/tnef package declares the same package
name, so it is imported as tneflib inside ours.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 5: `internal/msg` — CFB reader e property MSG

Il pezzo più grosso: 794 righe. Si sposta insieme alla glue RTF creata alla Task 3.

**Files:**
- Create: `internal/msg/msg.go` (da `msg_reader.go`)
- Create: `internal/msg/rtfbody.go` (da `msg_rtfbody.go`)
- Create: `internal/msg/rtfbody_test.go` (da `msg_rtfbody_test.go`)
- Delete: `msg_reader.go`, `msg_rtfbody.go`, `msg_rtfbody_test.go`
- Modify: `alias.go`, `parse.go:20`

**Interfaces:**
- Consumes: `email.Data`, `email.Attachment`, `email.ConvertToUTF8`, `email.DecodeString8`, `rtf.Decompress`, `rtf.IsEncapsulatedHTML`, `rtf.DeEncapsulateHTML`
- Produces: `msg.ReadFile(string) (*email.Data, error)`, `msg.Read(io.ReaderAt) (*email.Data, error)`

- [ ] **Step 1: Sposta i file**

```bash
mkdir -p internal/msg
git mv msg_reader.go internal/msg/msg.go
git mv msg_rtfbody.go internal/msg/rtfbody.go
git mv msg_rtfbody_test.go internal/msg/rtfbody_test.go
```

In tutti e tre: `package mailfmt` → `package msg`.

In `internal/msg/msg.go`:
- sostituisci `EmailData` → `email.Data` e `EmailAttachment` → `email.Attachment` ovunque;
- `ConvertToUTF8(` → `email.ConvertToUTF8(`, `decodeString8(` → `email.DecodeString8(`;
- import `"github.com/ffois/mailfmt/internal/email"`.

Attenzione: dentro `parseMessage` c'è una variabile locale chiamata `email` (riga 436: `email := &EmailData{}`). Collide con il nome del package. Rinominala in `data`:

```go
	data := &email.Data{}

	data.Subject = getPropString(props, pidTagSubject)
	// ...e così via per tutti gli usi nella funzione, fino a
	return data, nil
```

Stesso problema in `Read` e in qualunque altra funzione con una variabile `email`. Cercale:

```bash
grep -n "email :=\|email\." internal/msg/msg.go | head -40
```

In `internal/msg/rtfbody_test.go`, `ReadMsgFile` diventa `ReadFile`.

- [ ] **Step 2: Aggiorna i wrapper in `alias.go`**

```go
// ReadMsgFile parses the Outlook MSG file at path.
func ReadMsgFile(path string) (*EmailData, error) { return msg.ReadFile(path) }

// Read parses an Outlook MSG message from r.
func Read(r io.ReaderAt) (*EmailData, error) { return msg.Read(r) }
```

Rinomina `ReadMsgFile` → `ReadFile` dentro `internal/msg/msg.go`; `Read` mantiene il nome. Aggiungi `"github.com/ffois/mailfmt/internal/msg"` agli import di `alias.go`.

- [ ] **Step 3: Aggiorna `parse.go`**

La riga 20 chiama `ReadMsgFile(path)`. Ora quella è la funzione wrapper in `alias.go`, quindi la riga non cambia. Verifica che `parse.go` compili senza modifiche.

- [ ] **Step 4: Verifica**

```bash
go vet ./... && go build ./... && go test ./...
```

Atteso: PASS. `internal/msg` esegue `TestHTMLFromCompressedRTFNonHTML`; `TestReadMSGInlineImagesE2E` riporta SKIP perché `MSG_DEBUG_PATH` non è impostata.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor: move the MSG reader into internal/msg

Carries htmlFromCompressedRTF along, since it looks up MSG property IDs.
The local variable named email inside parseMessage is renamed to data so
it stops shadowing the internal/email package.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 6: `internal/eml` — lettore EML e `Build`

`ReadEmlFile` e `ReadPecInnerEml` condividono ~100 righe quasi identiche. Qui le si estrae in `Build`, che la Task 7 riusa per la PEC.

**Files:**
- Create: `internal/eml/eml.go`
- Create: `internal/eml/eml_test.go` (da `eml_reader_test.go`)
- Delete: `eml_reader.go`, `eml_reader_test.go`
- Modify: `alias.go`
- Note: `ReadPecInnerEml` resta temporaneamente nella root, in un nuovo `pec_reader.go`, finché la Task 7 non la sposta.

**Interfaces:**
- Consumes: `email.Data`, `mimeparse.Email`, `mimeparse.EmbeddedFile`, `tnef.Expand`, `email.ConvertToUTF8`
- Produces: `eml.ReadFile(string) (*email.Data, error)`, `eml.Build(mimeparse.Email) *email.Data`

- [ ] **Step 1: Estrai `Build` dentro `eml_reader.go`, ancora nella root**

Prima di spostare, fai il refactoring dove il codice compila già. `ReadEmlFile` diventa:

```go
func ReadEmlFile(filePath string) (*EmailData, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	parsed, err := Parse(file)
	if err != nil {
		return nil, fmt.Errorf("failed to parse email: %w", err)
	}

	return buildEmailData(parsed), nil
}
```

e nasce `buildEmailData`, che è il corpo condiviso — identico a quello che oggi sta in `ReadEmlFile:45-149`:

```go
// buildEmailData flattens a parsed MIME message into EmailData: it resolves
// cid: references in the body to inline data URIs, lists inline images as
// attachments as well, expands any winmail.dat container, and flags PEC
// envelopes.
func buildEmailData(parsed Email) *EmailData {
	formatAddress := func(addr []*mail.Address) []string {
		var result []string
		for _, a := range addr {
			result = append(result, ConvertToUTF8(a.String()))
		}
		return result
	}

	body := parsed.HTMLBody
	if body == "" {
		body = parsed.TextBody
	}

	var attachments []EmailAttachment
	var hasDatiCert, hasSmime, hasInnerEmail bool

	for _, ef := range parsed.EmbeddedFiles {
		data, err := io.ReadAll(ef.Data)
		if err != nil {
			continue
		}

		b64 := base64.StdEncoding.EncodeToString(data)
		mimeType := ef.ContentType
		if parts := strings.Split(mimeType, ";"); len(parts) > 0 {
			mimeType = strings.TrimSpace(parts[0])
		}
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}

		dataURI := fmt.Sprintf("data:%s;base64,%s", mimeType, b64)
		re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta("cid:"+ef.CID))
		body = re.ReplaceAllLiteralString(body, dataURI)

		attachments = append(attachments, EmailAttachment{
			Filename:    embeddedFilename(ef, mimeType),
			ContentType: mimeType,
			Data:        data,
		})
	}

	for _, att := range parsed.Attachments {
		data, err := io.ReadAll(att.Data)
		if err != nil {
			continue
		}

		filenameLower := strings.ToLower(att.Filename)
		if filenameLower == "daticert.xml" {
			hasDatiCert = true
		}
		if filenameLower == "smime.p7s" {
			hasSmime = true
		}
		if strings.HasSuffix(filenameLower, ".eml") {
			hasInnerEmail = true
		}

		attachments = append(attachments, EmailAttachment{
			Filename:    att.Filename,
			ContentType: att.ContentType,
			Data:        data,
		})
	}

	attachments = tnef.Expand(attachments)

	var from string
	if len(parsed.From) > 0 {
		from = parsed.From[0].String()
	}

	return &EmailData{
		From:          ConvertToUTF8(from),
		To:            formatAddress(parsed.To),
		Cc:            formatAddress(parsed.Cc),
		Bcc:           formatAddress(parsed.Bcc),
		Subject:       ConvertToUTF8(parsed.Subject),
		Body:          ConvertToUTF8(body),
		Attachments:   attachments,
		IsPec:         hasDatiCert && hasSmime,
		HasInnerEmail: hasInnerEmail,
		Date:          parsed.Header.Get("Date"),
	}
}
```

- [ ] **Step 2: Fai usare `buildEmailData` anche a `ReadPecInnerEml`**

Il corpo di `ReadPecInnerEml` (righe 193-286) è lo stesso, applicato a `innerEmail`. Sostituiscilo:

```go
func ReadPecInnerEml(filePath string) (*EmailData, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	outerEmail, err := Parse(file)
	if err != nil {
		return nil, fmt.Errorf("failed to parse outer email: %w", err)
	}

	// A signed PEC carries the real message in a postacert.eml part.
	var innerEmailData []byte
	foundPec := false
	for _, att := range outerEmail.Attachments {
		if strings.Contains(strings.ToLower(att.Filename), "postacert.eml") {
			data, err := io.ReadAll(att.Data)
			if err != nil {
				return nil, fmt.Errorf("failed to read inner email content: %w", err)
			}
			innerEmailData = data
			foundPec = true
			break
		}
	}
	if !foundPec {
		return nil, fmt.Errorf("not a signed PEC or 'postacert.eml' attachment is missing")
	}

	innerEmail, err := Parse(bytes.NewReader(innerEmailData))
	if err != nil {
		return nil, fmt.Errorf("failed to parse inner email structure: %w", err)
	}

	return buildEmailData(innerEmail), nil
}
```

- [ ] **Step 3: Verifica che il refactoring non abbia cambiato comportamento**

```bash
go vet ./... && go build ./... && go test ./...
```

Atteso: PASS, in particolare `TestParseFilePecEnvelope` e `TestParseFilePlainEML`. Questi due test sono la rete di sicurezza della deduplicazione: se passano, i due percorsi producono lo stesso risultato di prima. Committa qui, prima di spostare i file.

```bash
git add eml_reader.go
git commit -m "refactor: extract buildEmailData shared by the EML and PEC readers

ReadEmlFile and ReadPecInnerEml carried about a hundred lines of identical
code: embedded-file handling, cid: to data URI substitution, PEC detection,
TNEF expansion and EmailData construction. Both now call buildEmailData.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

- [ ] **Step 4: Separa PEC in un file suo, ancora nella root**

Crea `pec_reader.go` e spostaci `ReadPecInnerEml`, togliendola da `eml_reader.go`. Il file nuovo contiene `package mailfmt`, gli import `bytes`, `fmt`, `io`, `os`, `strings`, e la sola `ReadPecInnerEml` nella forma dello Step 2.

Serve a separare i due lettori prima di spostarli in package diversi: dopo lo Step 5 `eml_reader.go` diventa `internal/eml` e `pec_reader.go` diventa `internal/pec` alla Task 7.

```bash
go build ./...
```

Atteso: nessun errore. Se `eml_reader.go` ha import ora inutilizzati (`bytes` in particolare), rimuovili.

- [ ] **Step 5: Sposta `eml_reader.go` in `internal/eml`**

```bash
mkdir -p internal/eml
git mv eml_reader.go internal/eml/eml.go
git mv eml_reader_test.go internal/eml/eml_test.go
```

In `internal/eml/eml.go`:
- `package mailfmt` → `package eml`;
- `ReadEmlFile` → `ReadFile`, `buildEmailData` → `Build`;
- `EmailData` → `email.Data`, `EmailAttachment` → `email.Attachment`, `Email` → `mimeparse.Email`, `EmbeddedFile` → `mimeparse.EmbeddedFile`;
- `Parse(` → `mimeparse.Parse(`, `ConvertToUTF8(` → `email.ConvertToUTF8(`;
- import: `"github.com/ffois/mailfmt/internal/email"`, `"github.com/ffois/mailfmt/internal/mimeparse"`, `"github.com/ffois/mailfmt/internal/tnef"`.

`internal/eml/eml_test.go` diventa `package eml` e contiene `TestEmbeddedFilename`, con `EmbeddedFile{...}` → `mimeparse.EmbeddedFile{...}`.

- [ ] **Step 6: Aggiorna `alias.go` e `pec_reader.go`**

In `alias.go`:

```go
// ReadEmlFile parses the MIME message at path.
func ReadEmlFile(path string) (*EmailData, error) { return eml.ReadFile(path) }
```

In `pec_reader.go`, `buildEmailData(innerEmail)` diventa `eml.Build(innerEmail)`, e `Parse` resta il wrapper della root. Aggiungi l'import di `internal/eml`.

- [ ] **Step 7: Verifica e commit**

```bash
go vet ./... && go build ./... && go test ./...
git add -A
git commit -m "refactor: move the EML reader into internal/eml

Build is exported so the PEC reader can reuse it.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 7: `internal/pec` — envelope PEC

**Files:**
- Create: `internal/pec/pec.go` (da `pec_reader.go`)
- Delete: `pec_reader.go`
- Modify: `alias.go`

**Interfaces:**
- Consumes: `mimeparse.Parse`, `eml.Build`, `email.Data`
- Produces: `pec.ReadInner(string) (*email.Data, error)`

- [ ] **Step 1: Sposta il file**

```bash
mkdir -p internal/pec
git mv pec_reader.go internal/pec/pec.go
```

In `internal/pec/pec.go`:
- `package mailfmt` → `package pec`;
- `ReadPecInnerEml` → `ReadInner`;
- `Parse(` → `mimeparse.Parse(`;
- `*EmailData` → `*email.Data`;
- import: `"github.com/ffois/mailfmt/internal/email"`, `"github.com/ffois/mailfmt/internal/eml"`, `"github.com/ffois/mailfmt/internal/mimeparse"`.

- [ ] **Step 2: Aggiorna `alias.go`**

```go
// ReadPecInnerEml parses the message carried inside a PEC envelope's
// postacert.eml part.
func ReadPecInnerEml(path string) (*EmailData, error) { return pec.ReadInner(path) }
```

- [ ] **Step 3: Verifica e commit**

```bash
go vet ./... && go build ./... && go test ./...
git add -A
git commit -m "refactor: move the PEC reader into internal/pec

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 8: Facade — `alias.go` completa e guardia di compatibilità

Lo split è finito. Qui si consolida la facade e si aggiunge un test che fallisce a compile time se l'API pubblica cambia.

**Files:**
- Modify: `alias.go` (versione finale)
- Modify: `doc.go`
- Create: `example_test.go`

**Interfaces:**
- Produces: la superficie pubblica completa

- [ ] **Step 1: `alias.go` finale**

```go
package mailfmt

import (
	"io"

	"github.com/ffois/mailfmt/internal/email"
	"github.com/ffois/mailfmt/internal/eml"
	"github.com/ffois/mailfmt/internal/mimeparse"
	"github.com/ffois/mailfmt/internal/msg"
	"github.com/ffois/mailfmt/internal/pec"
)

// The readers live in internal packages, one per format. This file is the
// facade that keeps them reachable under the names this module has always
// exported: type aliases are type identities, not new types, so callers can
// pass a mailfmt.EmailData wherever an email.Data is expected and back.

// EmailData is a message flattened into the shape callers consume.
type EmailData = email.Data

// EmailAttachment is a single decoded attachment.
type EmailAttachment = email.Attachment

// Email is a parsed MIME message with all RFC5322 headers.
type Email = mimeparse.Email

// Attachment is an attachment part of a parsed MIME message.
type Attachment = mimeparse.Attachment

// EmbeddedFile is an inline part of a parsed MIME message, referenced from
// the body via its Content-ID.
type EmbeddedFile = mimeparse.EmbeddedFile

// ReadEmlFile parses the MIME message at path.
func ReadEmlFile(path string) (*EmailData, error) { return eml.ReadFile(path) }

// ReadPecInnerEml parses the message carried inside a PEC envelope's
// postacert.eml part. It fails when the part is missing.
func ReadPecInnerEml(path string) (*EmailData, error) { return pec.ReadInner(path) }

// ReadMsgFile parses the Outlook MSG file at path.
func ReadMsgFile(path string) (*EmailData, error) { return msg.ReadFile(path) }

// Read parses an Outlook MSG message from r.
func Read(r io.ReaderAt) (*EmailData, error) { return msg.Read(r) }

// Parse reads a MIME message from r.
func Parse(r io.Reader) (Email, error) { return mimeparse.Parse(r) }

// ConvertToUTF8 returns s unchanged if it is already valid UTF-8, otherwise
// decodes it from Windows-1252.
func ConvertToUTF8(s string) string { return email.ConvertToUTF8(s) }
```

- [ ] **Step 2: Scrivi la guardia di compatibilità**

Crea `example_test.go`. È in `package mailfmt_test`, quindi vede solo l'API pubblica: se un identificatore sparisce o cambia firma, non compila.

```go
package mailfmt_test

import (
	"io"
	"strings"
	"testing"

	"github.com/ffois/mailfmt"
)

// The package split moved every reader into internal packages. This test
// touches the whole public surface from outside the package, so any rename
// or signature change breaks the build here instead of breaking callers.
func TestPublicAPISurface(t *testing.T) {
	var (
		_ func(string) (*mailfmt.EmailData, mailfmt.EmailFormat, error) = mailfmt.ParseFile
		_ func(string) (mailfmt.EmailFormat, error)                     = mailfmt.DetectEmailFormat
		_ func(string) (*mailfmt.EmailData, error)                      = mailfmt.ReadEmlFile
		_ func(string) (*mailfmt.EmailData, error)                      = mailfmt.ReadPecInnerEml
		_ func(string) (*mailfmt.EmailData, error)                      = mailfmt.ReadMsgFile
		_ func(io.ReaderAt) (*mailfmt.EmailData, error)                 = mailfmt.Read
		_ func(io.Reader) (mailfmt.Email, error)                        = mailfmt.Parse
		_ func(string) string                                           = mailfmt.ConvertToUTF8
	)

	var (
		_ mailfmt.EmailFormat = mailfmt.FormatEML
		_ mailfmt.EmailFormat = mailfmt.FormatMSG
		_ mailfmt.EmailFormat = mailfmt.FormatUnknown
	)

	// The struct fields are part of the surface too.
	data := mailfmt.EmailData{
		From:          "a@example.com",
		To:            []string{"b@example.com"},
		Cc:            nil,
		Bcc:           nil,
		Subject:       "s",
		Body:          "b",
		Attachments:   []mailfmt.EmailAttachment{{Filename: "f", ContentType: "text/plain", Data: []byte("x")}},
		IsPec:         false,
		HasInnerEmail: false,
		Date:          "",
	}
	if data.Attachments[0].Filename != "f" {
		t.Fatalf("unexpected attachment filename %q", data.Attachments[0].Filename)
	}

	parsed, err := mailfmt.Parse(strings.NewReader("Subject: hi\r\n\r\nbody\r\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if parsed.Subject != "hi" {
		t.Errorf("expected subject %q, got %q", "hi", parsed.Subject)
	}

	var (
		_ mailfmt.Attachment   = mailfmt.Attachment{Filename: "f", ContentType: "text/plain"}
		_ mailfmt.EmbeddedFile = mailfmt.EmbeddedFile{CID: "c", Filename: "f"}
	)
}
```

- [ ] **Step 3: Aggiorna `doc.go`**

Aggiungi una frase sulla struttura, mantenendo il resto:

```go
// Package mailfmt reads and parses Outlook and MIME mail formats: EML,
// Outlook MSG (OLE2/CFB compound files), TNEF winmail.dat containers,
// compressed RTF bodies, and Italian PEC (Posta Elettronica Certificata)
// envelopes.
//
// ParseFile is the usual entry point: it detects the format from a file's
// magic bytes and returns a flattened, JSON-friendly EmailData. Callers that
// already know the format, or need lower-level access, can call
// ReadEmlFile, ReadMsgFile, ReadPecInnerEml, or the MIME-level Parse
// directly instead.
//
// Each format is implemented in its own package under internal/, and this
// package is the facade over them. The types below are aliases of the
// internal ones, so they are interchangeable with them.
package mailfmt
```

- [ ] **Step 4: Verifica**

```bash
go vet ./... && go build ./... && go test ./...
```

Atteso: PASS, con `TestPublicAPISurface` fra i test eseguiti.

Controlla anche che non sia rimasto codice nella root oltre alla facade:

```bash
ls *.go
```

Atteso esattamente: `alias.go`, `doc.go`, `example_test.go`, `format_detector.go`, `parse.go`, `parse_test.go`.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor: consolidate the public facade in alias.go

Adds an external test that names every exported identifier and its
signature, so a rename inside the internal packages breaks the build here
rather than breaking callers silently.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 9: Writer CFB per generare fixture `.msg`

Un `.msg` è un compound file OLE2: i dati stanno in property stream binari e non si anonimizzano con una sostituzione testuale. Quindi i fixture MSG si generano.

Il writer deve produrre file che il reader in `internal/msg/msg.go` legge davvero, quindi rispetta i suoi vincoli: settori da 512 byte a partire dall'offset 512, mini-stream con mini-settori da 64 byte per gli stream sotto i 4096 byte, e figli della root collegati come lista tramite `RightSiblingID` (il reader in `collectChildren` visita a sinistra, accoda, poi a destra: con `LeftSiblingID = noStream` l'ordine di inserimento è preservato).

**Files:**
- Create: `internal/msg/cfbwriter_test.go`

**Interfaces:**
- Produces (solo per i test di `internal/msg`): `cfbEntry`, `buildCFB(*cfbEntry) []byte`, `substgName(propID uint32, propType uint16) string`, `utf16LE(string) []byte`

- [ ] **Step 1: Scrivi il test che descrive il round-trip**

`internal/msg/cfbwriter_test.go`, prima parte:

```go
package msg

import (
	"bytes"
	"testing"
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
```

- [ ] **Step 2: Esegui il test e verifica che fallisca**

```bash
go test ./internal/msg/ -run TestCFBWriterRoundTrip -v
```

Atteso: FAIL con `undefined: cfbEntry`, `undefined: substgName`, `undefined: utf16LE`, `undefined: buildCFB`.

- [ ] **Step 3: Implementa il writer**

Aggiungi in coda a `internal/msg/cfbwriter_test.go`, e allarga il blocco `import` già in cima al file con `"encoding/binary"`, `"fmt"` e `"unicode/utf16"` — non aggiungere un secondo blocco `import` a metà file, non compila.

```go
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
```

Gli offset vengono dal layout di `directoryEntry` in `internal/msg/msg.go`: `Name[64]` a 0, `NameLen` a 64, `ObjectType` a 66, `ColorFlag` a 67, `LeftSiblingID` a 68, `RightSiblingID` a 72, `ChildID` a 76, `CLSID[16]` a 80, `StateBits` a 96, `CreationTime` a 100, `ModifiedTime` a 108, `StartingSectorLoc` a 116, `StreamSize` a 120. Totale 128.

- [ ] **Step 4: Esegui il test e verifica che passi**

```bash
go test ./internal/msg/ -run TestCFBWriterRoundTrip -v
```

Atteso: PASS. Se fallisce con `invalid MSG file`, l'header è sbagliato; se `Subject` è vuoto, controlla `substgName` e il mini stream.

- [ ] **Step 5: Aggiungi un test per il percorso a settore regolare**

Uno stream sopra i 4096 byte non passa dal mini stream: è un percorso di codice diverso nel reader e va coperto.

```go
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
```

Aggiungi `"strings"` agli import.

- [ ] **Step 6: Verifica e commit**

```bash
go vet ./... && go build ./... && go test ./...
git add internal/msg/cfbwriter_test.go
git commit -m "test: add a minimal CFB writer for MSG fixtures

A .msg is an OLE2 compound file, so its contents cannot be anonymised by
substituting text. Building fixtures instead gives synthetic files with no
real data and exact control over the edge cases: mini stream versus regular
sectors, unicode versus string8 properties, compressed RTF bodies.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 10: Fixture EML, PEC e TNEF derivati dalle mail reali

I file di partenza sono in `C:\Users\LyzCoote\Desktop\Test mail`. Contengono nomi, indirizzi, codici fiscali, numeri di polizza e referti medici veri: **non entrano nel repository**. Da ciascuno si deriva un fixture con i dati sostituiti e gli allegati troncati.

**Files:**
- Create: `testdata/eml/*.eml`, `testdata/pec/*.eml`, `testdata/tnef/*.eml`
- Create: `internal/fixtures/README.md`

**Interfaces:**
- Produces: i file in `testdata/`, consumati dalla Task 11

- [ ] **Step 1: Ispeziona la struttura MIME dei file reali senza copiarne il contenuto**

```bash
for f in "/c/Users/LyzCoote/Desktop/Test mail"/*.eml; do
  echo "=== $(basename "$f") ==="
  grep -a -m 20 -E '^(Content-Type|Content-Disposition|Content-Transfer-Encoding|Subject|MIME-Version):' "$f"
done
```

Serve a decidere quale file reale è la base migliore per ciascun caso della tabella allo step 3. Non copiare l'output nel repository.

- [ ] **Step 2: Scrivi il README che documenta la provenienza**

`internal/fixtures/README.md`:

```markdown
# Fixture

I file in `testdata/` sono derivati da messaggi reali, non copiati.

Da ogni messaggio di partenza è stata mantenuta la struttura MIME — confini,
header di parte, codifiche, ordine delle parti — e sostituito tutto il resto:

- nomi, indirizzi email, numeri di telefono, codici fiscali e numeri
  identificativi rimpiazzati con valori inventati;
- corpi dei messaggi riscritti;
- allegati troncati a pochi byte, mantenendo header e codifica.

I messaggi di partenza contengono dati personali e non sono versionati.

I fixture `.msg` in `testdata/msg/` non sono derivati: sono generati dal
writer CFB in `internal/msg/cfbwriter_test.go`, perché un compound file OLE2
non si anonimizza in modo affidabile con una sostituzione testuale. Per
rigenerarli: `go test ./internal/msg/ -run TestGenerateMSGTestdata -update`.
```

- [ ] **Step 3: Crea i fixture EML**

Per ciascuno, parti dal file reale corrispondente, sostituisci i dati e tronca gli allegati. Ogni fixture deve restare sotto i 50 KB.

| File | Base | Cosa deve esercitare |
|---|---|---|
| `testdata/eml/plain.eml` | `msg-17880258090950209 (2).eml` | `text/plain` senza parti multiple |
| `testdata/eml/alternative.eml` | `mail.eml` | `multipart/alternative`, l'HTML deve vincere sul testo |
| `testdata/eml/mixed-attachment.eml` | `CamScanner ... .pdf.eml` | `multipart/mixed` con un allegato PDF |
| `testdata/eml/related-inline.eml` | `idk.eml` | `multipart/related`, `cid:` sostituito da data URI |
| `testdata/eml/encoded-headers.eml` | `Rimborso premio ... .eml` | Subject e From in `=?utf-8?B?` e `=?iso-8859-1?Q?` |
| `testdata/eml/latin1-body.eml` | `Richiesta visita ... .eml` | corpo `charset=iso-8859-1` con accenti |

Verifica che ogni fixture si legga:

```bash
cat > /tmp/check_fixture.go <<'EOF'
package main

import (
	"fmt"
	"os"

	"github.com/ffois/mailfmt"
)

func main() {
	d, f, err := mailfmt.ParseFile(os.Args[1])
	if err != nil {
		fmt.Println("ERROR:", err)
		os.Exit(1)
	}
	fmt.Printf("format=%s subject=%q attachments=%d isPec=%v bodyLen=%d\n",
		f, d.Subject, len(d.Attachments), d.IsPec, len(d.Body))
}
EOF
go run /tmp/check_fixture.go testdata/eml/plain.eml
```

Ripeti per ognuno. Se uno dà errore o zero allegati dove te ne aspetti uno, il fixture è rotto: correggilo prima di proseguire.

- [ ] **Step 4: Crea i fixture PEC**

| File | Base | Cosa deve esercitare |
|---|---|---|
| `testdata/pec/envelope.eml` | `postacert.eml` | envelope con `postacert.eml`: `ParseFile` deve restituire il messaggio interno |
| `testdata/pec/signed.eml` | `postacert.eml` | envelope con `daticert.xml` e `smime.p7s`: `IsPec` deve essere `true` |
| `testdata/pec/no-postacert.eml` | costruito a mano | envelope senza `postacert.eml`: `ReadPecInnerEml` deve fallire, `ParseFile` deve ripiegare su EML |
| `testdata/pec/nested.eml` | costruito a mano | `postacert.eml` che a sua volta contiene un `.eml`: `HasInnerEmail` deve essere `true` |

- [ ] **Step 5: Crea i fixture TNEF**

Un `winmail.dat` va costruito, non derivato. Il formato TNEF inizia con la firma `78 9F 3E 22` seguita da una chiave a 16 bit e da una sequenza di attributi. Usa la libreria già in dipendenza per verificare che quello che costruisci sia leggibile.

| File | Cosa deve esercitare |
|---|---|
| `testdata/tnef/winmail.eml` | EML con allegato `winmail.dat` che contiene due file: `tnef.Expand` deve sostituirlo con i due |
| `testdata/tnef/not-tnef.eml` | EML con un allegato `winmail.dat` che **non** ha la firma TNEF: deve restare intatto |

Il caso TNEF annidato è coperto dai test unitari alla Task 15, non da un fixture su file.

- [ ] **Step 6: Verifica che nessun dato reale sia finito nel repo**

```bash
grep -ril -E 'cavigli|CVGRCR81S13D612O|10002113226|TASK0541832|genesys' testdata/ || echo "pulito"
```

Atteso: `pulito`. Se trova qualcosa, il fixture non è stato anonimizzato: rifallo.

- [ ] **Step 7: Commit**

```bash
git add testdata internal/fixtures/README.md
git commit -m "test: add EML, PEC and TNEF fixtures derived from real mail

Structure preserved, contents replaced: names, addresses, tax codes and
policy numbers are invented, bodies rewritten, attachments truncated to a
few bytes. The source messages hold real personal data and stay out of the
repository; internal/fixtures/README.md records how the derivation works.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 11: Golden test e generazione dei fixture MSG

**Files:**
- Create: `golden_test.go`
- Create: `internal/msg/gen_testdata_test.go`
- Create: `testdata/msg/*.msg`, `testdata/*/*.golden.json`

**Interfaces:**
- Consumes: `mailfmt.ParseFile`, i fixture della Task 10, `buildCFB` della Task 9
- Produces: i file `.golden.json`

- [ ] **Step 1: Genera i fixture MSG**

`internal/msg/gen_testdata_test.go`:

```go
package msg

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateTestdata = flag.Bool("update", false, "rewrite the generated .msg fixtures")

// The .msg fixtures are generated rather than derived from real mail: a
// compound file cannot be anonymised by substituting text. This test writes
// them when -update is passed and otherwise checks they are still in sync,
// so a change to the writer cannot silently diverge from what is committed.
func TestGenerateMSGTestdata(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "msg")

	cases := map[string]func() []byte{
		"unicode-props.msg":   fixtureUnicodeProps,
		"string8-props.msg":   fixtureString8Props,
		"with-attachment.msg": fixtureWithAttachment,
		"large-body.msg":      fixtureLargeBody,
	}

	for name, build := range cases {
		path := filepath.Join(dir, name)
		want := build()

		if *updateTestdata {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", dir, err)
			}
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
			continue
		}

		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v (run with -update to generate)", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is out of date; rerun with -update", name)
		}
	}
}

// A message with PT_UNICODE properties, the common case.
func fixtureUnicodeProps() []byte {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("Preventivo n. 4471"))
	root.addStream(substgName(pidTagSenderName, propTypeString), utf16LE("Anna Ferri"))
	root.addStream(substgName(pidTagSenderEmailAddress, propTypeString), utf16LE("anna.ferri@example.it"))
	root.addStream(substgName(pidTagDisplayTo, propTypeString), utf16LE("ufficio@example.it"))
	root.addStream(substgName(pidTagDisplayCc, propTypeString), utf16LE("copia@example.it; altra@example.it"))
	root.addStream(substgName(pidTagBodyHTML, propTypeString), utf16LE("<html><body>In allegato il preventivo.</body></html>"))
	return buildCFB(root)
}

// PT_STRING8 properties hold single-byte text; accents come back through the
// Windows-1252 fallback in ConvertToUTF8.
func fixtureString8Props() []byte {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString8), []byte{
		'P', 'e', 'r', 'c', 'h', 0xE9, ' ', 'n', 'o', // "Perché no" in cp1252
	})
	root.addStream(substgName(pidTagBody, propTypeString8), []byte("Testo semplice\r\ncon due righe"))
	return buildCFB(root)
}

// An attachment is a storage under the root, holding its own property streams.
func fixtureWithAttachment() []byte {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("Con allegato"))
	root.addStream(substgName(pidTagBodyHTML, propTypeString), utf16LE("<html><body>Vedi allegato.</body></html>"))

	att := root.addStorage("__attach_version1.0_#00000000")
	att.addStream(substgName(pidTagAttachLongFilename, propTypeString), utf16LE("nota.txt"))
	att.addStream(substgName(pidTagAttachMimeTag, propTypeString), utf16LE("text/plain"))
	att.addStream(substgName(pidTagAttachData, propTypeBinary), []byte("contenuto della nota"))
	return buildCFB(root)
}

// A body above the 4096-byte cutoff exercises the regular-sector read path.
func fixtureLargeBody() []byte {
	body := "<html><body>" + strings.Repeat("riga di testo<br>", 400) + "</body></html>"
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("Corpo lungo"))
	root.addStream(substgName(pidTagBodyHTML, propTypeString8), []byte(body))
	return buildCFB(root)
}
```

Aggiungi `"bytes"` e `"strings"` agli import.

```bash
go test ./internal/msg/ -run TestGenerateMSGTestdata -update
ls -la testdata/msg/
```

Atteso: quattro file `.msg`.

- [ ] **Step 2: Scrivi il golden test**

`golden_test.go`:

```go
package mailfmt_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ffois/mailfmt"
)

var update = flag.Bool("update", false, "rewrite the .golden.json files")

// Every fixture under testdata/ is parsed through ParseFile and compared
// against a committed snapshot of the result. This covers format detection,
// the dispatch in ParseFile and each reader end to end, so a change in any of
// them shows up as a diff rather than as silence.
func TestGoldenFixtures(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "*", "*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no fixtures found under testdata/")
	}

	for _, path := range paths {
		if strings.HasSuffix(path, ".golden.json") {
			continue
		}

		t.Run(filepath.ToSlash(path), func(t *testing.T) {
			data, format, err := mailfmt.ParseFile(path)

			snapshot := map[string]any{"format": string(format)}
			if err != nil {
				snapshot["error"] = err.Error()
			} else {
				snapshot["email"] = summarise(data)
			}

			got, mErr := json.MarshalIndent(snapshot, "", "  ")
			if mErr != nil {
				t.Fatalf("marshal: %v", mErr)
			}
			got = append(got, '\n')

			goldenPath := path + ".golden.json"
			if *update {
				if wErr := os.WriteFile(goldenPath, got, 0o644); wErr != nil {
					t.Fatalf("write golden: %v", wErr)
				}
				return
			}

			want, rErr := os.ReadFile(goldenPath)
			if rErr != nil {
				t.Fatalf("read golden: %v (run with -update to create it)", rErr)
			}
			if string(got) != string(want) {
				t.Errorf("golden mismatch for %s\n got=%s\nwant=%s", path, got, want)
			}
		})
	}
}

// summarise keeps the fields that describe the parse and drops attachment
// bytes, which would make the snapshots huge and unreadable. Their length is
// kept so a change in decoding still shows up.
func summarise(d *mailfmt.EmailData) map[string]any {
	atts := make([]map[string]any, 0, len(d.Attachments))
	for _, a := range d.Attachments {
		atts = append(atts, map[string]any{
			"filename":    a.Filename,
			"contentType": a.ContentType,
			"size":        len(a.Data),
		})
	}
	return map[string]any{
		"from":          d.From,
		"to":            d.To,
		"cc":            d.Cc,
		"bcc":           d.Bcc,
		"subject":       d.Subject,
		"bodyLen":       len(d.Body),
		"bodyPrefix":    firstRunes(d.Body, 120),
		"attachments":   atts,
		"isPec":         d.IsPec,
		"hasInnerEmail": d.HasInnerEmail,
		"date":          d.Date,
	}
}

func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
```

- [ ] **Step 3: Genera i golden e ispezionali**

```bash
go test . -run TestGoldenFixtures -update
git status --short testdata/
```

Poi **leggi ogni `.golden.json` generato**. Sono la definizione del comportamento atteso: se uno contiene un subject vuoto dove ne aspetti uno, o zero allegati dove ce n'è uno, il parser ha un problema o il fixture è rotto. Non committare golden che non hai verificato.

- [ ] **Step 4: Verifica che il golden test fallisca su una modifica**

```bash
go test . -run TestGoldenFixtures
```

Atteso: PASS. Poi verifica che il test morda davvero: modifica temporaneamente un `.golden.json` (cambia un subject), rilancia e controlla che fallisca, poi ripristina con `git checkout`.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "test: add golden tests over the per-format fixtures

Each fixture under testdata/ goes through ParseFile and is compared with a
committed snapshot, so format detection, dispatch and every reader are
covered end to end. Attachment bytes are reduced to their length to keep the
snapshots readable.

The .msg fixtures are generated by the CFB writer; TestGenerateMSGTestdata
fails when they drift out of sync with it.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 12: Test unitari `internal/eml`

**Files:**
- Modify: `internal/eml/eml_test.go`

**Interfaces:**
- Consumes: `eml.ReadFile`, `eml.Build`, `mimeparse.Parse`

- [ ] **Step 1: Scrivi i test**

Aggiungi a `internal/eml/eml_test.go`, che già contiene `TestEmbeddedFilename`:

```go
// writeEML writes a message to a temp file and returns its path.
func writeEML(t *testing.T, raw string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "message.eml")
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// With both a text and an HTML alternative, the HTML one is what callers get:
// it is the richer rendering, and the text part is a fallback for clients
// that cannot display it.
func TestBuildPrefersHTMLBody(t *testing.T) {
	raw := "From: a@example.com\r\n" +
		"To: b@example.com\r\n" +
		"Subject: Alternative\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=\"alt\"\r\n" +
		"\r\n" +
		"--alt\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"versione testo\r\n" +
		"--alt\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<p>versione html</p>\r\n" +
		"--alt--\r\n"

	data, err := ReadFile(writeEML(t, raw))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(data.Body, "versione html") {
		t.Errorf("expected the HTML alternative in the body, got %q", data.Body)
	}
	if strings.Contains(data.Body, "versione testo") {
		t.Errorf("text alternative leaked into the body: %q", data.Body)
	}
}

// An inline image is inlined into the body as a data URI so the message
// renders standalone, and is also listed as an attachment so callers that
// only walk Attachments still see it.
func TestBuildInlinesCIDReferences(t *testing.T) {
	raw := "From: a@example.com\r\n" +
		"To: b@example.com\r\n" +
		"Subject: Inline\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/related; boundary=\"rel\"\r\n" +
		"\r\n" +
		"--rel\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<img src=\"cid:logo123\">\r\n" +
		"--rel\r\n" +
		"Content-Type: image/png; name=\"logo.png\"\r\n" +
		"Content-Disposition: inline; filename=\"logo.png\"\r\n" +
		"Content-Id: <logo123>\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"iVBORw0KGgo=\r\n" +
		"--rel--\r\n"

	data, err := ReadFile(writeEML(t, raw))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(data.Body, "cid:logo123") {
		t.Errorf("cid: reference left unresolved in the body: %q", data.Body)
	}
	if !strings.Contains(data.Body, "data:image/png;base64,") {
		t.Errorf("inline image was not substituted with a data URI: %q", data.Body)
	}
	if len(data.Attachments) != 1 {
		t.Fatalf("expected the inline image to be listed as an attachment, got %d", len(data.Attachments))
	}
	if data.Attachments[0].Filename != "logo.png" {
		t.Errorf("expected filename %q, got %q", "logo.png", data.Attachments[0].Filename)
	}
}

// A message is a PEC envelope only when it carries both daticert.xml and
// smime.p7s. Either one alone is not enough.
func TestBuildFlagsPecOnlyWithBothParts(t *testing.T) {
	envelope := func(extraPart string) string {
		return "From: posta-certificata@pec.it\r\n" +
			"To: dest@pec.it\r\n" +
			"Subject: POSTA CERTIFICATA\r\n" +
			"MIME-Version: 1.0\r\n" +
			"Content-Type: multipart/mixed; boundary=\"b\"\r\n" +
			"\r\n" +
			"--b\r\n" +
			"Content-Type: text/plain; charset=utf-8\r\n" +
			"\r\n" +
			"ricevuta\r\n" +
			extraPart +
			"--b--\r\n"
	}
	datiCert := "--b\r\n" +
		"Content-Type: application/xml\r\n" +
		"Content-Disposition: attachment; filename=\"daticert.xml\"\r\n" +
		"\r\n" +
		"<postacert/>\r\n"
	smime := "--b\r\n" +
		"Content-Type: application/pkcs7-signature\r\n" +
		"Content-Disposition: attachment; filename=\"smime.p7s\"\r\n" +
		"\r\n" +
		"sig\r\n"

	cases := []struct {
		name  string
		parts string
		want  bool
	}{
		{"both parts", datiCert + smime, true},
		{"only daticert", datiCert, false},
		{"only smime", smime, false},
		{"neither", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := ReadFile(writeEML(t, envelope(tc.parts)))
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if data.IsPec != tc.want {
				t.Errorf("expected IsPec=%v, got %v", tc.want, data.IsPec)
			}
		})
	}
}

// Headers encoded per RFC 2047 must be decoded, whatever the charset and
// whichever of the two encodings is used.
func TestReadFileDecodesEncodedHeaders(t *testing.T) {
	raw := "From: =?utf-8?B?TWFyaW8gUm9zc2k=?= <mario@example.it>\r\n" +
		"To: b@example.it\r\n" +
		"Subject: =?iso-8859-1?Q?Perch=E9_no=3F?=\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"corpo\r\n"

	data, err := ReadFile(writeEML(t, raw))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if data.Subject != "Perché no?" {
		t.Errorf("expected subject %q, got %q", "Perché no?", data.Subject)
	}
	if !strings.Contains(data.From, "Mario Rossi") {
		t.Errorf("expected the decoded display name in From, got %q", data.From)
	}
}

// A body declared as iso-8859-1 has to come back as valid UTF-8.
func TestReadFileDecodesLatin1Body(t *testing.T) {
	raw := "From: a@example.it\r\n" +
		"To: b@example.it\r\n" +
		"Subject: Latin1\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=iso-8859-1\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n" +
		"\r\n" +
		"Caff\xe8 corretto\r\n"

	data, err := ReadFile(writeEML(t, raw))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !utf8.ValidString(data.Body) {
		t.Fatalf("body is not valid UTF-8: %q", data.Body)
	}
	if !strings.Contains(data.Body, "Caffè") {
		t.Errorf("expected %q in the body, got %q", "Caffè", data.Body)
	}
}

// A missing file is an error, not a panic or an empty result.
func TestReadFileMissing(t *testing.T) {
	data, err := ReadFile(filepath.Join(t.TempDir(), "nope.eml"))
	if err == nil {
		t.Fatal("expected an error for a missing file, got nil")
	}
	if data != nil {
		t.Errorf("expected nil data on error, got %+v", data)
	}
}
```

Import necessari: `os`, `path/filepath`, `strings`, `testing`, `unicode/utf8`.

- [ ] **Step 2: Esegui**

```bash
go test ./internal/eml/ -v
```

Atteso: PASS su tutti. Se `TestReadFileDecodesLatin1Body` fallisce, è un bug reale del parser: **non correggerlo**. Marcalo con `t.Skip` e un commento che descrive il comportamento osservato, e annotalo per il riepilogo finale.

- [ ] **Step 3: Commit**

```bash
git add internal/eml/eml_test.go
git commit -m "test: cover the EML reader

Body preference between alternatives, cid: inlining, PEC detection needing
both daticert.xml and smime.p7s, RFC 2047 headers, latin1 bodies, missing
files.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 13: Test unitari `internal/msg`

**Files:**
- Create: `internal/msg/msg_test.go`

**Interfaces:**
- Consumes: `buildCFB`, `cfbEntry`, `substgName`, `utf16LE` dalla Task 9

- [ ] **Step 1: Scrivi i test**

`internal/msg/msg_test.go`:

```go
package msg

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

// readFixture builds a message and parses it, failing the test on error.
func readFixture(t *testing.T, root *cfbEntry) *email.Data {
	t.Helper()
	data, err := Read(bytes.NewReader(buildCFB(root)))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return data
}

// PT_UNICODE properties are UTF-16LE; recipients arrive as one
// semicolon-separated string and have to be split.
func TestReadUnicodeProperties(t *testing.T) {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("Preventivo"))
	root.addStream(substgName(pidTagSenderName, propTypeString), utf16LE("Anna Ferri"))
	root.addStream(substgName(pidTagSenderEmailAddress, propTypeString), utf16LE("anna@example.it"))
	root.addStream(substgName(pidTagDisplayTo, propTypeString), utf16LE("uno@example.it; due@example.it"))

	data := readFixture(t, root)

	if data.Subject != "Preventivo" {
		t.Errorf("expected subject %q, got %q", "Preventivo", data.Subject)
	}
	if data.From != "Anna Ferri <anna@example.it>" {
		t.Errorf("expected From %q, got %q", "Anna Ferri <anna@example.it>", data.From)
	}
	if len(data.To) != 2 {
		t.Fatalf("expected 2 recipients, got %d: %v", len(data.To), data.To)
	}
}

// PT_STRING8 properties hold single-byte text. Outlook writes them in the
// sender's ANSI code page, so a byte above 0x7F has to come back as UTF-8.
func TestReadString8Properties(t *testing.T) {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString8),
		[]byte{'P', 'e', 'r', 'c', 'h', 0xE9})

	data := readFixture(t, root)

	if !utf8.ValidString(data.Subject) {
		t.Fatalf("subject is not valid UTF-8: %q", data.Subject)
	}
	if data.Subject != "Perché" {
		t.Errorf("expected subject %q, got %q", "Perché", data.Subject)
	}
}

// With no subject of its own, a message falls back to the conversation topic.
func TestReadFallsBackToConversationTopic(t *testing.T) {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagConversationTopic, propTypeString), utf16LE("Discussione"))

	if got := readFixture(t, root).Subject; got != "Discussione" {
		t.Errorf("expected subject %q, got %q", "Discussione", got)
	}
}

// With no HTML body, the plain-text body is escaped and its newlines turned
// into <br>, so callers always get HTML.
func TestReadConvertsTextBodyToHTML(t *testing.T) {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagBody, propTypeString), utf16LE("prima riga\r\nsecondo <b>"))

	body := readFixture(t, root).Body

	if !strings.Contains(body, "<br>") {
		t.Errorf("expected newlines to become <br>, got %q", body)
	}
	if !strings.Contains(body, "&lt;b&gt;") {
		t.Errorf("expected the literal tag to be escaped, got %q", body)
	}
}

// An attachment lives in its own storage under the root, with its filename,
// MIME type and data in separate property streams.
func TestReadAttachment(t *testing.T) {
	root := &cfbEntry{name: "Root Entry"}
	root.addStream(substgName(pidTagSubject, propTypeString), utf16LE("Con allegato"))
	att := root.addStorage("__attach_version1.0_#00000000")
	att.addStream(substgName(pidTagAttachLongFilename, propTypeString), utf16LE("nota.txt"))
	att.addStream(substgName(pidTagAttachMimeTag, propTypeString), utf16LE("text/plain"))
	att.addStream(substgName(pidTagAttachData, propTypeBinary), []byte("contenuto"))

	data := readFixture(t, root)

	if len(data.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(data.Attachments))
	}
	got := data.Attachments[0]
	if got.Filename != "nota.txt" {
		t.Errorf("expected filename %q, got %q", "nota.txt", got.Filename)
	}
	if got.ContentType != "text/plain" {
		t.Errorf("expected content type %q, got %q", "text/plain", got.ContentType)
	}
	if string(got.Data) != "contenuto" {
		t.Errorf("expected data %q, got %q", "contenuto", got.Data)
	}
}

// The long filename wins over the short one when both are present.
func TestReadAttachmentPrefersLongFilename(t *testing.T) {
	root := &cfbEntry{name: "Root Entry"}
	att := root.addStorage("__attach_version1.0_#00000000")
	att.addStream(substgName(pidTagAttachFilename, propTypeString), utf16LE("NOTA~1.TXT"))
	att.addStream(substgName(pidTagAttachLongFilename, propTypeString), utf16LE("nota lunga.txt"))
	att.addStream(substgName(pidTagAttachData, propTypeBinary), []byte("x"))

	data := readFixture(t, root)

	if len(data.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(data.Attachments))
	}
	if data.Attachments[0].Filename != "nota lunga.txt" {
		t.Errorf("expected the long filename, got %q", data.Attachments[0].Filename)
	}
}

// Anything that is not a compound file is an error, not a panic.
func TestReadRejectsNonMSG(t *testing.T) {
	if _, err := Read(bytes.NewReader([]byte("From: a@example.com\r\n\r\nnot a msg"))); err == nil {
		t.Fatal("expected an error for a non-MSG input, got nil")
	}
}
```

Aggiungi l'import `"github.com/ffois/mailfmt/internal/email"`.

- [ ] **Step 2: Esegui**

```bash
go test ./internal/msg/ -v
```

Atteso: PASS. `TestReadMSGInlineImagesE2E` resta SKIP.

- [ ] **Step 3: Commit**

```bash
git add internal/msg/msg_test.go
git commit -m "test: cover the MSG reader

Unicode and string8 properties, subject fallback to the conversation topic,
text-to-HTML body conversion, attachments and filename preference, and
rejection of non-compound input.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 14: Test unitari `internal/pec`

**Files:**
- Create: `internal/pec/pec_test.go`

- [ ] **Step 1: Scrivi i test**

`internal/pec/pec_test.go`:

```go
package pec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeEML(t *testing.T, raw string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "envelope.eml")
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// envelopeWith wraps an inner message in a PEC envelope carrying it as
// postacert.eml, plus any extra parts given.
func envelopeWith(inner, extraParts string) string {
	return "From: posta-certificata@pec.it\r\n" +
		"To: dest@pec.it\r\n" +
		"Subject: POSTA CERTIFICATA: originale\r\n" +
		"Date: Mon, 02 Jun 2025 10:00:00 +0200\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b\"\r\n" +
		"\r\n" +
		"--b\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Messaggio di posta certificata\r\n" +
		"--b\r\n" +
		"Content-Type: message/rfc822\r\n" +
		"Content-Disposition: attachment; filename=\"postacert.eml\"\r\n" +
		"\r\n" +
		inner +
		extraParts +
		"--b--\r\n"
}

const innerMessage = "From: mittente <mittente@example.it>\r\n" +
	"To: destinatario@example.it\r\n" +
	"Subject: Oggetto reale\r\n" +
	"Date: Mon, 02 Jun 2025 09:59:00 +0200\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"contenuto reale\r\n"

// The envelope is packaging: what callers want is the message inside it, with
// the inner headers, not the "POSTA CERTIFICATA:" ones.
func TestReadInnerReturnsTheInnerMessage(t *testing.T) {
	data, err := ReadInner(writeEML(t, envelopeWith(innerMessage, "")))
	if err != nil {
		t.Fatalf("ReadInner: %v", err)
	}
	if data.Subject != "Oggetto reale" {
		t.Errorf("expected the inner subject %q, got %q", "Oggetto reale", data.Subject)
	}
	if !strings.Contains(data.From, "mittente@example.it") {
		t.Errorf("expected the inner sender, got %q", data.From)
	}
	if !strings.Contains(data.Body, "contenuto reale") {
		t.Errorf("expected the inner body, got %q", data.Body)
	}
	if data.Date != "Mon, 02 Jun 2025 09:59:00 +0200" {
		t.Errorf("expected the inner Date header, got %q", data.Date)
	}
}

// Without a postacert.eml part there is no inner message to return, and the
// caller has to be told so it can fall back to reading the file as plain EML.
func TestReadInnerFailsWithoutPostacert(t *testing.T) {
	raw := "From: a@example.it\r\n" +
		"To: b@example.it\r\n" +
		"Subject: Non una PEC\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"corpo\r\n"

	data, err := ReadInner(writeEML(t, raw))
	if err == nil {
		t.Fatal("expected an error when postacert.eml is missing, got nil")
	}
	if data != nil {
		t.Errorf("expected nil data on error, got %+v", data)
	}
}

// The part name is matched case-insensitively: senders are not consistent
// about it.
func TestReadInnerMatchesPostacertCaseInsensitively(t *testing.T) {
	envelope := strings.Replace(
		envelopeWith(innerMessage, ""),
		`filename="postacert.eml"`,
		`filename="PostaCert.EML"`,
		1,
	)

	data, err := ReadInner(writeEML(t, envelope))
	if err != nil {
		t.Fatalf("ReadInner: %v", err)
	}
	if data.Subject != "Oggetto reale" {
		t.Errorf("expected the inner subject %q, got %q", "Oggetto reale", data.Subject)
	}
}

// When the inner message itself carries an .eml attachment, HasInnerEmail
// flags it so callers know there is another level to descend.
func TestReadInnerFlagsNestedEmail(t *testing.T) {
	nested := "From: mittente <mittente@example.it>\r\n" +
		"To: destinatario@example.it\r\n" +
		"Subject: Con inoltro\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"inner\"\r\n" +
		"\r\n" +
		"--inner\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"in allegato l'originale\r\n" +
		"--inner\r\n" +
		"Content-Type: message/rfc822\r\n" +
		"Content-Disposition: attachment; filename=\"originale.eml\"\r\n" +
		"\r\n" +
		"From: terzo@example.it\r\n" +
		"Subject: Terzo livello\r\n" +
		"\r\n" +
		"testo\r\n" +
		"--inner--\r\n"

	data, err := ReadInner(writeEML(t, envelopeWith(nested, "")))
	if err != nil {
		t.Fatalf("ReadInner: %v", err)
	}
	if !data.HasInnerEmail {
		t.Error("expected HasInnerEmail to be set for a nested .eml attachment")
	}
}
```

- [ ] **Step 2: Esegui**

```bash
go test ./internal/pec/ -v
```

Atteso: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/pec/pec_test.go
git commit -m "test: cover the PEC reader

Inner message extraction with inner headers, failure without postacert.eml,
case-insensitive part matching, nested .eml flagging.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 15: Test unitari `internal/tnef`

**Files:**
- Create: `internal/tnef/tnef_test.go`

- [ ] **Step 1: Scrivi un costruttore TNEF nei test**

Serve per produrre blob validi senza committare binari.

```go
package tnef

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/ffois/mailfmt/internal/email"
)

// buildTNEF assembles a minimal winmail.dat holding the given files. The
// format is a signature, a key, then a sequence of attributes; attachments are
// an AttachTitle followed by an AttachData in the attachment level.
func buildTNEF(files map[string][]byte) []byte {
	var buf bytes.Buffer
	buf.Write(tnefMagic)
	binary.Write(&buf, binary.LittleEndian, uint16(0x0001)) // key

	writeAttr := func(level byte, id uint16, typ uint16, data []byte) {
		buf.WriteByte(level)
		binary.Write(&buf, binary.LittleEndian, id)
		binary.Write(&buf, binary.LittleEndian, typ)
		binary.Write(&buf, binary.LittleEndian, uint32(len(data)))
		buf.Write(data)
		var sum uint16
		for _, b := range data {
			sum += uint16(b)
		}
		binary.Write(&buf, binary.LittleEndian, sum)
	}

	for name, content := range files {
		writeAttr(2, 0x8010, 0x0006, append([]byte(name), 0)) // attAttachTitle
		writeAttr(2, 0x800F, 0x000D, content)                 // attAttachData
	}
	return buf.Bytes()
}

// The signature is what distinguishes a real winmail.dat from a file that
// merely has the name.
func TestIsTNEFData(t *testing.T) {
	if !IsTNEFData(buildTNEF(map[string][]byte{"a.txt": []byte("x")})) {
		t.Error("a generated TNEF blob was not recognised")
	}
	if IsTNEFData([]byte("From: a@example.com\r\n\r\nnope")) {
		t.Error("a plain message was recognised as TNEF")
	}
	if IsTNEFData(nil) {
		t.Error("nil was recognised as TNEF")
	}
	if IsTNEFData([]byte{0x78}) {
		t.Error("a one-byte input was recognised as TNEF")
	}
}

// A winmail.dat is a container: callers want the files inside it, not the
// container, so Expand replaces it with its contents.
func TestExpandReplacesContainer(t *testing.T) {
	blob := buildTNEF(map[string][]byte{
		"relazione.pdf": []byte("%PDF-1.4 finto"),
		"nota.txt":      []byte("appunti"),
	})

	got := Expand([]email.Attachment{
		{Filename: "documento.pdf", ContentType: "application/pdf", Data: []byte("%PDF altro")},
		{Filename: "winmail.dat", ContentType: "application/ms-tnef", Data: blob},
	})

	names := map[string]bool{}
	for _, a := range got {
		names[a.Filename] = true
	}
	if names["winmail.dat"] {
		t.Error("the winmail.dat container was left in the attachment list")
	}
	if !names["documento.pdf"] {
		t.Error("the unrelated attachment was dropped")
	}
	if !names["relazione.pdf"] || !names["nota.txt"] {
		t.Errorf("expected both extracted files, got %v", names)
	}
}

// An attachment named winmail.dat without the TNEF signature is just a file
// with an unlucky name and has to survive untouched.
func TestExpandLeavesNonTNEFAlone(t *testing.T) {
	original := []email.Attachment{
		{Filename: "winmail.dat", ContentType: "application/octet-stream", Data: []byte("solo testo")},
	}

	got := Expand(original)

	if len(got) != 1 {
		t.Fatalf("expected the attachment to survive, got %d", len(got))
	}
	if got[0].Filename != "winmail.dat" || string(got[0].Data) != "solo testo" {
		t.Errorf("attachment was modified: %+v", got[0])
	}
}

// Extract on garbage must return an error rather than partial results.
func TestExtractRejectsGarbage(t *testing.T) {
	if _, err := Extract([]byte("non è un tnef")); err == nil {
		t.Fatal("expected an error for non-TNEF input, got nil")
	}
}
```

- [ ] **Step 2: Esegui**

```bash
go test ./internal/tnef/ -v
```

Atteso: PASS. Se `buildTNEF` non produce blob che `github.com/teamwork/tnef` accetta, aggiusta gli ID degli attributi confrontandoli con la libreria:

```bash
go doc github.com/teamwork/tnef
find "$(go env GOMODCACHE)/github.com/teamwork" -name '*.go' | head
```

- [ ] **Step 3: Commit**

```bash
git add internal/tnef/tnef_test.go
git commit -m "test: cover TNEF extraction

Signature detection, container replacement in the attachment list, an
unlucky-named non-TNEF file left alone, garbage rejected.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 16: Completa i test `internal/rtf`

I tre test spostati alla Task 3 coprono la de-encapsulation e il passthrough non compresso. Manca il percorso compresso vero, che è il più usato.

**Files:**
- Modify: `internal/rtf/rtf_test.go`

- [ ] **Step 1: Scrivi il test sul percorso compresso**

Il modo affidabile di ottenere uno stream LZFu valido è comprimerlo, quindi il test include un compressore minimale: usa solo letterali, che il formato ammette (ogni byte di controllo con bit a 0 introduce otto letterali).

```go
// compressRTF produces a valid LZFu stream that uses literals only. The format
// allows it: a control byte with a zero bit means "the next byte is a literal",
// so a stream of zero control bytes is a correct, if useless, encoding. That is
// enough to exercise the decompressor's framing and dictionary handling.
func compressRTF(payload []byte) []byte {
	var body []byte
	for i := 0; i < len(payload); i += 8 {
		body = append(body, 0x00) // eight literals follow
		end := i + 8
		if end > len(payload) {
			end = len(payload)
		}
		body = append(body, payload[i:end]...)
	}

	out := make([]byte, 16+len(body))
	binary.LittleEndian.PutUint32(out[0:], uint32(len(body)+12)) // compSize
	binary.LittleEndian.PutUint32(out[4:], uint32(len(payload))) // rawSize
	binary.LittleEndian.PutUint32(out[8:], RTFCompressedMagic)
	binary.LittleEndian.PutUint32(out[12:], 0) // CRC, not checked
	copy(out[16:], body)
	return out
}

// The compressed path is the one Outlook actually uses. A round trip through
// it must give back exactly the original RTF.
func TestDecompressRTFCompressed(t *testing.T) {
	payload := []byte(`{\rtf1\ansi\fromhtml1{\*\htmltag <p>ciao</p>}}`)

	got, err := Decompress(compressRTF(payload))
	if err != nil {
		t.Fatalf("Decompress: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("round trip mismatch:\n got=%q\nwant=%q", got, payload)
	}
}

// A truncated stream is an error, not a panic or a partial result.
func TestDecompressRTFTruncated(t *testing.T) {
	full := compressRTF([]byte(`{\rtf1\fromhtml1 testo}`))

	if _, err := Decompress(full[:8]); err == nil {
		t.Error("expected an error for a stream cut inside the header, got nil")
	}
	if _, err := Decompress(full[:len(full)-4]); err == nil {
		t.Error("expected an error for a stream cut inside the body, got nil")
	}
}

// Only RTF marked \fromhtml carries HTML. Genuine RTF must be reported as
// such so the caller falls back to the plain-text body.
func TestIsEncapsulatedHTML(t *testing.T) {
	if !IsEncapsulatedHTML([]byte(`{\rtf1\ansi\fromhtml1{\*\htmltag <p>}}`)) {
		t.Error("encapsulated HTML was not recognised")
	}
	if IsEncapsulatedHTML([]byte(`{\rtf1\ansi\pard Testo normale.\par}`)) {
		t.Error("genuine RTF was recognised as encapsulated HTML")
	}
}
```

Aggiungi `"bytes"` agli import.

- [ ] **Step 2: Esegui**

```bash
go test ./internal/rtf/ -v
```

Atteso: PASS. Se `TestDecompressRTFCompressed` fallisce, controlla il formato del control byte confrontandolo con `Decompress` in `internal/rtf/rtf.go`: nell'implementazione un bit a 0 potrebbe indicare il caso opposto. In quel caso inverti il byte di controllo a `0xFF` e riprova, non toccare il decompressore.

- [ ] **Step 3: Commit**

```bash
git add internal/rtf/rtf_test.go
git commit -m "test: cover the compressed RTF path

The literal-only compressor in the test produces valid LZFu streams, which
lets the decompressor be exercised on the path Outlook actually uses, plus
truncation handling and the \\fromhtml check.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

### Task 17: Chiusura — README, verifica finale

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Aggiorna il README**

Aggiungi una sezione sulla struttura, dopo la descrizione esistente:

```markdown
## Struttura

L'API pubblica sta nella root; ogni formato è implementato in un package
sotto `internal/`.

| Package | Responsabilità |
|---|---|
| `internal/email` | Tipi piatti condivisi e helper di testo |
| `internal/mimeparse` | Parser RFC5322/MIME |
| `internal/eml` | Lettore EML |
| `internal/pec` | Envelope PEC |
| `internal/msg` | Compound file OLE2 e property Outlook |
| `internal/rtf` | Decompressione e de-encapsulation RTF |
| `internal/tnef` | Container winmail.dat |

## Test

```
go test ./...
```

I fixture in `testdata/` sono derivati da messaggi reali con i dati
sostituiti, tranne quelli `.msg` che sono generati: vedi
`internal/fixtures/README.md`.

Per rigenerare le snapshot dopo un cambio di comportamento voluto:

```
go test . -run TestGoldenFixtures -update
go test ./internal/msg/ -run TestGenerateMSGTestdata -update
```

Un test end-to-end su un `.msg` reale, non versionato:

```
MSG_DEBUG_PATH=/percorso/al/file.msg go test ./internal/msg/ -run E2E -v
```
```

- [ ] **Step 2: Verifica finale**

```bash
go vet ./... && go build ./... && go test ./...
go test ./... -count=1 -cover
```

Controlla la copertura per package. Se un package sta sotto il 40%, dillo nel riepilogo: non è un blocco, ma va saputo.

- [ ] **Step 3: Verifica che nessun dato reale sia nel repo**

```bash
git ls-files | grep -iE '\.(eml|msg)$'
grep -ril -E 'cavigli|CVGRCR81S13D612O|10002113226|TASK0541832|genesys|promocode' -- $(git ls-files) || echo "pulito"
```

Atteso: solo file sotto `testdata/`, e `pulito`.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: document the package layout and how to run the tests

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

- [ ] **Step 5: Riepilogo**

Riporta:
- eventuali test marcati `t.Skip` per bug reali trovati, con il comportamento osservato;
- la copertura per package;
- qualunque punto in cui il piano si è rivelato sbagliato e cosa hai fatto invece.
