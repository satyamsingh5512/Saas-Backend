package routes

import (
	"os/exec"
	"testing"
)

func TestRolePermissionEditorDataIntegrity(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable; skipping dashboard JavaScript regression test")
	}

	cmd := exec.Command(node, "--test", "testdata/role_permissions.test.js")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("role permission editor regression test failed: %v\n%s", err, output)
	}
}
