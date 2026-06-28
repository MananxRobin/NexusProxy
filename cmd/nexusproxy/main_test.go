package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunWithoutArgsPrintsHelp(t *testing.T) {
	var out bytes.Buffer

	if err := runWithOutput(nil, &out); err != nil {
		t.Fatal(err)
	}

	text := out.String()
	if !strings.Contains(text, "Usage:") {
		t.Fatalf("expected help output, got:\n%s", text)
	}
	if !strings.Contains(text, "nexusproxy run") {
		t.Fatalf("expected run command in help, got:\n%s", text)
	}
}

func TestRunHelpFlagPrintsFriendlyHelp(t *testing.T) {
	var out bytes.Buffer

	if err := runWithOutput([]string{"--help"}, &out); err != nil {
		t.Fatal(err)
	}

	text := out.String()
	if !strings.Contains(text, "nexusproxy setup") {
		t.Fatalf("expected friendly help output, got:\n%s", text)
	}
	if !strings.Contains(text, "nexusproxy uninstall") {
		t.Fatalf("expected uninstall command in help, got:\n%s", text)
	}
}
