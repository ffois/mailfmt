# Split in package per formato + suite di test

Data: 2026-07-27
Stato: approvato

## Problema

`mailfmt` è un package Go piatto: 11 file, 2951 righe, tutte nella root. EML,
MSG, PEC, TNEF e RTF compresso vivono nello stesso namespace. I tipi condivisi
(`EmailData`, `EmailAttachment`, `ConvertToUTF8`) sono definiti nel file del
lettore EML e usati da MSG e TNEF, quindi non c'è un confine dichiarato fra i
formati.

I test coprono solo RTF e il dispatch di `ParseFile`: 248 righe in tutto, contro
2700 di codice. MSG, TNEF, PEC e il grosso del parser MIME non hanno test.

## Obiettivi

1. Una directory per formato, con un confine di import esplicito fra i formati.
2. Una suite di test per formato, con fixture versionati.
3. API pubblica invariata: chi importa `github.com/ffois/mailfmt` non cambia una
   riga.

## Non obiettivi

- Nessun cambio di comportamento del parser. Se un test scopre un bug, va
  annotato e trattato a parte, non corretto dentro questo lavoro.
- Nessun refactoring non richiesto dallo split.

## Layout

```
mailfmt/
  doc.go
  parse.go              ParseFile
  format_detector.go    DetectEmailFormat, EmailFormat, Format*
  alias.go              re-export dei tipi e delle funzioni interne
  golden_test.go        golden test table-driven su testdata/
  internal/
    email/   email.go   Data, Attachment, ConvertToUTF8
    mime/    mime.go    Parse, Email, Attachment, EmbeddedFile
    rtf/     rtf.go     Decompress, DeEncapsulateHTML, IsEncapsulatedHTML
    tnef/    tnef.go    Extract, Expand, IsTNEFData
    msg/     msg.go     ReadFile, Read, CFB reader, property parsing
    eml/     eml.go     ReadFile, Build
    pec/     pec.go     ReadInner
  testdata/
    eml/ msg/ pec/ rtf/ tnef/
```

### Grafo delle dipendenze

Aciclico per costruzione:

```
email, mime, rtf   ->  nessuna dipendenza interna
tnef               ->  email
eml                ->  email, mime, tnef
pec                ->  email, mime, eml
msg                ->  email, rtf, tnef
root               ->  tutti
```

`internal/` impedisce che qualcuno importi un sotto-package dall'esterno, quindi
questi confini restano ridiscutibili in futuro senza rompere nessuno.

## Decisioni

### `ConvertToUTF8` va in `internal/email`

Oggi sta in `msg_reader.go` ma la usa anche `eml_reader.go`. È un helper di
testo condiviso fra i formati. `internal/email` è l'unico package che entrambi
importano già, quindi è lì che va senza inventare un package in più.

### `htmlFromCompressedRTF` va in `internal/msg`, non in `internal/rtf`

La funzione prende `map[uint32][]byte` e cerca la chiave
`(pidTagRtfCompressed << 16) | propTypeBinary`: sono property ID MSG. È glue fra
MSG e RTF, non logica RTF. Spostandola in `internal/msg`, `internal/rtf` resta
puro — opera su `[]byte` e non importa nulla.

Il test `TestHTMLFromCompressedRTFNonHTML` si sposta con lei in
`internal/msg/rtfbody_test.go`.

### `pec` riusa `eml.Build`

`ReadEmlFile` (eml_reader.go:60-149) e `ReadPecInnerEml` (eml_reader.go:208-286)
contengono ~100 righe quasi identiche: stessa gestione degli embedded file, stessa
sostituzione `cid:` con data URI, stesso rilevamento PEC, stessa espansione TNEF,
stessa costruzione di `EmailData`.

`internal/eml` esporta:

```go
// Build converte un mime.Email nel formato piatto email.Data.
func Build(m mime.Email) *email.Data
```

`eml.ReadFile` diventa "apri file, `mime.Parse`, `Build`". `pec.ReadInner`
diventa "apri file, `mime.Parse`, trova `postacert.eml`, `mime.Parse` di quello,
`eml.Build`". La duplicazione sparisce.

### Type alias per l'API pubblica

Un package `internal/` non può importare la root (ciclo), quindi i tipi
condivisi devono stare in `internal/email` e `internal/mime`. La root li
ri-espone con type alias, che sono identità di tipo — non tipi nuovi — quindi
`mailfmt.EmailData` e `email.Data` restano intercambiabili per i chiamanti.

`alias.go`:

```go
type EmailData = email.Data
type EmailAttachment = email.Attachment
type Email = mime.Email
type Attachment = mime.Attachment
type EmbeddedFile = mime.EmbeddedFile

func ReadEmlFile(path string) (*EmailData, error)      { return eml.ReadFile(path) }
func ReadPecInnerEml(path string) (*EmailData, error)  { return pec.ReadInner(path) }
func ReadMsgFile(path string) (*EmailData, error)      { return msg.ReadFile(path) }
func Read(r io.ReaderAt) (*EmailData, error)           { return msg.Read(r) }
func Parse(r io.Reader) (Email, error)                 { return mime.Parse(r) }
func ConvertToUTF8(s string) string                    { return email.ConvertToUTF8(s) }
```

`ParseFile`, `DetectEmailFormat`, `EmailFormat` e le costanti `Format*` restano
definite nella root: sono logica di dispatch, non appartengono a un formato.

Superficie pubblica dopo lo split, identica a prima:

| Identificatore | Dove |
|---|---|
| `ParseFile` | `parse.go` |
| `DetectEmailFormat`, `EmailFormat`, `FormatEML`, `FormatMSG`, `FormatUnknown` | `format_detector.go` |
| `EmailData`, `EmailAttachment`, `Email`, `Attachment`, `EmbeddedFile` | `alias.go` (alias) |
| `ReadEmlFile`, `ReadPecInnerEml`, `ReadMsgFile`, `Read`, `Parse`, `ConvertToUTF8` | `alias.go` (wrapper) |

## Test

### Due livelli

**Unit test sintetici**, uno per package interno. Input costruiti in memoria,
niente I/O su file, esecuzione veloce. Stile già presente in `rtf_test.go`:
un commento sopra ogni test che spiega l'invariante verificata, non il
meccanismo.

**Golden test** nella root (`golden_test.go`): table-driven su
`testdata/<formato>/*`, ogni fixture accompagnato da un `.golden.json` con
l'`EmailData` atteso serializzato. Flag `-update` per rigenerare i golden quando
il comportamento cambia di proposito.

Il golden test verifica `ParseFile` end-to-end, quindi copre anche il dispatch e
l'integrazione fra package.

### Fixture

I file di partenza stanno in `C:\Users\LyzCoote\Desktop\Test mail` e contengono
dati personali reali (nomi, codici fiscali, numeri di polizza, referti medici).
**Non vengono committati.** Da ciascuno derivo un fixture con:

- nomi, indirizzi email, codici fiscali e numeri identificativi sostituiti con
  valori finti;
- allegati binari troncati a pochi byte, mantenendo header MIME e struttura
  (gli `.eml` da 8-11 MB scendono sotto i 50 KB).

Quello che conta per il parser è la struttura MIME, non il contenuto degli
allegati.

### Fixture MSG

Un `.msg` è un compound file OLE2: i dati stanno sparsi in property stream
binari e non si anonimizzano in modo affidabile con una sostituzione testuale.

Quindi i fixture MSG sono generati: `internal/msg/cfbwriter_test.go` contiene un
writer CFB minimale che costruisce uno stream `.msg` valido da una
`map[uint32][]byte` di property più una lista di allegati. I file in
`testdata/msg/` sono prodotti da quel writer — sintetici al 100%, nessun dato
reale, e permettono di costruire esattamente i casi limite voluti (mini-stream
contro settore regolare, property unicode contro string8, RTF compresso).

I `.msg` reali restano fuori dal repo e alimentano un test end-to-end gated da
variabile d'ambiente, come l'attuale `TestReadMSGInlineImagesE2E` con
`MSG_DEBUG_PATH`.

### Copertura pianificata

| Package | Casi | N |
|---|---|---|
| `internal/eml` | plain text; multipart/alternative; multipart/mixed con allegato; multipart/related con `cid:` inline sostituito da data URI; header MIME-encoded `=?utf-8?B?`; body charset ISO-8859-1 | 6 |
| `internal/msg` | property unicode; property string8/cp1252; allegato; body RTF compresso con immagini inline; stream sotto la soglia mini-stream (4096 byte) | 5 |
| `internal/pec` | envelope con `postacert.eml`; envelope con `daticert.xml` + `smime.p7s` che deve dare `IsPec=true`; envelope senza `postacert.eml` che deve fallire; PEC annidata | 4 |
| `internal/tnef` | `winmail.dat` dentro un EML; TNEF annidato; allegato non-TNEF ignorato | 3 |
| `internal/rtf` | payload LZFu compresso; payload MELA non compresso; RTF non-HTML che deve dare stringa vuota; de-encapsulation `\fromhtml`; `\*\mhtmltag` scartato | 5 |
| root | dispatch EML; dispatch MSG; file mancante | 3 |

Totale ~26 test, più il golden test table-driven che li attraversa tutti
end-to-end.

I test esistenti vengono spostati, non riscritti:

- `rtf_test.go` va in `internal/rtf`, meno due casi che sono MSG e non RTF:
  `TestHTMLFromCompressedRTFNonHTML` e `TestReadMSGInlineImagesE2E` vanno in
  `internal/msg`;
- `mailparser_test.go` va in `internal/mime`;
- `parse_test.go` resta nella root.

## Verifica

Lo split è corretto quando:

- `go build ./...` e `go vet ./...` passano;
- `go test ./...` passa;
- i test esistenti passano dopo lo spostamento, senza modifiche alle asserzioni;
- un programma esterno che importa `mailfmt` e usa la tabella della superficie
  pubblica qui sopra compila senza cambiamenti.

L'ultimo punto si verifica con un file `example_test.go` nella root che tocca
ogni identificatore pubblico.
