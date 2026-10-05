package main

import (
	"fmt"
	"time"
)

// TestStatus represents the outcome of an attack test case
type TestStatus string

const (
	StatusPass TestStatus = "PASS" // Attack was safely mitigated/rejected
	StatusFail TestStatus = "FAIL" // Attack bypassed security or caused panic/divergence
	StatusWarn TestStatus = "WARN" // Attack mitigated but with caveats
	StatusSkip TestStatus = "SKIP" // Test skipped
)

// TestCaseResult records the outcome of a specific security attack scenario
type TestCaseResult struct {
	ID          string        `json:"id"`
	Category    string        `json:"category"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Status      TestStatus    `json:"status"`
	Duration    time.Duration `json:"duration"`
	Details     string        `json:"details"`
	Error       string        `json:"error,omitempty"`
}

// SecurityReport holds summary metrics and individual test results
type SecurityReport struct {
	Timestamp   time.Time        `json:"timestamp"`
	TargetRPC   string           `json:"target_rpc"`
	TargetChain uint64           `json:"target_chain"`
	TotalTests  int              `json:"total_tests"`
	Passed      int              `json:"passed"`
	Failed      int              `json:"failed"`
	Warnings    int              `json:"warnings"`
	Skipped     int              `json:"skipped"`
	Duration    time.Duration    `json:"duration"`
	Results     []TestCaseResult `json:"results"`
}

func (r *SecurityReport) AddResult(res TestCaseResult) {
	r.Results = append(r.Results, res)
	r.TotalTests++
	switch res.Status {
	case StatusPass:
		r.Passed++
		fmt.Printf("  ✅ [%s] %s (%v)\n", res.ID, res.Name, res.Duration.Round(time.Millisecond))
	case StatusFail:
		r.Failed++
		fmt.Printf("  ❌ [%s] %s: %s (%v)\n", res.ID, res.Name, res.Details, res.Duration.Round(time.Millisecond))
	case StatusWarn:
		r.Warnings++
		fmt.Printf("  ⚠️  [%s] %s: %s (%v)\n", res.ID, res.Name, res.Details, res.Duration.Round(time.Millisecond))
	case StatusSkip:
		r.Skipped++
		fmt.Printf("  ⏭️  [%s] %s (SKIPPED)\n", res.ID, res.Name)
	}
}
