package commentsmoke

import "testing"

func TestCommentSmokeStatus(t *testing.T) {
	const got, want = "pending", "ready"
	if got != want {
		t.Fatalf("status mismatch: got %q, want %q", got, want)
	}
}
