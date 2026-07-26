package mailfmt

// ParseFile detects the format of the file at path and parses it with the
// matching reader.
//
// For MSG files the MSG reader is used. For everything else the file is
// treated as MIME: a PEC envelope is tried first (the real message lives in
// the postacert.eml part) and, when that fails, the file is parsed as a
// plain EML.
//
// The returned format is the detected one even when parsing fails, except
// when detection itself fails: then it is FormatUnknown.
func ParseFile(path string) (*EmailData, EmailFormat, error) {
	format, err := DetectEmailFormat(path)
	if err != nil {
		return nil, FormatUnknown, err
	}

	if format == FormatMSG {
		email, err := ReadMsgFile(path)
		if err != nil {
			return nil, format, err
		}
		return email, format, nil
	}

	// FormatEML or FormatUnknown: try PEC, fall back to plain EML.
	if email, pecErr := ReadPecInnerEml(path); pecErr == nil {
		return email, format, nil
	}

	email, err := ReadEmlFile(path)
	if err != nil {
		return nil, format, err
	}
	return email, format, nil
}
