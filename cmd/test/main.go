package main

import (
	"fmt"
	"time"

	"io.github.myslibor/winkeys"
)

// test made with ai
func main() {
	fmt.Println("Click into Notepad within 3 seconds...")
	time.Sleep(3 * time.Second)
	winkeys.KeyTap(winkeys.VK_A) 

	ch, stop, err := winkeys.Listen()
	if err != nil { panic(err) }
	go func() { time.Sleep(3 * time.Second); winkeys.KeyTap(winkeys.VK_D) }()
	select {
	case ev, ok := <-ch:
		if !ok { return }
		fmt.Printf("vk=0x%02X down=%v injected=%v\n", ev.VK, ev.Down, ev.Injected)
	case <-time.After(10 * time.Second):
		fmt.Println("no key for 10s, exiting")
		return
	}
	stop()
	fmt.Println("done")
}
