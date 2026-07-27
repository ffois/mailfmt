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
