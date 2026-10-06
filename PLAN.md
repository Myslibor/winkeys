# Plan: `winkeys` — cgo-free Windows key listener + simulator module

> **Goal:** replace `github.com/robotn/gohook` (listener) and `github.com/go-vgo/robotgo` (key simulation) — both cgo-based — with a new minimalistic Windows-only Go module using only the standard library `syscall` package and `golang.org/x/sys/windows`.
>
> **Reading guide:** every build step below has three parts: *why it works* (Windows internals), *what to write* (concrete API/struct guidance), and *how to verify* (a manual test you can observe). Reference links point to the authoritative docs (Microsoft Learn for Win32, pkg.go.dev for Go).

---

## 0. Background: why cgo is the problem

`robotgo` and `gohook` both use cgo — they `import "C"` directly or depend on packages that do. This has three practical consequences for a Go project:

| Problem | Explanation |
|---|---|
| **Build toolchain** | cgo requires a working C compiler (gcc/clang) on the build machine. On Windows that means mingw-w64, which is one more thing to install and keep in sync. See the [cgo docs](https://pkg.go.dev/cmd/cgo). |
| **Cross-compilation** | With cgo, cross-compiling from e.g. Linux to Windows requires a cross-compiled C toolchain. With `CGO_ENABLED=0`, a plain `GOOS=windows go build` works with zero extra tooling. See [cmd/cgo docs](https://pkg.go.dev/cmd/cgo#hdr-Cross_compilation). |
| **Startup & portability** | cgo binaries dynamically link against C runtime quirks and carry a heavier runtime. A pure-Go binary using `syscall` is a single static `.exe` that starts fast. |

The key insight making a pure-Go rewrite possible: **Windows exposes both halves of what we need through plain DLLs** —

- *listening* to global key events: `user32.dll` → `SetWindowsHookExW` ([docs](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-setwindowshookexw))
- *simulating* key events: `user32.dll` → `SendInput` ([docs](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-sendinput))

Go can call those without cgo via the `syscall` package's dynamic-loading helpers (`syscall.NewLazyDLL`, [docs](https://pkg.go.dev/syscall#NewLazyDLL)) — this is exactly the mechanism `golang.org/x/sys/windows` ([docs](https://pkg.go.dev/golang.org/x/sys/windows)) formalizes with typed wrappers. That is why a project can be "Windows-only, cgo-free, and still talk to the Win32 API".

---

## 1. Module scaffold

```
winkeys/
├── go.mod              // module io.github.myslibor/winkeys; require golang.org/x/sys
├── winkeys.go          // public types: Event, Listen signature, errors, package doc
├── keys.go             // VK constants (paste from internal/macro/keymaps.go)
├── hook_windows.go     // WH_KEYBOARD_LL listener
└── send_windows.go     // SendInput-based simulation
```

`go mod init io.github.myslibor/winkeys && go get golang.org/x/sys`

Notes:

- `//go:build windows` constraints are not strictly required (the module *is* Windows-only), but adding them keeps the files honest and lets `go vet` run on other platforms gracefully. See [Build constraints](https://pkg.go.dev/go/build#hdr-Build_Constraints).
- Naming a file `*_windows.go` restricts it to Windows builds automatically — Go's [implicit build constraints](https://pkg.go.dev/go/build#hdr-Build_Constraints) derive the GOOS from the filename suffix. This is idiomatic; it's how `x/sys/windows` itself is structured.

---

## 2. Build order (each step verifiable before the next)

### Step 1: `send_windows.go` — smallest, testable in isolation

#### Why SendInput

`SendInput` ([Microsoft docs](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-sendinput)) is the modern way to inject input events. It supersedes `keybd_event` (which is emulated on top of it for backwards compatibility) and supersedes the older, *deprecated* `WM_KEYDOWN`-posting tricks. Its signature:

```c
UINT SendInput(
  UINT cInputs,          // number of INPUT structs
  LPINPUT pInputs,       // array
  int  cbSize            // sizeof(INPUT) — sanity check by the OS
);
```

It takes an array of [`INPUT`](https://learn.microsoft.com/en-us/windows/win32/api/winuser/ns-winuser-input) structs. `INPUT` is a tagged union: `type` selects `MOUSEINPUT`, `KEYBDINPUT`, or `HARDWAREINPUT`. For keyboard events we use [`KEYBDINPUT`](https://learn.microsoft.com/en-us/windows/win32/api/winuser/ns-winuser-keybdinput):

```c
typedef struct tagKEYBDINPUT {
  WORD wVk;         // virtual-key code  (the "which key" number)
  WORD wScan;       // hardware scancode  (which physical key on the keyboard)
  DWORD dwFlags;    // KEYEVENTF_* flags
  DWORD time;       // timestamp; 0 = let the OS fill it in
  ULONG_PTR dwExtraInfo; // opaque; often used to tag injected input
} KEYBDINPUT;
```

The `dwFlags` field is where behavior lives ([key event flags](https://learn.microsoft.com/en-us/windows/win32/inputdev/about-keyboard-input)):

- `0` → key **down**
- `KEYEVENTF_KEYUP` (0x0002) → key **up**
- `KEYEVENTF_SCANCODE` (0x0008) → interpret `wScan` instead of `wVk`; what most games require, because games read raw scancodes from DirectInput rather than translated VK codes
- `KEYEVENTF_EXTENDEDKEY` (0x0001) → for keys on the "extended" half of the keyboard (right Ctrl, numpad Enter, arrows/nav cluster when NumLock is off). See [Extended-Key Flag](https://learn.microsoft.com/en-us/windows/win32/inputdev/about-keyboard-input#extended-key-flag).

#### The Go side

**Important, verified fact:** `golang.org/x/sys/windows` does *not* declare `SendInput`, `INPUT`, or `KEYBDINPUT` (checked against x/sys v0.48.0 — `go doc golang.org/x/sys/windows.SendInput` → "no symbol"; the package has keyboard-adjacent helpers like `ToUnicodeEx` and `LoadKeyboardLayout`, but nothing for input injection). So we declare the structs ourselves — this is safe and idiomatic because the Win32 ABI fixes the layout — and load the function through `user32.dll`:

```go
var procSendInput = user32.NewProc("SendInput")
```

The Go structs must mirror the C memory layout *exactly* — `SendInput` takes `cbSize` precisely so the OS can reject callers with a mismatched struct definition:

```go
// Mirrors winuser.h (https://learn.microsoft.com/en-us/windows/win32/api/winuser/ns-winuser-keybdinput)
type keybdInput struct {
    Wvk         uint16   // wVk
    Wscan       uint16   // wScan
    DwFlags     uint32   // dwFlags
    Time        uint32   // time
    DwExtraInfo uintptr  // dwExtraInfo (ULONG_PTR — pointer-sized!)
}

// Mirrors the INPUT tagged union (https://learn.microsoft.com/en-us/windows/win32/api/winuser/ns-winuser-input).
// Windows declares it as union { MOUSEINPUT mi; KEYBDINPUT ki; HARDWAREINPUT hi; }
// where MOUSEINPUT is the largest member. In C, Go-style explicit padding
// is unnecessary; here we size the union manually.
type mouseInput struct { // needed only so INPUT has the correct total size
    Dx          int32
    Dy          int32
    MouseData   uint32
    DwFlags     uint32
    Time        uint32
    DwExtraInfo uintptr
}

type input struct {
    Type uint32     // INPUT_KEYBOARD = 1
    Ki   keybdInput // the union member we actually use
    // Union storage must be as large as its largest member (MOUSEINPUT).
    // This pad array is computed at compile time and is correct on every GOARCH:
    //   amd64/arm64: sizeof(mouseInput)=32, sizeof(keybdInput)=24 → pad 8, total 40
    //   386/arm:    sizeof(mouseInput)=24, sizeof(keybdInput)=16 → pad 8, total 28
    // unsafe.Sizeof is a compile-time constant for fixed-size types
    // (https://pkg.go.dev/unsafe#Sizeof), so it is legal in an array length.
    Pad [unsafe.Sizeof(mouseInput{}) - unsafe.Sizeof(keybdInput{})]byte
}
```

Two layout details that bite people here:

1. `dwExtraInfo` is `ULONG_PTR` — **pointer-sized** (8 bytes on x64), not `uint32`. Getting this wrong shifts the second element of the array and every event after the first is garbage.
2. The union must be as large as `MOUSEINPUT` — the largest member — or `cbSize` lands wrong and `SendInput` fails. Two valid strategies:
   - **Ki + fixed pad** (shown above): declare the member we use, then pad the rest with a byte array whose length is computed from the two member sizes at compile time. No `unsafe` needed in the fill path.
   - **Embed the largest member**: `Mi mouseInput` and fill it via `ki := (*keybdInput)(unsafe.Pointer(&in.Mi))`. Same bytes, but every send does an unsafe cast.
   Note there is **no manual alignment pad after `Type`** in either variant: Go inserts the same natural-alignment padding as C (4 bytes on 64-bit, none on 32-bit). Adding an explicit `_ uint32` field would look correct on x64 but *break* 386/arm, where the C ABI puts the union at offset 4, not 8 — the layout test below catches exactly this mistake.

   Safety net for the whole exercise — a layout test that fails loudly if the numbers ever drift:

```go
func TestInputLayout(t *testing.T) {
    want := uintptr(40) // amd64, arm64 — matches winuser.h INPUT on x64
    if runtime.GOARCH == "386" || runtime.GOARCH == "arm" {
        want = 28
    }
    if got := unsafe.Sizeof(input{}); got != want {
        t.Fatalf("sizeof(INPUT) = %d, want %d", got, want)
    }
}
```

The wrapper API — three functions, no state:

```go
const (
    inputKeyboard = 1
    keyeventfKeyUp = 0x0002
)

func sendKey(vk uint16, up bool) {
    in := input{
        Type: inputKeyboard,
        Ki: keybdInput{Wvk: vk},
    }
    if up {
        in.Ki.DwFlags = keyeventfKeyUp
    }
    // SendInput returns the number of events actually injected.
    // 0 means injection was blocked (UIPI, or cbSize mismatch).
    procSendInput.Call(1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in))
}

func Down(vk uint16) { sendKey(vk, false) }
func Up(vk uint16)   { sendKey(vk, true) }

func Tap(vk uint16) {
    Down(vk)
    time.Sleep(30 * time.Millisecond) // give the target app's message pump a moment
    Up(vk)
}
```

Why the small sleep in `Tap`: `SendInput` injects the event into the system input stream, but the target application processes it asynchronously through its own message loop. A zero-delay down+up can occasionally be coalesced or missed by slow apps (especially games polling once per frame). 10–50 ms is the empirically safe range; robotgo does the same internally.

Sanity check before trusting any of this: run `go doc golang.org/x/sys/windows | grep -i input` yourself — the point of this section is that *the plan was verified against the actual x/sys v0.48.0 contents*, not against folklore about what x/sys "surely has". If a future x/sys adds `SendInput`, the hand-rolled version still works; nothing here depends on x/sys at all except by convention.

#### Gotchas worth knowing now

- **UIPI (User Interface Privilege Isolation):** if the target window belongs to a process running *elevated* (as admin) and we are not, `SendInput` events are silently dropped. This is a Windows security feature ([UIPI overview](https://learn.microsoft.com/en-us/windows/win32/winmsg/about-messages-and-message-queues#user-interface-privilege-isolation)) that prevents exactly the kind of input spoofing we're doing from reaching higher-integrity processes. Out of scope — document it.
- `SendInput` is *atomic for the array*: all events in one call are injected before any others are interleaved (relevant later if we ever batch a whole macro in a single call).

#### Verify

Chicken-and-egg note: `winkeys.VK_F2` doesn't exist yet — it's a `keys.go` (Step 2) constant. Two ways around it:

- Simplest: pass the literal. `VK_F2` is `0x71` per the [virtual-key code table](https://learn.microsoft.com/en-us/windows/win32/inputdev/virtual-key-codes) — `winkeys.Tap(0x71)`.
- Or do Step 2 first: it's pure data with no dependencies, so the build order is a suggestion, not a contract.

Scratch harness `cmd/test/main.go` (same module, so the import needs no `replace`):

```go
package main

import (
    "fmt"
    "time"

    "io.github.myslibor/winkeys"
)

func main() {
    fmt.Println("Click into Notepad within 3 seconds...")
    time.Sleep(3 * time.Second)
    winkeys.Tap(winkeys.VK_A) // 0x41 — watch "a" appear in Notepad
}
```

Run with `go run ./cmd/test` from the module root. Two practical points:

- The 3-second sleep matters: `Tap` fires the instant the program starts, and `SendInput` delivers to whatever has *keyboard focus at that moment* — without the delay the character lands in your terminal.
- Verify with a character key (`VK_A`), not F2: Notepad has no visible reaction to F2. If you specifically want to watch an F2 arrive, the honest end-to-end check is Step 3's own listener printing `Injected=true` — which doubles as the proof for the next verify item.

`go doc` output checked against [x/sys/windows package docs](https://pkg.go.dev/golang.org/x/sys/windows).

---

### Step 2: `keys.go` — VK constants

#### What a virtual-key code is

A [virtual-key code (VK)](https://learn.microsoft.com/en-us/windows/win32/inputdev/virtual-key-codes) is a number naming a *logical* key, independent of keyboard layout and language. `0x41` is "the A key" whether the layout is US, Czech (where it prints `a` on a different physical spot), or Dvorak. The OS translates VK codes into characters later (via keyboard layout + `ToUnicode`), which is why a macro tool that stores *VK codes* works across layouts — it replays "press the key that was pressed", not "type this character".

VK codes are byte-sized (`0x00`–`0xFE`); the `winkeys.Event.VK` field is `uint16` only for uniformity with the existing keymaps (and because some media/app-launch keys live in `0xB0`–`0xE5` comfortably inside a byte anyway).

#### What to do

Copy the VK constants out of `internal/macro/keymaps.go` unchanged. The rawcodes in that file **are** Windows VK codes (0x41 `KeyA`, 0xA0 `ShiftLeft`, 0x2C `PrintScreen`, …) — gohook passes Windows VK codes through as "rawcodes" — so this is pure data migration, zero risk:

```go
package winkeys

// Virtual-key codes (https://learn.microsoft.com/en-us/windows/win32/inputdev/virtual-key-codes)
const (
    VK_BACK      = 0x08
    VK_TAB       = 0x09
    VK_RETURN     = 0x0D
    VK_SHIFT     = 0x10
    VK_CONTROL   = 0x11
    VK_MENU      = 0x12 // Alt
    // ...
    VK_ESCAPE    = 0x1B
    VK_SPACE     = 0x20
    // ...
    VK_LEFT      = 0x25
    VK_UP        = 0x26
    VK_RIGHT     = 0x27
    VK_DOWN      = 0x28
    // ...
    VK_LWIN      = 0x5B
    VK_RWIN      = 0x5C
    // ...
    VK_F2        = 0x71
    // ...
    VK_LSHIFT   = 0xA0
    VK_RSHIFT   = 0xA1
    VK_LCONTROL = 0xA2
    VK_RCONTROL = 0xA3
    VK_LMENU    = 0xA4
    VK_RMENU    = 0xA5
)
```

Cross-check a handful against the official table ([virtual-key codes](https://learn.microsoft.com/en-us/windows/win32/inputdev/virtual-key-codes)) as a smoke test of the copy — not because we doubt the copy, but because it also validates the *claim* that keymaps rawcodes == VK codes before we wire everything to it in Step 4.

---

### Step 3: `hook_windows.go` — the listener

This is the meat of the module. It uses four mechanisms that need understanding, not just copying:

#### 3.1 Windows hooks — what `WH_KEYBOARD_LL` actually is

A [hook](https://learn.microsoft.com/en-us/windows/win32/winmsg/about-hooks) is an OS-managed interception point in the message pipeline. `SetWindowsHookExW` ([docs](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-setwindowshookexw)) installs one:

```c
HHOOK SetWindowsHookExW(
  int       idHook,      // WH_KEYBOARD_LL = 13
  HOOKPROC  lpfn,        // our callback
  HINSTANCE hmod,        // NULL for low-level hooks (no DLL needed)
  DWORD     dwThreadId); // 0 = all threads on this desktop (global)
);
```

`WH_KEYBOARD_LL` ([about hooks](https://learn.microsoft.com/en-us/windows/win32/winmsg/about-hooks#wh_keyboard_ll)) is the *low-level* keyboard hook: unlike `WH_KEYBOARD` (which requires a DLL injected into every target process), the LL variant is a **global hook the OS calls in the context of the installing process**. That's why pure Go works at all: our callback runs in *our* process, on the thread that installed the hook.

The callback is a [`LowLevelKeyboardProc`](https://learn.microsoft.com/en-us/windows/win32/winmsg/lowlevelkeyboardproc):

```c
LRESULT CALLBACK LowLevelKeyboardProc(
  int nCode,     // if >= 0, must process; if < 0, pass through
  WPARAM wParam,  // WM_KEYDOWN / WM_KEYUP / WM_SYSKEYDOWN / WM_SYSKEYUP
  LPARAM lParam   // pointer to KBDLLHOOKSTRUCT
);
```

And the payload, [`KBDLLHOOKSTRUCT`](https://learn.microsoft.com/en-us/windows/win32/api/winuser/ns-winuser-kbdllhookstruct):

```c
typedef struct tagKBDLLHOOKSTRUCT {
  DWORD vkCode;      // virtual-key code — the same VK universe as Step 1/2
  DWORD scanCode;    // hardware scancode
  DWORD flags;       // LLKHF_* bits (see below)
  DWORD time;        // event timestamp
  ULONG_PTR dwExtraInfo; // sender-supplied tag (used by SendInput pass-through)
} KBDLLHOOKSTRUCT;
```

The `flags` field is the crucial one for us ([member docs](https://learn.microsoft.com/en-us/windows/win32/api/winuser/ns-winuser-kbdllhookstruct#fields)):

- `LLKHF_INJECTED` (0x10): the event was generated by `SendInput`/`keybd_event` — i.e. **by us, or any other automation tool** — rather than a physical keypress. This is the flag that replaces the current `pending` echo-suppression map in `listener_handler.go`: instead of predicting "I injected N events, skip the next N", we simply look at ground truth.
- `LLKHF_LOWER_IL_INJECTED` (0x02): injected by a lower-integrity process (the UIPI counterpart on the receive side).
- `LLKHF_UP` (0x80): this is a key-up event (also implied by `wParam == WM_KEYUP`).

#### 3.2 The message-loop requirement

The non-obvious Windows rule: **a low-level hook callback is only invoked while the installing thread is pumping messages.** The OS delivers hook invocations as window messages posted to the installing thread, so if that thread does not call `GetMessageW` ([docs](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-getmessage)), the callback never fires and the OS eventually silently removes the hook (after `LowLevelHooksTimeout`, ~300 ms of non-response). See [message queues](https://learn.microsoft.com/en-us/windows/win32/winmsg/about-messages-and-message-queues).

Hence the architecture:

- `Listen()` spawns a dedicated goroutine that:
  1. calls `runtime.LockOSThread()` ([docs](https://pkg.go.dev/runtime#LockOSThread)) — Go's scheduler migrates goroutines between OS threads; a Windows hook is bound to *the thread that installed it*, so we must pin the goroutine to one thread for the hook's lifetime
  2. installs the hook
  3. runs a classic Win32 message loop
  4. on `WM_QUIT` (0x0012): unhooks, drains, closes the channel
- `stop()` is called from *any* goroutine and works by [posting](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-postthreadmessagea) `WM_QUIT` to the hook thread — the same pattern `golang.org/x/sys/windows/svc` uses to shut down service loops.

#### 3.3 The Go mechanics

Proc declarations:

```go
var (
    user32               = syscall.NewLazyDLL("user32.dll")
    procSetWindowsHookEx  = user32.NewProc("SetWindowsHookExW")
    procUnhookWindowsHookEx = user32.NewProc("UnhookWindowsHookEx")
    procGetMessage        = user32.NewProc("GetMessageW")
    procPostThreadMessage = user32.NewProc("PostThreadMessageW")
    procGetThreadId       = user32.NewProc("GetCurrentThreadId")
)
```

The `KBDLLHOOKSTRUCT` arrives as a raw pointer in `lParam` — we read it once, by value, into Go memory:

```go
type kbdllHookStruct struct {
    VkCode      uint32
    ScanCode    uint32
    Flags       uint32
    Time        uint32
    ExtraInfo   uintptr
}

func hookCallback(nCode int32, wparam uintptr, lparam uintptr) uintptr {
    if nCode >= 0 {
        kb := (*kbdllHookStruct)(unsafe.Pointer(lparam))
        down := wparam == WM_KEYDOWN || wparam == WM_SYSKEYDOWN
        ev := Event{
            VK:       uint16(kb.VkCode),
            Down:     down,
            Injected: kb.Flags&LLKHF_INJECTED != 0, // 0x10
        }
        select {
        case eventCh <- ev: // non-blocking send
        default:            // buffer full -> drop; the hook must not stall
        }
    }
    // Pass the event on so other apps still receive it.
    return procCallNextHookEx.Call(0, uintptr(nCode), wparam, lparam)[0]
}
```

Three things in this snippet are load-bearing:

1. **`syscall.NewCallback`** ([docs](https://pkg.go.dev/syscall#NewCallback)) converts a Go func into a Win32 callback pointer. Its hard limits: only one per call site at a time is fine, but **the callback must not throw** (a panic in a `NewCallback` function crashes the process — no recover possible), and there's a fixed-size callback table (~2000 per process). Keep the body tiny; do all real work elsewhere.
2. **Non-blocking channel send with a buffered chan (cap 128)** — the callback runs on a thread the OS considers critical: if it takes longer than `LowLevelHooksTimeout` (~300 ms, [registry-tunable](https://learn.microsoft.com/en-us/windows/win32/winmsg/about-hooks)), Windows silently removes the hook. Never block in the callback; drop events instead.
3. **`CallNextHookEx`** ([docs](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-callnexthookex)) — always call it. Returning non-zero would *swallow* the keystroke system-wide (that's how keyloggers block keys; here it would make the user's keyboard unusable while recording).

The loop and lifecycle:

```go
func listen() (chan Event, func(), error) {
    eventCh := make(chan Event, 128)

    go func() {
        runtime.LockOSThread()
        defer runtime.UnlockOSThread()

        hook, _, err := procSetWindowsHookEx.Call(
            WH_KEYBOARD_LL, // 13
            syscall.NewCallback(hookCallback),
            0,               // hmod — unused for LL hooks
            0,               // dwThreadId 0 = global
        )
        if hook == 0 {
            close(eventCh)
            return
        }

        threadID, _, _ := procGetThreadId.Call()

        var msg msg  // MSG struct: hwnd, message, wParam, lParam, time, pt
        for {
            r, _, _ := procGetMessage.Call( // blocks until a message arrives
                uintptr(unsafe.Pointer(&msg)), 0, 0, 0,
            )
            if r == 0 || r == ^uintptr(0) { // WM_QUIT or -1 error
                break
            }
        }

        procUnhookWindowsHookEx.Call(hook)
        close(eventCh)
    }()

    stop := func() { procPostThreadMessage.Call(threadID, WM_QUIT, 0, 0) }
    return eventCh, stop, nil
}
```

(`threadID` needs to escape the goroutine before `stop` is usable — in the real implementation, capture it via a channel or start the goroutine after obtaining the ID; the snippet shows the *shape*, and the build step should resolve that ordering deliberately.)

#### 3.4 Why "single listener at a time" is the constraint we accept

Windows itself allows multiple LL hooks (they chain), but our module design (one package-level callback, one buffered channel, one hook thread) supports one `Listen()` cleanly. Multiple hooks would need either a registry of callbacks (fights `NewCallback`'s fixed table) or a multiplexing layer — not worth it for this app. Second `Listen()` returns an error; documented constraint.

#### Verify

- Print events to stdout while typing in another window — global capture works.
- Simulate with our own `Tap` and confirm the event shows `Injected=true` — this is the proof that the `pending` map in `listener_handler.go` can be deleted (Step 4/integration).

---

### Step 4: `winkeys.go` — public API polish

Exactly the surface the app uses, nothing more — a small API is the point of the module:

```go
// Package winkeys provides a cgo-free global keyboard listener and
// simulator for Windows, using only syscall and golang.org/x/sys/windows.
package winkeys

// Event is a single keyboard event observed globally.
type Event struct {
    VK       uint16 // Windows virtual-key code
    Down     bool   // true = key press, false = key release
    Injected bool   // true = generated by SendInput (ours or another tool), not a physical key
}

// Listen starts a global low-level keyboard hook.
// It returns the event stream, a stop function, and any setup error.
// Only one Listen may be active at a time.
func Listen() (<-chan Event, stop func(), error)

// Tap presses and releases a key.
func Tap(vk uint16)

// Down presses a key. Pair with Up; see Extended-Key Flag notes
// for edge cases (numpad Enter, right Ctrl).
func Down(vk uint16)

// Up releases a key.
func Up(vk uint16)
```

Deliberate omissions (documented, not implemented): text/character input (layout-dependent — needs `ToUnicode`/`MapVirtualKey`, [docs](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-mapvirtualkeyw)), mouse events, scancode-based injection (a `TapScan` variant using `KEYEVENTF_SCANCODE` for games — see the gotcha table).

---

## 3. Integration into macro-tool-go

1. **Local dep first** (no publishing yet):

   ```
   require io.github.myslibor/winkeys v0.0.0
   replace io.github.myslibor/winkeys => ../winkeys
   ```

   ([Modules reference: replace directive](https://go.dev/ref/mod#go-mod-file-replace))

2. **Rewrite `internal/macro/listener_handler.go`:**

   | Before (gohook/robotgo) | After (winkeys) | Why it's safe |
   |---|---|---|
   | `ev.Kind == hook.KeyDown` | `ev.Down && !ev.Injected` | gohook's Kind conflated press events with injected ones; winkeys splits them explicitly |
   | `ev.Rawcode` | `ev.VK` | keymaps rawcodes ARE VK codes — keymaps.go untouched, numeric values identical |
   | `robotgo.KeyTap(...)` | `winkeys.Tap(jsCodeToRawcode[...])` | same VK code, same SendInput underneath — robotgo's KeyTap is a SendInput wrapper too |
   | `pending map[uint16]int` echo suppression | *(deleted)* | the `Injected` flag is ground truth; the pending map was a heuristic that desynced whenever robotgo's internal count disagreed with reality |

   Deleting the `pending` map is the payoff of Step 3's `LLKHF_INJECTED` handling: the old code had to guess "this event is probably my own echo"; the new code *knows*.

3. **`go mod tidy`** — `robotgo`, `gohook`, `vcaesar/keycode` drop out of the graph entirely.

4. **Prove no cgo:** `CGO_ENABLED=0 go build ./...`. This is the acceptance test for the whole refactor: if any transitive dependency still needs cgo, this command fails loudly. ([cmd/go docs on CGO_ENABLED](https://pkg.go.dev/cmd/go#hdr-Environment_Variables))

---

## 4. Acceptance checklist

- [ ] `CGO_ENABLED=0 go build ./...` succeeds for both modules
- [ ] Global keydown/keyup events arrive while another window has focus
- [ ] Own injected events filtered via `Injected` flag (no echo loop, no `pending` map)
- [ ] `stop()` cleanly unhooks and closes the channel; a second `Listen()` works after
- [ ] Macro recording + playback behaves identically to the robotgo/gohook version
- [ ] Key repeat (multiple KeyDowns without KeyUp) — handled by existing `pressed` map, verify it still holds with winkeys' native repeat delivery
- [ ] System stays responsive while the hook is installed (no swallowed keystrokes — proof that `CallNextHookEx` is always called)
- [ ] Typing fast during a macro *playback* floods neither the 128-buffer (drops are OK) nor stalls the system (callback never blocks)

## 5. Known gotchas

| Gotcha | Why it exists | Handling |
|---|---|---|
| Callback must not block | OS removes the hook after `LowLevelHooksTimeout` (~300 ms) of unresponsiveness ([hooks overview](https://learn.microsoft.com/en-us/windows/win32/winmsg/about-hooks#hook-procedures)) | non-blocking send, drop on full buffer |
| Hook thread must pump messages | LL hook invocations are delivered as messages to the installing thread ([message queues](https://learn.microsoft.com/en-us/windows/win32/winmsg/about-messages-and-message-queues)) | `LockOSThread` + `GetMessageW` loop |
| Elevated apps ignore SendInput | UIPI blocks lower-integrity processes from injecting input into higher ones ([UIPI](https://learn.microsoft.com/en-us/windows/win32/winmsg/about-messages-and-message-queues#user-interface-privilege-isolation)) | document, out of scope |
| Games want scancodes | many games read raw DirectInput scancodes and ignore VK-based injection | later: optional `KEYEVENTF_SCANCODE` variant of Tap ([flags](https://learn.microsoft.com/en-us/windows/win32/api/winuser/ns-winuser-keybdinput#parameters)) |
| NewCallback limits | fixed-size callback table, no panics allowed in callbacks ([syscall.NewCallback](https://pkg.go.dev/syscall#NewCallback)) | keep `KBDLLHOOKSTRUCT` read by pointer, no boxing; tiny callback body |
| Single listener at a time | one callback, one channel, one hook thread in the module design | document as constraint; second `Listen()` returns error |
| Hook silently dies | if the callback is too slow or the thread stops pumping, Windows removes the hook without notice | stop() is idempotent; optional future: watchdog re-installing the hook |

## Reference links

Win32 (Microsoft Learn):

- Hooks: [About hooks](https://learn.microsoft.com/en-us/windows/win32/winmsg/about-hooks) · [SetWindowsHookExW](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-setwindowshookexw) · [LowLevelKeyboardProc](https://learn.microsoft.com/en-us/windows/win32/winmsg/lowlevelkeyboardproc) · [KBDLLHOOKSTRUCT](https://learn.microsoft.com/en-us/windows/win32/api/winuser/ns-winuser-kbdllhookstruct) · [UnhookWindowsHookEx](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-unhookwindowshookex) · [CallNextHookEx](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-callnexthookex)
- Message loop: [GetMessageW](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-getmessage) · [PostThreadMessageW](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-postthreadmessagew) · [About messages and queues](https://learn.microsoft.com/en-us/windows/win32/winmsg/about-messages-and-message-queues)
- Input injection: [SendInput](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-sendinput) · [INPUT](https://learn.microsoft.com/en-us/windows/win32/api/winuser/ns-winuser-input) · [KEYBDINPUT](https://learn.microsoft.com/en-us/windows/win32/api/winuser/ns-winuser-keybdinput) · [Virtual-key codes](https://learn.microsoft.com/en-us/windows/win32/inputdev/virtual-key-codes) · [About keyboard input](https://learn.microsoft.com/en-us/windows/win32/inputdev/about-keyboard-input)

Go:

- [syscall.NewLazyDLL](https://pkg.go.dev/syscall#NewLazyDLL) · [syscall.NewCallback](https://pkg.go.dev/syscall#NewCallback) · [runtime.LockOSThread](https://pkg.go.dev/runtime#LockOSThread) · [golang.org/x/sys/windows](https://pkg.go.dev/golang.org/x/sys/windows) · [cmd/cgo](https://pkg.go.dev/cmd/cgo) · [Go modules: replace](https://go.dev/ref/mod#go-mod-file-replace)

Replaced dependencies (for contrast, not for use): [robotn/gohook](https://github.com/robotn/gohook) · [go-vgo/robotgo](https://github.com/go-vgo/robotgo)

Estimate: ~250–300 lines total across the module.
