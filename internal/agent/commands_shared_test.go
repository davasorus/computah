package agent

import (
	"fmt"
	"strings"
	"testing"
)

func TestRunInfoCommandHandlesReadOnly(t *testing.T) {
	st := &SessionStore{}
	msgs := []Message{{Role: "user", Content: "hi"}}

	// /help, /context, /tools, /stats, /budget are pure read-only.
	for _, cmd := range []string{"/help", "/context", "/tools", "/stats", "/budget"} {
		out, handled := runInfoCommand(cmd, "http://x", "m", msgs, st)
		if !handled {
			t.Fatalf("%s should be handled by the info dispatcher", cmd)
		}
		if strings.TrimSpace(out) == "" {
			t.Fatalf("%s produced no output", cmd)
		}
	}
}

func TestRunInfoCommandRejectsStateful(t *testing.T) {
	st := &SessionStore{}
	for _, cmd := range []string{"/plan do x", "/commit", "/reload", "/model qwen", "/fork", "/compact"} {
		_, handled := runInfoCommand(cmd, "http://x", "m", nil, st)
		if handled {
			t.Fatalf("%s is stateful and must NOT be handled by the info dispatcher", cmd)
		}
	}
}

func TestEffortArgIsStateful(t *testing.T) {
	st := &SessionStore{}
	// bare /effort → info (reports)
	if _, handled := runInfoCommand("/effort", "u", "m", nil, st); !handled {
		t.Fatal("bare /effort should be an info command")
	}
	// /effort low → stateful (sets); dispatcher must decline
	if _, handled := runInfoCommand("/effort low", "u", "m", nil, st); handled {
		t.Fatal("/effort low mutates state — dispatcher must decline it")
	}
}

func TestIsInfoCommand(t *testing.T) {
	if !isInfoCommand("/stats") {
		t.Fatal("/stats is info")
	}
	if isInfoCommand("/commit") {
		t.Fatal("/commit is not info")
	}
	if isInfoCommand("/tools") != true {
		t.Fatal("/tools is info")
	}
}

func TestCaptureRedirectsStdout(t *testing.T) {
	got := capture(func() { fmt.Print("hello capture") })
	if !strings.Contains(got, "hello capture") {
		t.Fatalf("capture missed output: %q", got)
	}
}

func TestRenderEffort(t *testing.T) {
	saved, savedP := reasoningEffort, planReasoningEffort
	defer func() { reasoningEffort, planReasoningEffort = saved, savedP }()
	reasoningEffort, planReasoningEffort = "low", "high"
	out := renderEffort()
	if !strings.Contains(out, "low") || !strings.Contains(out, "high") {
		t.Fatalf("effort render wrong: %q", out)
	}
}
