package serial

import "testing"

func TestParseLsof(t *testing.T) {
	got := parseLsof("p123\ncminicom\np456\ncscreen\n")
	if got != "minicom (pid 123), screen (pid 456)" {
		t.Fatal(got)
	}
	if parseLsof("") != "" {
		t.Fatal("empty output")
	}
}
