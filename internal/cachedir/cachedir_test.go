package cachedir

import "testing"

func TestKernelsRejectsInvalidName(t *testing.T) {
	if _, err := Kernels("../evil"); err == nil {
		t.Fatal("expected invalid runtime name error")
	}
}
