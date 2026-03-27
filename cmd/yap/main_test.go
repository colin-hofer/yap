package main

import (
	"reflect"
	"testing"
)

func TestSplitDebugFlags(t *testing.T) {
	args, debug := splitDebugFlags([]string{"--debug", "join", "TOKEN"})
	if !debug {
		t.Fatal("splitDebugFlags() debug = false, want true")
	}
	if got, want := args, []string{"join", "TOKEN"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("splitDebugFlags() args = %#v, want %#v", got, want)
	}
}

func TestSummarizeArgsRedactsJoinInvite(t *testing.T) {
	if got, want := summarizeArgs([]string{"join", "Y1-secret-token"}), "join <invite>"; got != want {
		t.Fatalf("summarizeArgs() = %q, want %q", got, want)
	}
}
