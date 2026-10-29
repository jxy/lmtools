package termwidth

import "testing"

func TestRuneWidth(t *testing.T) {
	for r, want := range map[rune]int{
		'a':          1,
		'\u00e9':     1, // e with acute, one rune
		'\u754c':     2, // CJK ideograph
		'\U0001F600': 2, // emoji
		'\u0301':     0, // combining acute accent
		'\u20dd':     0, // combining enclosing circle
	} {
		if got := RuneWidth(r); got != want {
			t.Errorf("RuneWidth(%U) = %d, want %d", r, got, want)
		}
	}
}
