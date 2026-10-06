package main

import (
	"fmt"
	"time"

	"io.github.myslibor/winkeys"
)

func main() {
	fmt.Println("Click into Notepad within 3 seconds...")
	time.Sleep(3 * time.Second)
	winkeys.KeyTap(0x41) // VK_A — watch "a" appear (keys.go not written yet, so the literal)
}
