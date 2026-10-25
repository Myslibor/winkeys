# winkeys

Windows keyboard listener and sender for Go, built on the Win32 low-level
keyboard hook (`WH_KEYBOARD_LL`).

## Install

    go get github.com/Myslibor/winkeys

## Listen to key events

    package main

    import (
        "fmt"
        "winkeys"
    )

    func main() {
        ch, stop, err := winkeys.Listen()
        if err != nil {
            panic(err)
        }
        defer stop()

        for ev := range ch {
            fmt.Printf("vk=0x%02X down=%v\n", ev.VK, ev.Down)
        }
    }

## Send key events

    winkeys.KeyTap(winkeys.VK_A) // press and release a key

Somehow it works, probably has problems, might fix it later
