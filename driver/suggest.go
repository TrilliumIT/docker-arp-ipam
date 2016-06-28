package driver

import (
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/TrilliumIT/iputil"
	"net"
	"sync"
)

type addrSuggest struct {
	ipn *net.IPNet
	ch  chan<- *net.IPNet
	ech chan<- error
}

func genSuggestions(sgCh <-chan *addrSuggest, ncCh chan<- *neighCheck, ncUch chan<- *neighUseNotifier, quit <-chan struct{}, wg sync.WaitGroup) {
	max := 3
	sugs := make(map[string][]*net.IPNet)
	addSug := make(chan *net.IPNet)
	delSug := make(chan *net.IPNet)

	for {
	Sel:
		select {
		case _ = <-quit:
			return
		case sg := <-sgCh:
			if _, ok := sugs[sg.ipn.String()]; !ok {
				sugs[sg.ipn.String()] = []*net.IPNet{}
			}
			for i, s := range sugs[sg.ipn.String()] {
				if !tryAddress(&s.IP, ncCh) {
					log.Debugf("Returning suggested address: %v", s)
					sg.ch <- s
					sugs[sg.ipn.String()] = sugs[sg.ipn.String()][i+1:]
					break Sel
				}
			}
			// if we've made it this far, there are no suggested addresses
			r, err := getRandomAddr(sg.ipn, ncCh)
			if err != nil {
				log.Errorf("Error getting random address")
				sg.ech <- err
			}
			sg.ch <- r
			break Sel
		case a := <-addSug:
			for _, ad := range sugs[iputil.NetworkID(a).String()] {
				if ad.IP.Equal(a.IP) {
					break Sel
				}
			}
			sugs[iputil.NetworkID(a).String()] = append(sugs[iputil.NetworkID(a).String()], a)
		case a := <-delSug:
			for i, ad := range sugs[iputil.NetworkID(a).String()] {
				if !ad.IP.Equal(a.IP) {
					continue
				}
				sugs[iputil.NetworkID(a).String()] = append(sugs[iputil.NetworkID(a).String()][:i], sugs[iputil.NetworkID(a).String()][i+1:]...)
				break Sel
			}
		}

		// refill the sugestions
		log.Debugf("Checking and refilling suggestions")
		for ns, s := range sugs {
			if len(s) >= max {
				continue
			}
			log.Debug("Refilling suggestions for %v", ns)
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, n, err := net.ParseCIDR(ns)
				if err != nil {
					log.Errorf("Failed to parse CIDR: %v", ns)
					log.Error(err)
					return
				}
				r, err := getRandomAddr(n, ncCh)
				if err != nil {
					log.Errorf("Failed to get random address for suggestion")
					log.Error(err)
					return
				}
				addSug <- r
				nuch := make(chan struct{})
				nu := &neighUseNotifier{
					ip: &r.IP,
					ch: nuch,
				}
				// if notified that this address is in use, delete it
				ncUch <- nu
				select {
				case _ = <-quit:
					return
				case _ = <-nuch:
					delSug <- r
				}
			}()
		}
	}
}

func getRandomAddr(n *net.IPNet, ncCh chan<- *neighCheck) (*net.IPNet, error) {
	log.Debugf("Generating Random Address in network %v", n)
	triedAddresses := make(map[string]struct{})
	var e struct{}
	lastAddress := iputil.LastAddr(n)
	log.Debugf("Excluding last address: %v", lastAddress)
	triedAddresses[string(lastAddress)] = e
	firstAddress := iputil.FirstAddr(n)
	log.Debugf("Excluding network address: %v", firstAddress)
	triedAddresses[string(firstAddress)] = e
	ones, maskSize := n.Mask.Size()
	var totalAddresses int
	totalAddresses = 1 << uint8(maskSize-ones)
	log.Debugf("Address avaliable to try: %v", totalAddresses)
	for len(triedAddresses) < totalAddresses {
		try, err := iputil.RandAddr(n)
		log.Debugf("Trying random address: %v", try)
		if err != nil {
			log.Errorf("Error generating random address: %v", err)
			return nil, err
		}
		if _, ok := triedAddresses[string(try)]; ok {
			log.Debugf("Address already tried: %v", try)
			continue
		}

		if !tryAddress(&try, ncCh) {
			log.Debugf("Returning address: %v", try)
			return &net.IPNet{IP: try, Mask: n.Mask}, nil
		}
		triedAddresses[string(try)] = e
		log.Debugf("Address in use: %v", try)
	}

	log.Errorf("All avaliable addresses are in use")
	return nil, fmt.Errorf("All avaliable addresses are in use")
}