package client

import (
	"runtime"
	"testing"
)

func TestGetSystemLoad_MemoryRange(t *testing.T) {
	t.Parallel()

	load := getSystemLoad()

	if load.CPU < 0 {
		t.Errorf("CPU load should be non-negative, got %f", load.CPU)
	}

	if load.Memory < 0 || load.Memory > 100 {
		t.Errorf("Memory should be 0-100, got %f", load.Memory)
	}

	// On Linux, at least one field should be non-zero.
	if runtime.GOOS == "linux" {
		if load.CPU == 0 && load.Memory == 0 {
			t.Log("warning: both CPU and Memory are zero on Linux")
		}
	}
}

func TestGetSystemLoad_NonLinux(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "linux" {
		t.Skip("this test only verifies non-Linux behavior")
	}

	load := getSystemLoad()

	if load.CPU != 0 {
		t.Errorf("expected CPU=0 on %s, got %f", runtime.GOOS, load.CPU)
	}
	if load.Memory != 0 {
		t.Errorf("expected Memory=0 on %s, got %f", runtime.GOOS, load.Memory)
	}
}
