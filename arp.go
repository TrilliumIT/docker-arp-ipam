package main

import (
	"github.com/mdlayher/arp"
	"net"
	"fmt"
)

func main() {
	iface, err := net.InterfaceByName("eno1")
	if err != nil {
		panic(err)
	}
	client, err := arp.NewClient(iface)
	if err != nil {
		panic(err)
	}
	ip := net.ParseIP("10.1.6.252")
	err = client.Request(ip)
	if err != nil {
		panic(err)
	}

	packet, frame, err := client.Read()
	if err != nil {
		panic(err)
	}
	fmt.Printf("Packet: %v", packet)
	fmt.Printf("Frame: %v", frame)

	err = client.Close()
	if err != nil {
		panic(err)
	}
}
