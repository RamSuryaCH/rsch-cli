package spec

import "testing"

func TestManagerCreatesAndValidatesSpec(t *testing.T) {
	manager := NewManager(t.TempDir())
	if _, err := manager.New("oauth"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Validate("oauth"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.New("../unsafe"); err == nil {
		t.Fatal("expected unsafe name rejection")
	}
}
