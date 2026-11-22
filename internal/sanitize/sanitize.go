package sanitize

import (
	"regexp"
	"strings"
)

var ansi = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|\x1b[@-Z\\\\-_]")

// Line preserves the newline (if present) and the last visible progress update.
func Line(s string) string {
	s = ansi.ReplaceAllString(s, "")
	if !strings.Contains(s, "\r") {
		return s
	}
	body := strings.TrimRight(strings.TrimSuffix(s, "\n"), "\r")
	if pos := strings.LastIndexByte(body, '\r'); pos >= 0 {
		body = body[pos+1:]
	}
	if strings.HasSuffix(s, "\n") {
		body += "\n"
	}
	return body
}
