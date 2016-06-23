package driver

import (
	log "github.com/Sirupsen/logrus"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"net"
	"syscall"
)

type neighCheck struct {
	ip *net.IP
	ch chan<- bool
}

type neighState struct {
	state int
	cbs   []chan<- bool
}

func (n *neighState) isKnown() bool {
	return n.state == netlink.NUD_FAILED || n.state == netlink.NUD_REACHABLE
}

func (n *neighState) isReachable() bool {
	return n.state != netlink.NUD_FAILED
}

func checkNeigh(ncCh <-chan *neighCheck) {
	nch := make(chan *netlink.Neigh)
	dch := make(chan struct{})
	defer close(dch)

	neighSubscribe(nch, dch)
	neighs := make(map[string]neighState)

Main:
	for {
		select {
		case n := <-nch:
			ns := neighs[n.IP.String()]
			ns.state = n.State
			if !ns.isKnown() {
				if len(ns.cbs) == 0 {
					delete(neighs, n.IP.String())
					continue Main
				}
				probe(&n.IP)
				continue Main
			}
			for _, cb := range ns.cbs {
				cb <- ns.isReachable()
			}
			ns.cbs = []chan<- bool{}
		case n := <-ncCh:
			ns := neighs[n.ip.String()]
			if ns.isKnown() {
				n.ch <- ns.isReachable()
				continue Main
			}
			ns.cbs = append(ns.cbs, n.ch)
			probe(n.ip)
		}
	}
}

func probe(ip *net.IP) {
	conn, err := net.Dial("udp", ip.String()+":8765")
	if err != nil {
		log.Error(err)
		return
	}
	defer conn.Close()
	conn.Write([]byte("probe"))
	return
}

func neighSubscribe(ch chan<- *netlink.Neigh, done <-chan struct{}) error {
	s, err := nl.Subscribe(syscall.NETLINK_ROUTE, syscall.RTNLGRP_NEIGH)
	if err != nil {
		return err
	}
	if done != nil {
		go func() {
			<-done
			s.Close()
		}()
	}
	go func() {
		defer close(ch)
		for {
			msgs, err := s.Receive()
			if err != nil {
				panic(err)
			}
			for _, m := range msgs {
				n, err := netlink.NeighDeserialize(m.Data)
				if err != nil {
					log.Errorf("Error deserializing neighbor message %v", m.Data)
				}
				ch <- n
			}
		}
	}()

	return nil
}
