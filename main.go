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

	"github.com/getlantern/systray"
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
)

//go:embed assets/dark_mode.ico
var dark_mode []byte

//go:embed assets/light_mode.ico
var light_mode []byte

// Windows API functions
var (
	user32                        = windows.NewLazySystemDLL("user32.dll")
	sendMessageW                  = user32.NewProc("SendMessageW")
	UpdatePerUserSystemParameters = syscall.NewLazyDLL("user32.dll").NewProc("UpdatePerUserSystemParameters")
)

func main() {
	fmt.Println("Dark Mode on:", isDark())
	go monitor(react)
	systray.Run(onReady, onExit)

}

func onReady() {

	if !isSetAutoRun() {
		SetAutoRun(true)
	}

	systray.SetTitle("Theme Switch")
	systray.SetTooltip("Theme switcher")

	mLightMode := systray.AddMenuItem("Light Mode", "Switch to Light Mode")
	mDarkMode := systray.AddMenuItem("Dark Mode", "Switch to Dark Mode")
	mExit := systray.AddMenuItem("Exit", "Quit the program")

	if isDark() {
		// systray.SetIcon(getIcon("assets/light_mode.ico"))
		systray.SetIcon(light_mode)
		mDarkMode.Disable()
	} else {
		// systray.SetIcon(getIcon("assets/dark_mode.ico"))
		systray.SetIcon(dark_mode)
		mLightMode.Disable()
	}

	go func() {
		for {
			select {
			case <-mLightMode.ClickedCh:
				fmt.Println("Set light mode")
				mDarkMode.Enable()
				mLightMode.Disable()
				setLightModeTheme()
			case <-mDarkMode.ClickedCh:
				fmt.Println("Set dark mode")
				mLightMode.Enable()
				mDarkMode.Disable()
				setDarkModeTheme()
			case <-mExit.ClickedCh:
				systray.Quit()
				return
			}

		}
	}()

}

func onExit() {
	systray.Quit()
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
	if isDark {
		systray.SetIcon(light_mode)
		// Refresh menu items if they exist
		updateMenuItems(isDark)
	} else {
		systray.SetIcon(dark_mode)
		// Refresh menu items if they exist
		updateMenuItems(isDark)
	}
}

// Helper function to update menu items based on current theme
func updateMenuItems(isDark bool) {
	// This would need to be implemented with access to menu items
	// For now this is a placeholder for the concept
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
