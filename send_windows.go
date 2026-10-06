package winkeys

import "unsafe"

type keybdInput struct {
	WVk         uint16
	WScan       uint16
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

// this is only for the size :>
type mouseInput struct {
	Dx          int32
	Dy          int32
	MouseData   uint32
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

type input struct {
	Type uint32
	Ki   keybdInput
	Pad  [unsafe.Sizeof(mouseInput{}) - unsafe.Sizeof(keybdInput{})]byte
}

func sendKey(vk uint16, up bool) {
	in := input{
		Type: iNPUT_KEYBOARD,
		Ki:   keybdInput{WVk: vk},
	}

	//regarding DwFlags, from MS docs: "If specified, the key is being released.
	//If not specified, the key is being pressed."
	if up {
		in.Ki.DwFlags = kEYEVENTF_KEYUP
	}

	procSendInput.Call(1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in))
}