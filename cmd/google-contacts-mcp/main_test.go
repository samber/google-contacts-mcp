package main

import (
	"bytes"
	"strings"
	"testing"
)

func noEnv(string) string { return "" }

func TestRun_Version(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, noEnv, &out, &errOut); code != 0 {
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
			if code := run(args, noEnv, &out, &errOut); code != exitFailure {
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

func TestRun_LoadsConfigBeforeStub(t *testing.T) {
	env := func(key string) string {
		return map[string]string{"GCMCP_ENCRYPTION_KEYS": "k1:AAAA"}[key]
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"rekey"}, env, &out, &errOut); code != exitFailure {
		t.Fatalf("exit = %d", code)
	}
	if got := errOut.String(); got != "rekey: not implemented\n" {
		t.Errorf("stderr = %q, want not implemented after a valid config", got)
	}

	errOut.Reset()
	run([]string{"serve", "stray"}, env, &out, &errOut)
	if !strings.Contains(errOut.String(), "stray") {
		t.Errorf("stderr = %q, want config error naming stray", errOut.String())
	}
}

func TestRun_Help(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"serve", "-h"}, noEnv, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "-base-url") {
		t.Errorf("stdout = %q, want usage", out.String())
	}
}
