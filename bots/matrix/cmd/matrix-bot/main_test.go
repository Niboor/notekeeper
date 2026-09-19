package main

import "testing"

func TestRun(t *testing.T) {
	if out, code := run([]string{"version"}); code != 0 || out == "" {
		t.Fatalf("version: %q %d", out, code)
	}
	if _, code := run(nil); code != 2 {
		t.Fatalf("no args: code %d", code)
	}
}
