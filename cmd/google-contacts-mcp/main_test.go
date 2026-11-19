package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun_Version(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if got := out.String(); got != "dev\n" {
		t.Errorf("stdout = %q, want %q", got, "dev\n")
	}
}

func TestRun_StubsAndUnknown(t *testing.T) {
	cases := map[string][]string{
		"default serve": nil,
		"flags only":    {"--listen=:1"},
		"serve":         {"serve"},
		"genkey":        {"genkey"},
		"rekey":         {"rekey"},
		"disconnect":    {"disconnect", "a@b.c"},
		"unknown":       {"bogus"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := run(args, &out, &errOut); code != exitFailure {
				t.Errorf("exit = %d, want %d", code, exitFailure)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want empty", out.String())
			}
			if errOut.Len() == 0 || !strings.HasSuffix(errOut.String(), "\n") {
				t.Errorf("stderr = %q, want a message", errOut.String())
			}
		})
	}
}
