package main

import (
	"strings"
	"testing"
)

func TestInstructionsDispatch(t *testing.T) {
	handler, ok := commands["instructions"]
	if !ok {
		t.Fatal("instructions command is not registered")
	}
	err := handler("unused-index-directory", []string{"not-a-command"})
	if err == nil || !strings.Contains(err.Error(), "unknown instructions command") {
		t.Fatalf("instructions did not reach its independent dispatcher: %v", err)
	}
	if !worksWithNoHome["instructions"] {
		t.Fatal("independent instruction store is blocked by the history-index home guard")
	}
}
