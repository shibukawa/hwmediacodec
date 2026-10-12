package fatal

import "testing"

func TestWrap(t *testing.T) {
	got := wrap("one two three four\n\nfive", 9)
	if want := "one two\nthree\nfour\n\nfive"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := wrap("unbreakablelongword x", 5); got != "unbreakablelongword\nx" {
		t.Errorf("got %q", got)
	}
}
