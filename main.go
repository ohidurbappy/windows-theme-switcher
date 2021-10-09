package main

import (
	_ "embed"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/getlantern/systray"
	"golang.org/x/sys/windows/registry"
)

const (
	REGKEY_THEME_PERSONALIZE = `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize` // in HKCU
	REGKEY_AUTORUN           = `SOFTWARE\Microsoft\Windows\CurrentVersion\Run`
	REGNAME_TASKBAR_TRAY     = `SystemUsesLightTheme`
	REGNAME_APP_LIGHT_THEME  = `AppsUseLightTheme`
	APPNAME                  = `Windows Theme Switcher`
)

//go:embed assets/dark_mode.ico
var dark_mode []byte

//go:embed assets/light_mode.ico
var light_mode []byte

func main() {
	fmt.Println("Dark Mode on:", isDark())
	go monitor(react)
	systray.Run(onReady, onExit)

}

func onReady() {

	if !isSetAutoRun() {
		SetAutoRun(true)
	}

	if isDark() {
		// systray.SetIcon(getIcon("assets/light_mode.ico"))
		systray.SetIcon(light_mode)
	} else {
		// systray.SetIcon(getIcon("assets/dark_mode.ico"))
		systray.SetIcon(dark_mode)
	}

	systray.SetTitle("Theme Switch")
	systray.SetTooltip("Theme switcher")

	mLightMode := systray.AddMenuItem("Light Mode", "Switch to Light Mode")
	mDarkMode := systray.AddMenuItem("Dark Mode", "Switch to Dark Mode")
	mExit := systray.AddMenuItem("Exit", "Quit the program")

	go func() {
		for {
			select {
			case <-mLightMode.ClickedCh:
				fmt.Println("Set light mode")
				setLightModeTheme()
			case <-mDarkMode.ClickedCh:
				fmt.Println("Set dark mode")
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
	b, err := ioutil.ReadFile(s)
	if err != nil {
		fmt.Print(err)
	}
	return b
}

// react to the change
func react(isDark bool) {
	if isDark {
		systray.SetIcon(light_mode)

	} else {
		systray.SetIcon(dark_mode)

	}
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
	if err := k.SetDWordValue(REGNAME_TASKBAR_TRAY, themeMode); err != nil {
		log.Fatal(err)
	}

	if err := k.SetDWordValue(REGNAME_APP_LIGHT_THEME, themeMode); err != nil {
		log.Fatal(err)
	}

	if err := k.Close(); err != nil {
		log.Fatal(err)
	}

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

	if err == registry.ErrNotExist {
		// the key value doesn't exit
		return false
	}

	return true

}
