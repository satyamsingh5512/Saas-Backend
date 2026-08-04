package routes

import (
	"os/exec"
	"testing"
)

func TestDashboardReliability(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable; skipping dashboard JavaScript reliability tests")
	}

	cmd := exec.Command(node, "--test", "testdata/dashboard_reliability.test.js")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dashboard reliability tests failed: %v\n%s", err, output)
	}
}
