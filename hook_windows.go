package winkeys

import "unsafe"

type kbdllHookStruct struct {
	VkCode    uint32
	ScanCode  uint32
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

const (
        WM_KEYDOWN    = 0x0100
        WM_SYSKEYDOWN = 0x0104
        LLKHF_INJECTED = 0x10
)

func hookCallback(nCode int32, wparam uintptr, lparam uintptr) (uintptr, uintptr, error) {
	if nCode >= 0 {
		kb := (*kbdllHookStruct)(unsafe.Pointer(lparam))
		down := wparam == WM_KEYDOWN || wparam == WM_SYSKEYDOWN
		ev := Event{
			VK:       uint16(kb.VkCode),
			Down:     down,
			Injected: kb.Flags&LLKHF_INJECTED != 0, // 0x10
		}
		select {
		case eventCh <- ev: // non-blunsafe send
		default: // buffer full -> drop; the hook must not stall
		}
	}
	// Pass the event on so other apps still receive it.
	return procCallNextHookEx.Call(0, uintptr(nCode), wparam, lparam)
}