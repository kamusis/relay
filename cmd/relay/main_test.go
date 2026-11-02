package main

import (
	"bytes"
	"testing"
)

func TestTemplateCommands(t *testing.T) {
	// Test template list
	templateCmd := newTemplateCmd()
	var buf bytes.Buffer
	templateCmd.SetOut(&buf)
	templateCmd.SetArgs([]string{"list"})
	if err := templateCmd.Execute(); err != nil {
		t.Fatalf("template list failed: %v", err)
	}

	// Test template show verification
	showBuf := bytes.Buffer{}
	templateCmd.SetOut(&showBuf)
	templateCmd.SetArgs([]string{"show", "verification"})
	if err := templateCmd.Execute(); err != nil {
		t.Fatalf("template show verification failed: %v", err)
	}

	// Test template show re-review
	showBuf2 := bytes.Buffer{}
	templateCmd.SetOut(&showBuf2)
	templateCmd.SetArgs([]string{"show", "re-review"})
	if err := templateCmd.Execute(); err != nil {
		t.Fatalf("template show re-review failed: %v", err)
	}
}

func TestSendFlags(t *testing.T) {
	sendCmd := newSendCmd()
	if sendCmd.Flags().Lookup("template") == nil {
		t.Fatalf("expected --template flag to be registered on send command")
	}
	if sendCmd.Flags().ShorthandLookup("t") == nil {
		t.Fatalf("expected -t shorthand flag to be registered on send command")
	}
	if sendCmd.Flags().Lookup("message") == nil {
		t.Fatalf("expected --message flag to be registered on send command")
	}
	if sendCmd.Flags().ShorthandLookup("m") == nil {
		t.Fatalf("expected -m shorthand flag to be registered on send command")
	}
}
