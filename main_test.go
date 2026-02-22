//go:build windows
// +build windows

package main

import (
	"testing"
	"time"
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

	if ID_SET_LIGHT_TIME != 1002 {
		t.Errorf("ID_SET_LIGHT_TIME should be 1002, got %d", ID_SET_LIGHT_TIME)
	}

	if ID_SET_DARK_TIME != 1003 {
		t.Errorf("ID_SET_DARK_TIME should be 1003, got %d", ID_SET_DARK_TIME)
	}

	if ID_CLEAR_SCHEDULE != 1004 {
		t.Errorf("ID_CLEAR_SCHEDULE should be 1004, got %d", ID_CLEAR_SCHEDULE)
	}

	if TPM_BOTTOMALIGN != 0x0020 {
		t.Errorf("TPM_BOTTOMALIGN should be 0x0020, got 0x%x", TPM_BOTTOMALIGN)
	}

	if MF_STRING != 0x0000 {
		t.Errorf("MF_STRING should be 0x0000, got 0x%x", MF_STRING)
	}

	if MF_SEPARATOR != 0x0800 {
		t.Errorf("MF_SEPARATOR should be 0x0800, got 0x%x", MF_SEPARATOR)
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

func TestParseTime(t *testing.T) {
	cases := []struct {
		input   string
		wantH   int
		wantM   int
		wantErr bool
	}{
		{"06:00", 6, 0, false},
		{"20:30", 20, 30, false},
		{"00:00", 0, 0, false},
		{"23:59", 23, 59, false},
		{"9:5", 9, 5, false},
		{"24:00", 0, 0, true},
		{"06:60", 0, 0, true},
		{"bad", 0, 0, true},
		{":30", 0, 0, true},
		{"", 0, 0, true},
	}
	for _, c := range cases {
		h, m, err := parseTime(c.input)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseTime(%q) expected error, got h=%d m=%d", c.input, h, m)
			}
		} else {
			if err != nil {
				t.Errorf("parseTime(%q) unexpected error: %v", c.input, err)
			} else if h != c.wantH || m != c.wantM {
				t.Errorf("parseTime(%q) = %d:%d, want %d:%d", c.input, h, m, c.wantH, c.wantM)
			}
		}
	}
}

func TestShouldBeLightModeAt(t *testing.T) {
	cases := []struct {
		light, dark string
		hour, min   int
		want        bool
		wantErr     bool
	}{
		// Normal day: light 06:00–20:00
		{"06:00", "20:00", 6, 0, true, false},
		{"06:00", "20:00", 12, 0, true, false},
		{"06:00", "20:00", 19, 59, true, false},
		{"06:00", "20:00", 20, 0, false, false},
		{"06:00", "20:00", 5, 59, false, false},
		{"06:00", "20:00", 0, 0, false, false},
		// Wrap midnight: dark only 02:00–06:00
		{"06:00", "02:00", 6, 0, true, false},
		{"06:00", "02:00", 23, 0, true, false},
		{"06:00", "02:00", 1, 59, true, false},
		{"06:00", "02:00", 2, 0, false, false},
		{"06:00", "02:00", 5, 59, false, false},
		// Same time – error
		{"06:00", "06:00", 6, 0, false, true},
		// Invalid times – error
		{"25:00", "20:00", 0, 0, false, true},
		{"06:00", "99:99", 0, 0, false, true},
	}
	for _, c := range cases {
		now := time.Date(2024, 1, 1, c.hour, c.min, 0, 0, time.UTC)
		got, err := shouldBeLightModeAt(c.light, c.dark, now)
		if c.wantErr {
			if err == nil {
				t.Errorf("shouldBeLightModeAt(%q,%q,%02d:%02d) expected error", c.light, c.dark, c.hour, c.min)
			}
			continue
		}
		if err != nil {
			t.Errorf("shouldBeLightModeAt(%q,%q,%02d:%02d) unexpected error: %v", c.light, c.dark, c.hour, c.min, err)
			continue
		}
		if got != c.want {
			t.Errorf("shouldBeLightModeAt(%q,%q,%02d:%02d) = %v, want %v", c.light, c.dark, c.hour, c.min, got, c.want)
		}
	}
}

func TestClearSchedule(t *testing.T) {
	// Set schedule globals directly, then clear them.
	schedMu.Lock()
	schedLightTime = "06:00"
	schedDarkTime = "20:00"
	schedMu.Unlock()

	clearSchedule()

	schedMu.Lock()
	lt, dt := schedLightTime, schedDarkTime
	schedMu.Unlock()

	if lt != "" || dt != "" {
		t.Errorf("clearSchedule() left light=%q dark=%q, want both empty", lt, dt)
	}
}

