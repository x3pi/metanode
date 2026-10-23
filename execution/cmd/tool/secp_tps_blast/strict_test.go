package main

import "testing"

func TestBenchVerdictFailsOnLossOrUnverifiedParity(t *testing.T) {
	if got := benchVerdict("r", 25000, 25000, true, true); len(got) != 0 {
		t.Fatalf("a clean run must pass, got %v", got)
	}
	if got := benchVerdict("r", 25000, 0, true, true); len(got) != 1 {
		t.Fatalf("100%% loss must fail, got %v", got)
	}
	if got := benchVerdict("r", 25000, 24999, false, true); len(got) != 1 {
		t.Fatalf("losing one transaction must fail even without parity, got %v", got)
	}
	if got := benchVerdict("r", 25000, 25000, true, false); len(got) != 1 {
		t.Fatalf("unverified parity must fail, got %v", got)
	}
	if got := benchVerdict("r", 25000, 25000, false, false); len(got) != 0 {
		t.Fatalf("parity that was not requested must not fail the run, got %v", got)
	}
	if got := benchVerdict("r", 25000, 0, true, false); len(got) != 2 {
		t.Fatalf("both problems must both be reported, got %v", got)
	}
}
