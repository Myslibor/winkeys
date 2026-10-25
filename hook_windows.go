package winkeys

import (
	"errors"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type kbdllHookStruct struct {
	VkCode    uint32
	ScanCode  uint32
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

const (
	WH_KEYBOARD_LL = 13 // Installs a hook procedure that monitors
	// low-level keyboard input events (from ms docs)
	WM_KEYDOWN     = 0x0100
	WM_SYSKEYDOWN  = 0x0104
	LLKHF_INJECTED = 0x10

	WM_QUIT = 0x0012
)

func hookCallback(nCode int32, wparam uintptr, lparam uintptr) uintptr {
	if nCode >= 0 {
		kb := (*kbdllHookStruct)(unsafe.Pointer(lparam))
		down := wparam == WM_KEYDOWN || wparam == WM_SYSKEYDOWN
		ev := Event{
			VK:       uint16(kb.VkCode),
			Down:     down,
			Injected: kb.Flags&LLKHF_INJECTED != 0,
		}

		select { //this is so a input doesnt stall for the os
		case eventCh <- ev:
		default:
		}
	}
	r1, _, _ := procCallNextHookEx.Call(0, uintptr(nCode), wparam, lparam)
	return r1
}

type point struct {
	X uint32
	Y uint32
}

type msg struct {
	Hwnd     uintptr
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       point
	LPrivate uint32 //tf is this
}

var ( //i want to be able to stop and the listening and later run a new one
	listenMu  sync.Mutex
	listening bool
)

func listen() (<-chan Event, func(), error) {
	listenMu.Lock()
	defer listenMu.Unlock()
	if listening { //windows allows only one of those so yeah
		return nil, nil, errors.New("winkeys: Listen is already running")
	}
	ch := make(chan Event)
	eventCh = ch
	tIDCh := make(chan uint32)
	errCh := make(chan error)

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		hook, _, err := procSetWindowsHookEx.Call(
			WH_KEYBOARD_LL,
			syscall.NewCallback(hookCallback),
			0,
			0,
		)

		if hook == 0 {
			errCh <- err
			close(eventCh)
			return
		}

		threadID := windows.GetCurrentThreadId()
		tIDCh <- threadID

		var msg msg
		for {
			r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if r == 0 || r == ^uintptr(0) { // Call produced nothing or a -1 error
				break
			}
		}

		procUnhookWindowsHookEx.Call(hook)
		close(eventCh)
	}()

	select {
	case tid := <-tIDCh:
		listening = true
		stop := func() {
			procPostThreadMessage.Call(uintptr(tid), WM_QUIT, 0, 0)
			listenMu.Lock()
			listening = false
			listenMu.Unlock()
		}
		return eventCh, stop, nil
	case err := <-errCh:
		return nil, nil, err
	}

}
