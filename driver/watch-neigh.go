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

type neighUseNotifier struct {
	ip *net.IP
	ch chan<- struct{}
}

type neighState struct {
	state int
	cbs   []chan<- bool     // callbacks to notify on address in use or not
	ncbs  []chan<- struct{} //channel to be closed if address comes in use
}

func (n *neighState) isKnown() bool {
	return n.state == netlink.NUD_FAILED || n.state == netlink.NUD_REACHABLE
}

func (n *neighState) isReachable() bool {
	return n.state != netlink.NUD_FAILED
}

func checkNeigh(ncCh <-chan *neighCheck, ncUCh <-chan *neighUseNotifier, quit <-chan struct{}) {
	nch := make(chan *netlink.Neigh)
	dch := make(chan struct{})
	defer close(dch)

	neighSubscribe(nch, dch)
	neighs := make(map[string]neighState)

	for {
		select {
		case _ = <-quit:
			return
		case n := <-nch:
			ns := neighs[n.IP.String()]
			ns.state = n.State
			if !ns.isKnown() {
				if len(ns.cbs) == 0 && len(ns.ncbs) == 0 {
					delete(neighs, n.IP.String())
				}
				if len(ns.cbs) > 0 {
					log.Debugf("Callbacks waiting on unknown neigh update. Probing %v", n.IP)
					probe(&n.IP)
				}
				break
			}
			if ns.isReachable() {
				for _, cb := range ns.ncbs {
					log.Debugf("Closing in use callback for %v", n.IP)
					close(cb)
				}
			}
			for _, cb := range ns.cbs {
				log.Debugf("Returning answer to callback for %v", n.IP)
				cb <- ns.isReachable()
			}
			ns.cbs = []chan<- bool{}
			neighs[n.IP.String()] = ns
		case n := <-ncCh:
			ns := neighs[n.ip.String()]
			if ns.isKnown() {
				log.Debugf("Already have answer for requested callback on %v", n.ip)
				n.ch <- ns.isReachable()
				break
			}
			log.Debugf("Registering callback on %v", n.ip)
			ns.cbs = append(ns.cbs, n.ch)
			neighs[n.ip.String()] = ns
			probe(n.ip)
		case n := <-ncUCh:
			ns := neighs[n.ip.String()]
			if ns.isKnown() && ns.isReachable() {
				log.Debugf("Already in use, closing callback on %v", n.ip)
				close(n.ch)
			}
			log.Debugf("Registering in use callback for %v", n.ip)
			ns.ncbs = append(ns.ncbs, n.ch)
			neighs[n.ip.String()] = ns
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
