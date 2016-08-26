package main

import (
	log "github.com/Sirupsen/logrus"
	"github.com/TrilliumIT/docker-arp-ipam/driver"
	"github.com/docker/go-plugins-helpers/ipam"
	"github.com/urfave/cli"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

func main() {

	app := cli.NewApp()
	app.Name = "docker-arp-ipam"
	app.Usage = "Docker ARP IPAM Plugin"
	app.Version = "0.6"
	app.Flags = []cli.Flag{
		cli.BoolFlag{
			Name:  "debug, d",
			Usage: "Enable debugging.",
		},
		cli.StringFlag{
			Name:  "plugin-name, name",
			Value: "arp-ipam",
			Usage: "Plugin Name. Useful if you want to run multiple instances of the plugin.",
		},
		cli.StringFlag{
			Name:  "address, addr",
			Value: ":8080",
			Usage: "TCP Address to bind the plugin on.",
		},
	}
	app.Action = Run
	app.Run(os.Args)
}

// Run initializes the driver
func Run(ctx *cli.Context) error {
	if ctx.Bool("debug") {
		log.SetLevel(log.DebugLevel)
	}
	log.SetFormatter(&log.TextFormatter{
		ForceColors:      false,
		DisableColors:    true,
		DisableTimestamp: false,
		FullTimestamp:    true,
	})

	quit := make(chan struct{}) // tells other goroutines to quit
	var wg sync.WaitGroup       // waits for goroutines to quit
	done := make(chan struct{}) // tells main that we're done
	ech := make(chan error)     // catches an error from serveTCP
	var err error               // holds the final return value

	c := make(chan os.Signal)
	defer close(c)
	signal.Notify(c, os.Interrupt)
	signal.Notify(c, syscall.SIGTERM)
	go func() {
		select {
		case _ = <-c:
			log.Debugf("Sigterm caught. Closing")
		case err = <-ech:
			log.Error(err)
		}
		close(quit)
		wg.Wait()
		close(done)
	}()

	d, err := driver.NewDriver(quit, wg)
	if err != nil {
		log.Error("Error initializing driver")
		ech <- err
	}

	h := ipam.NewHandler(d)
	go func() {
		ech <- h.ServeTCP(ctx.String("plugin-name"), ctx.String("address"))
	}()

	<-done
	return nil
}
