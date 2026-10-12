package email

import (
	"fmt"
	"sort"
	"strings"
)

// msByteUnits are the unit names StrFormatByteSizeW uses on the Italian
// Windows this was measured on; "byte" is used for counts below 1024.
var msByteUnits = []string{"KB", "MB", "GB", "TB", "PB", "EB"}

// MSFormatByteSize formats n as shlwapi's StrFormatByteSizeW does on an
// Italian Windows, which is what Outlook 2010 calls (OUTLMIME.DLL
// FUN_3fe61f70) to put a size in an attached message's name: below 1024
// "N byte", otherwise three significant digits, truncated, with a decimal
// comma ("1,07 KB", "10,4 KB", "311 KB"). A value reaching 1000 of a unit
// moves to the next one ("0,97 MB"), and every step down to the unit shown
// truncates to a whole number first, so 1059062 bytes are 1034 KB and then
// "1,00 MB", not 1,01.
func MSFormatByteSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d byte", n)
	}
	// q counts whole units one step below the unit shown.
	q, unit := n, 0
	for unit < len(msByteUnits)-1 && q>>10 >= 1000 {
		q >>= 10
		unit++
	}
	name := msByteUnits[unit]
	if h := q * 100 / 1024; h < 1000 {
		return fmt.Sprintf("%d,%02d %s", h/100, h%100, name)
	}
	if t := q * 10 / 1024; t < 1000 {
		return fmt.Sprintf("%d,%d %s", t/10, t%10, name)
	}
	return fmt.Sprintf("%d %s", q/1024, name)
}

// MSAttachedMessageFileName is the file name Outlook gives an attached
// message (Attachment.FileName, and PR_ATTACH_LONG_FILENAME when the
// attachment has none stored): its display name without the characters in
// msFileNameDropped, plus ".msg". Nothing else is changed: spaces and dots
// are kept, a name of only dropped characters becomes ".msg", and reserved
// names such as "CON" are not altered.
func MSAttachedMessageFileName(display string) string {
	var b strings.Builder
	for _, r := range display {
		if r <= 0xFFFF && msFileNameDrops(uint16(r)) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String() + ".msg"
}

func msFileNameDrops(c uint16) bool {
	i := sort.Search(len(msFileNameDropped), func(i int) bool { return msFileNameDropped[i][1] >= c })
	return i < len(msFileNameDropped) && msFileNameDropped[i][0] <= c
}
