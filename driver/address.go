package driver

import (
	"fmt"
	"net"
	"syscall"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"sync"
)

const neighChanLen = 256

func (d *Driver) tryAddress(addr *net.IPNet) error {
	r, err := d.ns.probeAndWait(addr)
	if err != nil {
		log.WithError(err).Fatal("Error determining if addr is reachable")
		return err
	}
	if r {
		return fmt.Errorf("Address already in use: %v", addr)
	}
	return nil
}

func (ns *neighSubscription) addrStatus(addr net.IP) (known, reachable bool) {
	neighList, err := netlink.NeighList(0, netlink.FAMILY_V4)
	if err != nil {
		log.WithError(err).Error("Error refreshing neighbor table.")
		return
	}
	for _, n := range neighList {
		if n.IP.Equal(addr) {
			return parseAddrStatus(&n)
		}
	}
	return
}

func parseAddrStatus(n *netlink.Neigh) (known, reachable bool) {
	if n != nil {
		known = n.State == netlink.NUD_FAILED || n.State == netlink.NUD_REACHABLE
		reachable = known && n.State != netlink.NUD_FAILED
		return
	}
	return
}

type neighSubscription struct {
	quit     <-chan struct{}
	addSubCh chan *subscription
}

type subscription struct {
	ip      *net.IPNet
	created time.Time
	sub     chan *netlink.Neigh
	close   chan struct{}
}

func (ns *neighSubscription) probeAndWait(addr *net.IPNet) (reachable bool, err error) {
	var known bool
	known, reachable = ns.addrStatus(addr.IP)
	if known {
		return
	}

	t := time.NewTicker(1 * time.Second)
	to := time.Now().Add(5 * time.Second)
	defer t.Stop()
	sub := ns.addSub(addr)
	defer sub.delSub()

	probe(addr.IP)
	for {
		select {
		case <-ns.quit:
			return
		case n := <-sub.sub:
			known, reachable = parseAddrStatus(n)
			if known {
				return
			}
		case <-t.C:
		}
		known, reachable = ns.addrStatus(addr.IP)
		if known {
			return
		}
		if time.Now().After(to) {
			return true, fmt.Errorf("Error determining reachability for %v", addr)
		}
		probe(addr.IP)
	}
}

func (ns *neighSubscription) addSub(ip *net.IPNet) *subscription {
	sub := &subscription{
		ip:      ip,
		created: time.Now(),
		sub:     make(chan *netlink.Neigh, neighChanLen),
		close:   make(chan struct{}),
	}
	go func() { ns.addSubCh <- sub }()
	return sub
}

func (sub *subscription) delSub() {
	close(sub.close)
}

type neighUpdate struct {
	time  time.Time
	neigh *netlink.Neigh
}

func newNeighSubscription(quit <-chan struct{}) *neighSubscription {
	ns := &neighSubscription{
		quit:     quit,
		addSubCh: make(chan *subscription),
	}
	return ns
}

func (ns *neighSubscription) start() error {
	quit := ns.quit
	defer close(ns.addSubCh)
	wg := sync.WaitGroup{}

	s, err := nl.Subscribe(syscall.NETLINK_ROUTE, syscall.RTNLGRP_NEIGH)
	if err != nil {
		return err
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-quit
		s.Close()
	}()

	neighSubCh := make(chan []*neighUpdate, neighChanLen)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			for {
				msgs, err := s.Receive()
				select {
				case <-quit:
					return
				default:
				}

				if err != nil {
					log.WithError(err).Error("Error recieving neighbor update")
				}
				t := time.Now()
				go func(t time.Time) {
					var ns []*neighUpdate
					for _, m := range msgs {
						n, err := netlink.NeighDeserialize(m.Data)
						if err != nil {
							log.Errorf("Error deserializing neighbor message %v", m.Data)
							continue
						}
						ns = append(ns, &neighUpdate{
							time:  t,
							neigh: n,
						})
					}
					neighSubCh <- ns
				}(t)
			}
		}
	}()

	subs := make(map[string][]*subscription)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-quit:
				return
			case sub := <-ns.addSubCh:
				subs[sub.ip.String()] = append(subs[sub.ip.String()], sub)
				continue
			case neighList := <-neighSubCh:
				for _, n := range neighList {
					sendNeighUpdates(n.neigh, subs[n.neigh.IP.String()])
				}
				continue
			}
		}
	}()

	wg.Wait()
	return nil
}

func sendNeighUpdates(n *netlink.Neigh, subs []*subscription) {
	l := len(subs)
	for i := range subs {
		j := l - i - 1 // loop in reverse order
		sub := subs[j]
		select {
		// Delete closed subs
		case <-sub.close:
			subs = append(subs[:j], subs[j+1:]...)
			close(sub.sub)
		// Send the update
		default:
			go func(sub *subscription, n *netlink.Neigh) {
				sub.sub <- n
			}(sub, n)
		}
	}
}

func probe(ip net.IP) {
	conn, err := net.Dial("udp", ip.String()+":8765")
	if err != nil {
		log.WithError(err).WithField("ip", ip).Error("Error creating probe connection.")
		return
	}
	if _, err := conn.Write([]byte("probe")); err != nil {
		log.WithError(err).WithField("ip", ip).Error("Error probing connection.")
	}
	if err := conn.Close(); err != nil {
		log.WithError(err).WithField("ip", ip).Error("Error clossing probe connection.")
	}
	return
}
