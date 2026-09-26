//go:build windows
// +build windows

package main

import (
	_ "embed"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	REGKEY_THEME_PERSONALIZE = `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize` // in HKCU
	REGKEY_AUTORUN           = `SOFTWARE\Microsoft\Windows\CurrentVersion\Run`
	REGNAME_TASKBAR_TRAY     = `SystemUsesLightTheme`
	REGNAME_APP_LIGHT_THEME  = `AppsUseLightTheme`
	APPNAME                  = `Windows Theme Switcher`

	// Windows message constants
	WM_SETTINGCHANGE = 0x001A
	HWND_BROADCAST   = uintptr(0xFFFF)

	// System tray constants
	NIM_ADD      = 0x00000000
	NIM_MODIFY   = 0x00000001
	NIM_DELETE   = 0x00000002
	NIF_MESSAGE  = 0x00000001
	NIF_ICON     = 0x00000002
	NIF_TIP      = 0x00000004
	WM_USER      = 0x0400
	WM_TRAYICON  = WM_USER + 1
	WM_LBUTTONUP = 0x0202
	WM_RBUTTONUP = 0x0205
	WM_COMMAND   = 0x0111

	// Menu constants
	TPM_BOTTOMALIGN = 0x0020
	TPM_LEFTALIGN   = 0x0000
	TPM_RIGHTBUTTON = 0x0002
	MF_STRING       = 0x0000

	// Menu item IDs
	ID_EXIT           = 1001
	ID_SET_LIGHT_TIME = 1002
	ID_SET_DARK_TIME  = 1003
	ID_CLEAR_SCHEDULE = 1004

	// Input dialog control IDs (1=IDOK, 2=IDCANCEL for IsDialogMessage compatibility)
	ID_DIALOG_OK     = 1
	ID_DIALOG_CANCEL = 2

	// Additional menu flags
	MF_SEPARATOR = 0x0800

	// Additional Windows messages
	WM_CLOSE = 0x0010

	// ShowWindow command
	SW_SHOW = 5

	// Window extended styles
	WS_EX_CLIENTEDGE    = 0x00000200
	WS_EX_TOPMOST       = 0x00000008
	WS_EX_DLGMODALFRAME = 0x00000001

	// System color brush for dialog background (COLOR_BTNFACE + 1)
	COLOR_BTNFACE_BRUSH = 16

	// Schedule registry key (HKCU)
	REGKEY_SCHEDULE = `Software\WindowsThemeSwitcher`
)

//go:embed assets/dark_mode.ico
var dark_mode []byte

//go:embed assets/light_mode.ico
var light_mode []byte

// Windows API functions
var (
	user32                        = windows.NewLazySystemDLL("user32.dll")
	shell32                       = windows.NewLazySystemDLL("shell32.dll")
	kernel32                      = windows.NewLazySystemDLL("kernel32.dll")
	sendMessageW                  = user32.NewProc("SendMessageW")
	UpdatePerUserSystemParameters = user32.NewProc("UpdatePerUserSystemParameters")
	shellNotifyIcon               = shell32.NewProc("Shell_NotifyIconW")
	createWindowEx                = user32.NewProc("CreateWindowExW")
	defWindowProc                 = user32.NewProc("DefWindowProcW")
	registerClass                 = user32.NewProc("RegisterClassW")
	getMessage                    = user32.NewProc("GetMessageW")
	translateMessage              = user32.NewProc("TranslateMessage")
	dispatchMessage               = user32.NewProc("DispatchMessageW")
	postQuitMessage               = user32.NewProc("PostQuitMessage")
	loadIcon                      = user32.NewProc("LoadIconW")
	loadImage                     = user32.NewProc("LoadImageW")
	createIconFromResourceEx      = user32.NewProc("CreateIconFromResourceEx")
	getModuleHandle               = kernel32.NewProc("GetModuleHandleW")
	createPopupMenu               = user32.NewProc("CreatePopupMenu")
	appendMenuW                   = user32.NewProc("AppendMenuW")
	trackPopupMenu                = user32.NewProc("TrackPopupMenu")
	getCursorPos                  = user32.NewProc("GetCursorPos")
	destroyMenu                   = user32.NewProc("DestroyMenu")
	setForegroundWindow           = user32.NewProc("SetForegroundWindow")
	showWindow                    = user32.NewProc("ShowWindow")
	getWindowTextW                = user32.NewProc("GetWindowTextW")
	destroyWindow                 = user32.NewProc("DestroyWindow")
	setFocus                      = user32.NewProc("SetFocus")
	enableWindow                  = user32.NewProc("EnableWindow")
	isDialogMessage               = user32.NewProc("IsDialogMessage")
	getSystemMetrics              = user32.NewProc("GetSystemMetrics")
	registerWindowMessage         = user32.NewProc("RegisterWindowMessageW")
)

// wmTaskbarCreated is broadcast by Explorer when the taskbar is (re)created,
// e.g. after explorer.exe restarts. The tray icon must be re-added then.
var wmTaskbarCreated uint32

func init() {
	// Win32 windows have thread affinity: messages for a window are only
	// delivered to the OS thread that created it. The Go scheduler may move
	// the main goroutine to another OS thread at any time, after which the
	// message loop no longer receives the tray icon's messages and the icon
	// stops responding. Pin the main goroutine to the main OS thread.
	runtime.LockOSThread()
}

// NOTIFYICONDATA structure for Shell_NotifyIcon
type NOTIFYICONDATA struct {
	CbSize           uint32
	Hwnd             syscall.Handle
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            syscall.Handle
	SzTip            [128]uint16
}

// WNDCLASS structure
type WNDCLASS struct {
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     syscall.Handle
	HIcon         syscall.Handle
	HCursor       syscall.Handle
	HbrBackground syscall.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
}

// MSG structure
type MSG struct {
	Hwnd    syscall.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

// POINT structure
type POINT struct {
	X int32
	Y int32
}

var (
	hwnd      syscall.Handle
	lightIcon syscall.Handle
	darkIcon  syscall.Handle

	// Schedule state – guarded by schedMu.
	schedMu        sync.Mutex
	schedLightTime string
	schedDarkTime  string

	// Input dialog state – accessed only on the main GUI thread.
	inputEditHwnd syscall.Handle
	inputResult   string
	inputOK       bool
	inputDone     bool

	// inputDialogWndProc is the Windows callback for the input dialog window.
	// It must be created once at package level (syscall.NewCallback limit).
	inputDialogWndProc = syscall.NewCallback(inputDialogProc)

	// inputDialogActive prevents re-entrant calls to showInputDialog.
	inputDialogActive bool
)

func main() {
	lightTimeFlag := flag.String("light-time", "", "time to switch to light mode (HH:MM, e.g. 06:00)")
	darkTimeFlag := flag.String("dark-time", "", "time to switch to dark mode (HH:MM, e.g. 20:00)")
	flag.Parse()

	fmt.Println("Dark Mode on:", isDark())

	if !isSetAutoRun() {
		SetAutoRun(true)
	}

	if *lightTimeFlag != "" || *darkTimeFlag != "" {
		if *lightTimeFlag == "" || *darkTimeFlag == "" {
			log.Fatal("Both -light-time and -dark-time must be specified together")
		}
		if err := setSchedule(*lightTimeFlag, *darkTimeFlag); err != nil {
			log.Fatalf("Invalid schedule: %v", err)
		}
		saveScheduleToRegistry(*lightTimeFlag, *darkTimeFlag)
	} else {
		// No flags – restore persisted schedule from registry.
		if lt, dt := loadScheduleFromRegistry(); lt != "" && dt != "" {
			if err := setSchedule(lt, dt); err != nil {
				log.Printf("Ignoring invalid persisted schedule: %v", err)
			}
		}
	}

	go runScheduler()
	go monitor(react)

	// Initialize Windows GUI
	initializeSystemTray()
}

func initializeSystemTray() {
	// Get module handle
	hInstance, _, _ := getModuleHandle.Call(0)

	// Register window class
	className, _ := syscall.UTF16PtrFromString("ThemeSwitcherClass")
	wc := WNDCLASS{
		LpfnWndProc:   syscall.NewCallback(windowProc),
		HInstance:     syscall.Handle(hInstance),
		LpszClassName: className,
	}

	registerClass.Call(uintptr(unsafe.Pointer(&wc)))

	// Create hidden window
	windowName, _ := syscall.UTF16PtrFromString("Theme Switcher")
	ret, _, _ := createWindowEx.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowName)),
		0,
		0, 0, 0, 0,
		0, 0,
		hInstance,
		0,
	)
	hwnd = syscall.Handle(ret)

	taskbarCreated, _ := syscall.UTF16PtrFromString("TaskbarCreated")
	msgID, _, _ := registerWindowMessage.Call(uintptr(unsafe.Pointer(taskbarCreated)))
	wmTaskbarCreated = uint32(msgID)

	// Load icons from embedded data
	lightIcon = createIconFromData(light_mode)
	darkIcon = createIconFromData(dark_mode)

	// Create system tray icon
	createTrayIcon()

	// Message loop
	messageLoop()
}

func createIconFromData(data []byte) syscall.Handle {
	// ICO files start with an ICONDIR structure, but for CreateIconFromResourceEx
	// we need to skip the ICO header and pass just the icon image data
	// ICO format: ICONDIR (6 bytes) + ICONDIRENTRY array + actual icon data

	if len(data) < 6 {
		// Fallback to system icon if data is invalid
		icon, _, _ := loadIcon.Call(0, 32512) // IDI_APPLICATION
		return syscall.Handle(icon)
	}

	// Parse ICO header to find the first icon entry
	// ICONDIR: Reserved(2) + Type(2) + Count(2)
	iconCount := uint16(data[4]) | (uint16(data[5]) << 8)
	if iconCount == 0 || len(data) < 6+int(iconCount)*16 {
		// Fallback to system icon if structure is invalid
		icon, _, _ := loadIcon.Call(0, 32512) // IDI_APPLICATION
		return syscall.Handle(icon)
	}

	// Get first ICONDIRENTRY (16 bytes starting at offset 6)
	entryOffset := 6
	imageOffset := uint32(data[entryOffset+12]) | (uint32(data[entryOffset+13]) << 8) |
		(uint32(data[entryOffset+14]) << 16) | (uint32(data[entryOffset+15]) << 24)
	imageSize := uint32(data[entryOffset+8]) | (uint32(data[entryOffset+9]) << 8) |
		(uint32(data[entryOffset+10]) << 16) | (uint32(data[entryOffset+11]) << 24)

	// Validate image offset and size
	if imageOffset >= uint32(len(data)) || imageOffset+imageSize > uint32(len(data)) {
		// Fallback to system icon if offset/size is invalid
		icon, _, _ := loadIcon.Call(0, 32512) // IDI_APPLICATION
		return syscall.Handle(icon)
	}

	// Extract the actual icon image data (skip ICO header)
	imageData := data[imageOffset : imageOffset+imageSize]

	// Create icon from the image data
	icon, _, _ := createIconFromResourceEx.Call(
		uintptr(unsafe.Pointer(&imageData[0])), // pointer to icon data
		uintptr(imageSize),                     // size of icon data
		1,                                      // fIcon = TRUE (this is an icon, not cursor)
		0x00030000,                             // dwVersion = 0x00030000
		0,                                      // cxDesired = 0 (use default)
		0,                                      // cyDesired = 0 (use default)
		0,                                      // Flags = 0 (default behavior)
	)

	if icon != 0 {
		return syscall.Handle(icon)
	}

	// Final fallback to system icon if CreateIconFromResourceEx failed
	fallbackIcon, _, _ := loadIcon.Call(0, 32512) // IDI_APPLICATION
	return syscall.Handle(fallbackIcon)
}

func createTrayIcon() {
	nid := NOTIFYICONDATA{
		CbSize:           uint32(unsafe.Sizeof(NOTIFYICONDATA{})),
		Hwnd:             hwnd,
		UID:              1,
		UFlags:           NIF_ICON | NIF_MESSAGE | NIF_TIP,
		UCallbackMessage: WM_TRAYICON,
		HIcon:            getCurrentIcon(),
	}

	// Set tooltip
	tip := getCurrentTooltip()
	copy(nid.SzTip[:], syscall.StringToUTF16(tip))

	shellNotifyIcon.Call(NIM_ADD, uintptr(unsafe.Pointer(&nid)))
}

func updateTrayIcon() {
	nid := NOTIFYICONDATA{
		CbSize: uint32(unsafe.Sizeof(NOTIFYICONDATA{})),
		Hwnd:   hwnd,
		UID:    1,
		UFlags: NIF_ICON | NIF_TIP,
		HIcon:  getCurrentIcon(),
	}

	// Set tooltip
	tip := getCurrentTooltip()
	copy(nid.SzTip[:], syscall.StringToUTF16(tip))

	shellNotifyIcon.Call(NIM_MODIFY, uintptr(unsafe.Pointer(&nid)))
}

func getCurrentIcon() syscall.Handle {
	if isDark() {
		return lightIcon // Show light icon when in dark mode (what clicking will do)
	} else {
		return darkIcon // Show dark icon when in light mode (what clicking will do)
	}
}

func getCurrentTooltip() string {
	if isDark() {
		return "Theme Switcher - Click to switch to Light mode"
	} else {
		return "Theme Switcher - Click to switch to Dark mode"
	}
}

func showContextMenu() {
	// Create popup menu
	hMenu, _, _ := createPopupMenu.Call()
	if hMenu == 0 {
		return
	}
	defer destroyMenu.Call(hMenu)

	// Build schedule-aware labels.
	schedMu.Lock()
	lt, dt := schedLightTime, schedDarkTime
	schedMu.Unlock()

	lightLabel := "Set Light Time..."
	if lt != "" {
		lightLabel = "Set Light Time... (" + lt + ")"
	}
	darkLabel := "Set Dark Time..."
	if dt != "" {
		darkLabel = "Set Dark Time... (" + dt + ")"
	}

	lightText, _ := syscall.UTF16PtrFromString(lightLabel)
	appendMenuW.Call(hMenu, MF_STRING, ID_SET_LIGHT_TIME, uintptr(unsafe.Pointer(lightText)))

	darkText, _ := syscall.UTF16PtrFromString(darkLabel)
	appendMenuW.Call(hMenu, MF_STRING, ID_SET_DARK_TIME, uintptr(unsafe.Pointer(darkText)))

	if lt != "" || dt != "" {
		clearText, _ := syscall.UTF16PtrFromString("Clear Schedule")
		appendMenuW.Call(hMenu, MF_STRING, ID_CLEAR_SCHEDULE, uintptr(unsafe.Pointer(clearText)))
	}

	// Separator before Exit
	appendMenuW.Call(hMenu, MF_SEPARATOR, 0, 0)

	// Add "Exit" menu item
	exitText, _ := syscall.UTF16PtrFromString("Exit")
	appendMenuW.Call(hMenu, MF_STRING, ID_EXIT, uintptr(unsafe.Pointer(exitText)))

	// Get cursor position
	var pt POINT
	getCursorPos.Call(uintptr(unsafe.Pointer(&pt)))

	// Set foreground window to ensure menu appears properly
	setForegroundWindow.Call(uintptr(hwnd))

	// Show context menu
	trackPopupMenu.Call(
		hMenu,
		TPM_BOTTOMALIGN|TPM_LEFTALIGN|TPM_RIGHTBUTTON,
		uintptr(pt.X),
		uintptr(pt.Y),
		0,
		uintptr(hwnd),
		0,
	)
}

func windowProc(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	if wmTaskbarCreated != 0 && msg == wmTaskbarCreated {
		createTrayIcon()
		return 0
	}
	switch msg {
	case WM_TRAYICON:
		if lParam == WM_LBUTTONUP {
			// Direct icon click - toggle theme!
			fmt.Println("Tray icon clicked - toggling theme")
			toggleTheme()
			updateTrayIcon()
		} else if lParam == WM_RBUTTONUP {
			// Right click - show context menu with schedule and Exit options
			fmt.Println("Tray icon right-clicked - showing context menu")
			showContextMenu()
		}
		return 0
	case WM_COMMAND:
		// Handle menu item selection
		menuID := wParam & 0xFFFF
		switch menuID {
		case ID_EXIT:
			fmt.Println("Exit selected from context menu")
			onExit()
		case ID_SET_LIGHT_TIME:
			schedMu.Lock()
			currentLight := schedLightTime
			schedMu.Unlock()
			result, ok := showInputDialog(
				"Set Light Mode Time",
				"Enter time to switch to light mode (HH:MM):",
				currentLight,
			)
			if !ok {
				break
			}
			if _, _, err := parseTime(result); err != nil {
				fmt.Printf("Invalid light time %q: %v\n", result, err)
				break
			}
			// Update light time and re-read dark time atomically so we use
			// the most current value even if the dialog took a while.
			schedMu.Lock()
			schedLightTime = result
			newDark := schedDarkTime
			schedMu.Unlock()
			if newDark != "" {
				if err := setSchedule(result, newDark); err == nil {
					saveScheduleToRegistry(result, newDark)
					updateTrayIcon()
				} else {
					fmt.Printf("Schedule error: %v\n", err)
				}
			} else {
				fmt.Printf("Light time set to %s. Set dark time to activate schedule.\n", result)
			}
		case ID_SET_DARK_TIME:
			schedMu.Lock()
			currentDark := schedDarkTime
			schedMu.Unlock()
			result, ok := showInputDialog(
				"Set Dark Mode Time",
				"Enter time to switch to dark mode (HH:MM):",
				currentDark,
			)
			if !ok {
				break
			}
			if _, _, err := parseTime(result); err != nil {
				fmt.Printf("Invalid dark time %q: %v\n", result, err)
				break
			}
			// Update dark time and re-read light time atomically.
			schedMu.Lock()
			schedDarkTime = result
			newLight := schedLightTime
			schedMu.Unlock()
			if newLight != "" {
				if err := setSchedule(newLight, result); err == nil {
					saveScheduleToRegistry(newLight, result)
					updateTrayIcon()
				} else {
					fmt.Printf("Schedule error: %v\n", err)
				}
			} else {
				fmt.Printf("Dark time set to %s. Set light time to activate schedule.\n", result)
			}
		case ID_CLEAR_SCHEDULE:
			clearSchedule()
			clearScheduleFromRegistry()
		}
		return 0
	default:
		ret, _, _ := defWindowProc.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
		return ret
	}
}

func messageLoop() {
	var msg MSG
	for {
		ret, _, _ := getMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if ret == 0 { // WM_QUIT
			break
		} else if ret == 0xFFFFFFFF { // Error
			break
		}

		translateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		dispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func onReady() {
	// This function is no longer needed as we handle initialization in initializeSystemTray
}

func onExit() {
	// Clean up system tray icon
	nid := NOTIFYICONDATA{
		CbSize: uint32(unsafe.Sizeof(NOTIFYICONDATA{})),
		Hwnd:   hwnd,
		UID:    1,
	}
	shellNotifyIcon.Call(NIM_DELETE, uintptr(unsafe.Pointer(&nid)))

	postQuitMessage.Call(0)
}

func getIcon(s string) []byte {
	b, err := os.ReadFile(s)
	if err != nil {
		fmt.Print(err)
	}
	return b
}

// react to the change
func react(isDark bool) {
	updateTrayIcon()
}

func isDark() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, REGKEY_THEME_PERSONALIZE, registry.QUERY_VALUE)
	if err != nil {
		log.Fatal(err)
	}
	defer k.Close()
	val, _, err := k.GetIntegerValue(REGNAME_TASKBAR_TRAY)
	if err != nil {
		log.Fatal(err)
	}
	return val == 0
}

func setDarkModeTheme() {
	setTheme(0)
}

func setLightModeTheme() {
	setTheme(1)
}

// toggleTheme switches between light and dark themes based on current state
func toggleTheme() {
	if isDark() {
		fmt.Println("Switching to light mode")
		setLightModeTheme()
	} else {
		fmt.Println("Switching to dark mode")
		setDarkModeTheme()
	}
}

func setTheme(themeMode uint32) {
	k, err := registry.OpenKey(registry.CURRENT_USER, REGKEY_THEME_PERSONALIZE, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		log.Fatal(err)
	}

	// Set both registry values
	if err := k.SetDWordValue(REGNAME_TASKBAR_TRAY, themeMode); err != nil {
		log.Fatal(err)
	}

	if err := k.SetDWordValue(REGNAME_APP_LIGHT_THEME, themeMode); err != nil {
		log.Fatal(err)
	}

	if err := k.Close(); err != nil {
		log.Fatal(err)
	}

	// Broadcast the theme change to all windows
	notifyThemeChange()
}

// Function to notify the system about theme change
func notifyThemeChange() {
	// Convert wide string to UTF16 pointer
	winStr, _ := syscall.UTF16PtrFromString("ImmersiveColorSet")

	// Broadcast theme change message
	sendMessageW.Call(
		HWND_BROADCAST,
		WM_SETTINGCHANGE,
		0,
		uintptr(unsafe.Pointer(winStr)),
	)

	// Update system parameters
	UpdatePerUserSystemParameters.Call(1, 0)
}

func monitor(fn func(bool)) {
	var regNotifyChangeKeyValue *syscall.Proc
	changed := make(chan bool)

	if advapi32, err := syscall.LoadDLL("Advapi32.dll"); err == nil {
		if p, err := advapi32.FindProc("RegNotifyChangeKeyValue"); err == nil {
			regNotifyChangeKeyValue = p
		} else {
			log.Fatal("Could not find function RegNotifyChangeKeyValue in Advapi32.dll")
		}
	}
	if regNotifyChangeKeyValue != nil {
		go func() {
			k, err := registry.OpenKey(registry.CURRENT_USER, REGKEY_THEME_PERSONALIZE, syscall.KEY_NOTIFY|registry.QUERY_VALUE)
			if err != nil {
				log.Fatal(err)
			}
			var wasDark uint64
			for {
				regNotifyChangeKeyValue.Call(uintptr(k), 0, 0x00000001|0x00000004, 0, 0)
				val, _, err := k.GetIntegerValue(REGNAME_TASKBAR_TRAY)
				if err != nil {
					log.Fatal(err)
				}
				if val != wasDark {
					wasDark = val
					changed <- val == 0
				}
			}
		}()
	}
	for {
		val := <-changed
		fn(val)
	}

}

// auto dark mode light mode switch

// parseTime parses a time string in HH:MM format and returns the hour and minute.
func parseTime(s string) (int, int, error) {
	var h, m int
	n, err := fmt.Sscanf(s, "%d:%d", &h, &m)
	if err != nil || n != 2 || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("invalid time %q, expected HH:MM (e.g. 06:00)", s)
	}
	return h, m, nil
}

// shouldBeLightModeAt returns true if, given the light and dark switch times,
// the provided time falls within the light mode window.
func shouldBeLightModeAt(lightTime, darkTime string, now time.Time) (bool, error) {
	lh, lm, err := parseTime(lightTime)
	if err != nil {
		return false, err
	}
	dh, dm, err := parseTime(darkTime)
	if err != nil {
		return false, err
	}
	if lh*60+lm == dh*60+dm {
		return false, fmt.Errorf("light-time and dark-time must be different")
	}
	current := now.Hour()*60 + now.Minute()
	light := lh*60 + lm
	dark := dh*60 + dm
	if light < dark {
		// e.g. light=06:00, dark=20:00 → light mode between 06:00 and 20:00
		return current >= light && current < dark, nil
	}
	// e.g. light=06:00, dark=02:00 → dark only between 02:00 and 06:00
	return current >= light || current < dark, nil
}

// shouldBeLightMode returns true if the current time falls within the light mode window.
func shouldBeLightMode(lightTime, darkTime string) (bool, error) {
	return shouldBeLightModeAt(lightTime, darkTime, time.Now())
}

// setSchedule validates the given times, updates the global schedule state,
// and immediately applies the correct theme. Returns an error on invalid times.
func setSchedule(lightTime, darkTime string) error {
	isLight, err := shouldBeLightMode(lightTime, darkTime)
	if err != nil {
		return err
	}
	schedMu.Lock()
	schedLightTime = lightTime
	schedDarkTime = darkTime
	schedMu.Unlock()
	fmt.Printf("Scheduler active: light=%s dark=%s (now light=%v)\n", lightTime, darkTime, isLight)
	if isLight {
		setLightModeTheme()
	} else {
		setDarkModeTheme()
	}
	return nil
}

// clearSchedule disables the active schedule without changing the current theme.
func clearSchedule() {
	schedMu.Lock()
	schedLightTime = ""
	schedDarkTime = ""
	schedMu.Unlock()
	fmt.Println("Schedule cleared")
}

// runScheduler is a long-running goroutine that checks the schedule every minute
// and switches the theme when a boundary time is crossed.
func runScheduler() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		schedMu.Lock()
		lt, dt := schedLightTime, schedDarkTime
		schedMu.Unlock()
		if lt == "" || dt == "" {
			continue
		}
		isLight, err := shouldBeLightMode(lt, dt)
		if err != nil {
			log.Printf("Scheduler error: %v", err)
			continue
		}
		if isLight && isDark() {
			fmt.Println("Scheduler: switching to light mode")
			setLightModeTheme()
		} else if !isLight && !isDark() {
			fmt.Println("Scheduler: switching to dark mode")
			setDarkModeTheme()
		}
	}
}

// loadScheduleFromRegistry returns the stored light and dark times from the registry.
// Returns empty strings when no schedule is stored.
func loadScheduleFromRegistry() (string, string) {
	k, err := registry.OpenKey(registry.CURRENT_USER, REGKEY_SCHEDULE, registry.QUERY_VALUE)
	if err != nil {
		return "", ""
	}
	defer k.Close()
	lt, _, err := k.GetStringValue("LightTime")
	if err != nil {
		return "", ""
	}
	dt, _, err := k.GetStringValue("DarkTime")
	if err != nil {
		return "", ""
	}
	return lt, dt
}

// saveScheduleToRegistry persists the schedule times so they survive restarts.
func saveScheduleToRegistry(lightTime, darkTime string) {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, REGKEY_SCHEDULE, registry.SET_VALUE)
	if err != nil {
		log.Printf("Failed to save schedule to registry: %v", err)
		return
	}
	defer k.Close()
	if err := k.SetStringValue("LightTime", lightTime); err != nil {
		log.Printf("Failed to write LightTime to registry: %v", err)
	}
	if err := k.SetStringValue("DarkTime", darkTime); err != nil {
		log.Printf("Failed to write DarkTime to registry: %v", err)
	}
}

// clearScheduleFromRegistry removes the persisted schedule from the registry.
func clearScheduleFromRegistry() {
	k, err := registry.OpenKey(registry.CURRENT_USER, REGKEY_SCHEDULE, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	if err := k.DeleteValue("LightTime"); err != nil && err != registry.ErrNotExist {
		log.Printf("Failed to delete LightTime from registry: %v", err)
	}
	if err := k.DeleteValue("DarkTime"); err != nil && err != registry.ErrNotExist {
		log.Printf("Failed to delete DarkTime from registry: %v", err)
	}
}

// inputDialogProc is the window procedure for the time-entry input dialog.
// It is referenced via the package-level inputDialogWndProc callback.
func inputDialogProc(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_COMMAND:
		id := wParam & 0xFFFF
		switch id {
		case ID_DIALOG_OK:
			buf := make([]uint16, 32)
			getWindowTextW.Call(uintptr(inputEditHwnd), uintptr(unsafe.Pointer(&buf[0])), 32)
			inputResult = syscall.UTF16ToString(buf)
			inputOK = true
			inputDone = true
			destroyWindow.Call(uintptr(hwnd))
		case ID_DIALOG_CANCEL:
			inputOK = false
			inputDone = true
			destroyWindow.Call(uintptr(hwnd))
		}
		return 0
	case WM_CLOSE:
		inputOK = false
		inputDone = true
		destroyWindow.Call(uintptr(hwnd))
		return 0
	}
	ret, _, _ := defWindowProc.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return ret
}

// showInputDialog displays a minimal popup dialog with a text entry field.
// It returns the entered text and true on OK, or (defaultValue, false) on Cancel.
// Must be called from the main GUI thread.
func showInputDialog(title, prompt, defaultValue string) (string, bool) {
	// Prevent re-entrant calls that would corrupt shared dialog state.
	if inputDialogActive {
		return defaultValue, false
	}
	inputDialogActive = true
	defer func() { inputDialogActive = false }()

	// Reset dialog state.
	inputResult = defaultValue
	inputOK = false
	inputDone = false

	hInstance, _, _ := getModuleHandle.Call(0)

	// Register the dialog window class (errors are ignored; re-registration is harmless).
	// HbrBackground is set so the dialog background is properly painted.
	dialogClassName, _ := syscall.UTF16PtrFromString("ThemeInputDialogClass")
	wc := WNDCLASS{
		LpfnWndProc:   inputDialogWndProc,
		HInstance:     syscall.Handle(hInstance),
		HbrBackground: syscall.Handle(COLOR_BTNFACE_BRUSH),
		LpszClassName: dialogClassName,
	}
	registerClass.Call(uintptr(unsafe.Pointer(&wc)))

	// Center dialog on screen.
	const dlgW, dlgH = 380, 160
	screenW, _, _ := getSystemMetrics.Call(0) // SM_CXSCREEN
	screenH, _, _ := getSystemMetrics.Call(1) // SM_CYSCREEN
	x := (int(screenW) - dlgW) / 2
	y := (int(screenH) - dlgH) / 2

	// WS_POPUP | WS_CAPTION | WS_SYSMENU = 0x80C80000
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	dlgHwnd, _, _ := createWindowEx.Call(
		WS_EX_TOPMOST|WS_EX_DLGMODALFRAME,
		uintptr(unsafe.Pointer(dialogClassName)),
		uintptr(unsafe.Pointer(titlePtr)),
		0x80C80000,
		uintptr(x), uintptr(y), dlgW, dlgH,
		uintptr(hwnd), 0, hInstance, 0,
	)
	if dlgHwnd == 0 {
		return defaultValue, false
	}

	// Disable the parent window to enforce modal behaviour and prevent a second
	// context-menu interaction from re-entering this function.
	enableWindow.Call(uintptr(hwnd), 0)

	// Static text label. WS_CHILD | WS_VISIBLE = 0x50000000
	staticClass, _ := syscall.UTF16PtrFromString("STATIC")
	promptPtr, _ := syscall.UTF16PtrFromString(prompt)
	createWindowEx.Call(
		0,
		uintptr(unsafe.Pointer(staticClass)),
		uintptr(unsafe.Pointer(promptPtr)),
		0x50000000,
		12, 18, 348, 20,
		dlgHwnd, 0, hInstance, 0,
	)

	// Edit control. WS_CHILD | WS_VISIBLE | WS_TABSTOP | ES_AUTOHSCROLL = 0x50010080
	editClass, _ := syscall.UTF16PtrFromString("EDIT")
	defaultPtr, _ := syscall.UTF16PtrFromString(defaultValue)
	editHwnd, _, _ := createWindowEx.Call(
		WS_EX_CLIENTEDGE,
		uintptr(unsafe.Pointer(editClass)),
		uintptr(unsafe.Pointer(defaultPtr)),
		0x50010080,
		12, 46, 348, 26,
		dlgHwnd, 0, hInstance, 0,
	)
	inputEditHwnd = syscall.Handle(editHwnd)

	// OK button (BS_DEFPUSHBUTTON). WS_CHILD | WS_VISIBLE | WS_TABSTOP | BS_DEFPUSHBUTTON = 0x50010001
	buttonClass, _ := syscall.UTF16PtrFromString("BUTTON")
	okText, _ := syscall.UTF16PtrFromString("OK")
	createWindowEx.Call(
		0,
		uintptr(unsafe.Pointer(buttonClass)),
		uintptr(unsafe.Pointer(okText)),
		0x50010001,
		195, 92, 80, 28,
		dlgHwnd, ID_DIALOG_OK, hInstance, 0,
	)

	// Cancel button. WS_CHILD | WS_VISIBLE | WS_TABSTOP | BS_PUSHBUTTON = 0x50010000
	cancelText, _ := syscall.UTF16PtrFromString("Cancel")
	createWindowEx.Call(
		0,
		uintptr(unsafe.Pointer(buttonClass)),
		uintptr(unsafe.Pointer(cancelText)),
		0x50010000,
		285, 92, 80, 28,
		dlgHwnd, ID_DIALOG_CANCEL, hInstance, 0,
	)

	showWindow.Call(dlgHwnd, SW_SHOW)
	setFocus.Call(uintptr(inputEditHwnd))

	// Run a nested message loop until the dialog is dismissed.
	// IsDialogMessage handles Tab navigation and Enter (triggers default button).
	var msg MSG
	for !inputDone {
		ret, _, _ := getMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if ret == 0 { // WM_QUIT – repost so the outer message loop can exit cleanly
			postQuitMessage.Call(msg.WParam)
			break
		} else if ret == 0xFFFFFFFF { // Error
			break
		}
		r, _, _ := isDialogMessage.Call(dlgHwnd, uintptr(unsafe.Pointer(&msg)))
		if r != 0 {
			continue
		}
		translateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		dispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}

	// Re-enable parent window now that the dialog has been dismissed.
	enableWindow.Call(uintptr(hwnd), 1)

	return inputResult, inputOK
}

func getClockTime(tz string) string {
	t := time.Now()
	utc, _ := time.LoadLocation(tz)

	hour, min, sec := t.In(utc).Clock()
	return ItoaTwoDigits(hour) + ":" + ItoaTwoDigits(min) + ":" + ItoaTwoDigits(sec)
}

// ItoaTwoDigits time.Clock returns one digit on values, so we make sure to convert to two digits
func ItoaTwoDigits(i int) string {
	b := "0" + strconv.Itoa(i)
	return b[len(b)-2:]
}

// add to autorun
func SetAutoRun(run bool) error {

	ex, err := os.Executable()

	if err != nil {
		panic(err)
	}
	// executable_path=filepath.Dir(ex)
	// get the real path if it is a symlink
	exReal, err := filepath.EvalSymlinks(ex)
	if err != nil {
		panic(err)
	}

	k, err := registry.OpenKey(registry.CURRENT_USER, REGKEY_AUTORUN, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if run {
		if err := k.SetStringValue(APPNAME, exReal); err != nil {
			return err
		}
	} else {
		k.DeleteValue(APPNAME)
	}
	return nil
}

func isSetAutoRun() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, REGKEY_AUTORUN, registry.QUERY_VALUE)
	if err != nil {
		log.Fatal(err)
	}
	defer k.Close()
	_, _, err = k.GetStringValue(APPNAME)
	return err != registry.ErrNotExist

}
