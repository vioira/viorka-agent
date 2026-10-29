package app

import (
	"bytes"
	"strings"
	"testing"
)

func TestDoctorIsExplicitAboutStage(t *testing.T) {
	var out, errOut bytes.Buffer
	if status := Main([]string{"doctor"}, &out, &errOut); status != 0 {
		t.Fatalf("unexpected status %d: %s", status, errOut.String())
	}
	if !strings.Contains(out.String(), "not-checked") {
		t.Fatal("doctor must not claim live remote connectivity")
	}
}

func TestRunFailsWithoutUsableConfiguration(t *testing.T) {
	var out, errOut bytes.Buffer
	if status := Main([]string{"run"}, &out, &errOut); status == 0 {
		t.Fatalf("run must fail without usable configuration; got %s", out.String())
	}
}
