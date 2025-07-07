//go:build windows
// +build windows

package main

import (
	_ "embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
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
	NIM_ADD        = 0x00000000
	NIM_MODIFY     = 0x00000001
	NIM_DELETE     = 0x00000002
	NIF_MESSAGE    = 0x00000001
	NIF_ICON       = 0x00000002
	NIF_TIP        = 0x00000004
	WM_USER        = 0x0400
	WM_TRAYICON    = WM_USER + 1
	WM_LBUTTONUP   = 0x0202
	WM_RBUTTONUP   = 0x0205
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
)

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

var (
	hwnd        syscall.Handle
	lightIcon   syscall.Handle
	darkIcon    syscall.Handle
)

func main() {
	fmt.Println("Dark Mode on:", isDark())
	
	if !isSetAutoRun() {
		SetAutoRun(true)
	}
	
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
	if iconCount == 0 || len(data) < 6 + int(iconCount)*16 {
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
	imageData := data[imageOffset:imageOffset+imageSize]
	
	// Create icon from the image data
	icon, _, _ := createIconFromResourceEx.Call(
		uintptr(unsafe.Pointer(&imageData[0])), // pointer to icon data
		uintptr(imageSize),                      // size of icon data  
		1,                                       // fIcon = TRUE (this is an icon, not cursor)
		0x00030000,                             // dwVersion = 0x00030000
		0,                                       // cxDesired = 0 (use default)
		0,                                       // cyDesired = 0 (use default)  
		0,                                       // Flags = 0 (default behavior)
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
		CbSize:           uint32(unsafe.Sizeof(NOTIFYICONDATA{})),
		Hwnd:             hwnd,
		UID:              1,
		UFlags:           NIF_ICON | NIF_TIP,
		HIcon:            getCurrentIcon(),
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

func windowProc(hwnd syscall.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_TRAYICON:
		if lParam == WM_LBUTTONUP {
			// Direct icon click - toggle theme!
			fmt.Println("Tray icon clicked - toggling theme")
			toggleTheme()
			updateTrayIcon()
		} else if lParam == WM_RBUTTONUP {
			// Right click could show a minimal context menu for "Exit" if needed
			// For now, we'll ignore right clicks to keep it truly 1-click
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
