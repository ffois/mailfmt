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
package mailfmt
