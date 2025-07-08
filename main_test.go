//go:build windows
// +build windows

package main

import (
	"testing"
	"unsafe"
)

func TestConstants(t *testing.T) {
	// Test that our menu constants are properly defined
	if WM_COMMAND != 0x0111 {
		t.Errorf("WM_COMMAND should be 0x0111, got 0x%x", WM_COMMAND)
	}

	if ID_EXIT != 1001 {
		t.Errorf("ID_EXIT should be 1001, got %d", ID_EXIT)
	}

	if TPM_BOTTOMALIGN != 0x0020 {
		t.Errorf("TPM_BOTTOMALIGN should be 0x0020, got 0x%x", TPM_BOTTOMALIGN)
	}

	if MF_STRING != 0x0000 {
		t.Errorf("MF_STRING should be 0x0000, got 0x%x", MF_STRING)
	}
}

func TestPOINTStructure(t *testing.T) {
	// Test that POINT structure has correct size and alignment
	var pt POINT
	if unsafe.Sizeof(pt) != 8 {
		t.Errorf("POINT structure should be 8 bytes, got %d", unsafe.Sizeof(pt))
	}

	// Test field access
	pt.X = 100
	pt.Y = 200
	if pt.X != 100 || pt.Y != 200 {
		t.Errorf("POINT field access failed: X=%d, Y=%d", pt.X, pt.Y)
	}
}

func TestWindowProcMenuHandling(t *testing.T) {
	// Test that menu ID extraction works correctly
	wParam := uintptr(ID_EXIT) // Simulate WM_COMMAND wParam
	menuID := wParam & 0xFFFF

	if menuID != ID_EXIT {
		t.Errorf("Menu ID extraction failed: expected %d, got %d", ID_EXIT, menuID)
	}
}
