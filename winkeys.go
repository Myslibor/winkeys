package winkeys

import (
	"syscall"
	"time"
)

var (
	user32 = syscall.NewLazyDLL("user32.dll")

	//send
	procSendInput = user32.NewProc("SendInput")

	//hook
	procSetWindowsHookEx    = user32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx = user32.NewProc("UnhookWindowsHookEx")
	procGetMessage          = user32.NewProc("GetMessageW")
	procPostThreadMessage   = user32.NewProc("PostThreadMessageW")
	procGetThreadId         = user32.NewProc("GetCurrentThreadId")
	procCallNextHookEx      = user32.NewProc("CallNextHookEx")
)

const iNPUT_KEYBOARD = 1
const kEYEVENTF_KEYUP = 0x0002

type Event struct {
	VK       uint16
	Down     bool
	Injected bool // if true key is not physical :}
}

func KeyDown(vk uint16) {
	sendKey(vk, false)
}

func KeyUp(vk uint16) {
	sendKey(vk, true)
}

func KeyTap(vk uint16) {
	KeyDown(vk)
	time.Sleep(50 * time.Millisecond)
	KeyUp(vk)
}
