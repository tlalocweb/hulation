package main

import (
	"fmt"
	"time"
)

var shutdown chan bool

func main() {
	shutdown = make(chan bool)
	go keepaliveLoop(nil)

	time.Sleep(4 * time.Second)
	fmt.Printf("main: shutdown\n")
	shutdown <- true
	fmt.Printf("main: sent shutdown\n")
	time.Sleep(2 * time.Second)
}

func keepaliveLoop(eventC chan struct{}) {
	to := time.NewTimer(300 * time.Millisecond)

mainloop:
	for {
		select {
		case <-shutdown:
			fmt.Printf("keepaliveLoop: shutdown\n")
			break mainloop
			// case <-eventC:

		// //if event.Msg == "heartbeat"...
		// time.Sleep(300 * time.Millisecond) // Simulate reset work (delay could be partly dur to whatever is triggering the
		case <-to.C:
			fmt.Printf("heartbeat\n")
			// if !to.Stop() {
			// 	<-to.C
			// }
			to.Reset(300 * time.Millisecond)
		}
	}
	fmt.Printf("keepaliveLoop done\n")
}
