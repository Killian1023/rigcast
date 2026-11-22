package sanitize

import "testing"

func TestLine(t *testing.T) {
	for input, want := range map[string]string{
		"hello\r\n": "hello\n",
		"\x1b[?25l1%\r20%\r\x1b[32m100%\x1b[0m\r\r\n": "100%\n",
		"partial\rupdated":                            "updated",
		"\x1b]0;title\x07text\n":                      "text\n",
	} {
		if got := Line(input); got != want {
			t.Errorf("Line(%q) = %q, want %q", input, got, want)
		}
	}
}
