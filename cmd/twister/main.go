/*-
 * Copyright © 2017, Jörg Pernfuß <code.jpe@gmail.com>
 * All rights reserved.
 *
 * Use of this source code is governed by a 2-clause BSD license
 * that can be found in the LICENSE file.
 */

package main // import "github.com/mjolnir42/twister/cmd/twister"

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/client9/reopen"
	"github.com/mjolnir42/delay"
	"github.com/mjolnir42/erebos"
	"github.com/mjolnir42/legacy"
	"github.com/mjolnir42/twister/internal/twister"
	metrics "github.com/rcrowley/go-metrics"
)

var githash, shorthash, builddate, buildtime string

func init() {
	// Discard logspam from Zookeeper library
	erebos.DisableZKLogger()

	// set standard logger options
	erebos.SetLogrusOptions()
}

func main() {
	// parse command line flags
	var (
		cliConfPath string
		versionFlag bool
	)
	flag.StringVar(&cliConfPath, `config`, `twister.conf`,
		`Configuration file location`)
	flag.BoolVar(&versionFlag, `version`, false,
		`Print version information`)
	flag.Parse()

	// only provide version information if --version was specified
	if versionFlag {
		fmt.Fprintln(os.Stderr, `Twister Metric Splitter`)
		fmt.Fprintf(os.Stderr, "Version  : %s-%s\n", builddate,
			shorthash)
		fmt.Fprintf(os.Stderr, "Git Hash : %s\n", githash)
		fmt.Fprintf(os.Stderr, "Timestamp: %s\n", buildtime)
		os.Exit(0)
	}

	// read runtime configuration
	conf := erebos.Config{}
	if err := conf.FromFile(cliConfPath); err != nil {
		logrus.Fatalf("Could not open configuration: %s", err)
	}

	// setup logfile
	if lfh, err := reopen.NewFileWriter(
		filepath.Join(conf.Log.Path, conf.Log.File),
	); err != nil {
		logrus.Fatalf("Unable to open logfile: %s", err)
	} else {
		conf.Log.FH = lfh
	}
	logrus.SetOutput(conf.Log.FH)
	logrus.Infoln(`Starting TWISTER...`)

	// signal handler will reopen logfile on USR2 if requested
	if conf.Log.Rotate {
		sigChanLogRotate := make(chan os.Signal, 1)
		signal.Notify(sigChanLogRotate, syscall.SIGUSR2)
		go erebos.Logrotate(sigChanLogRotate, conf)
	}

	// setup signal receiver for graceful shutdown
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	// this channel is used by the handlers on error
	handlerDeath := make(chan error)
	// this channel is used to signal the consumer to stop
	consumerShutdown := make(chan struct{})
	// this channel will be closed by the consumer
	consumerExit := make(chan struct{})

	// setup goroutine waiting policy
	waitdelay := delay.New()

	// setup metrics
	var metricPrefix string
	switch conf.Misc.InstanceName {
	case ``:
		metricPrefix = `/twister`
	default:
		metricPrefix = fmt.Sprintf("/twister/%s",
			conf.Misc.InstanceName)
	}
	pfxRegistry := metrics.NewPrefixedRegistry(metricPrefix)
	metrics.NewRegisteredMeter(`/input/messages.per.second`,
		pfxRegistry)
	metrics.NewRegisteredMeter(`/output/messages.per.second`,
		pfxRegistry)

	ms := legacy.NewMetricSocket(&conf, &pfxRegistry, handlerDeath,
		twister.FormatMetrics)
	ms.SetDebugFormatter(twister.DebugFormatMetrics)
	if conf.Misc.ProduceMetrics {
		logrus.Info(`Launched metrics producer socket`)
		waitdelay.Use()
		go func() {
			defer waitdelay.Done()
			ms.Run()
		}()
	}

	// start application handlers
	for i := 0; i < runtime.NumCPU(); i++ {
		h := twister.Twister{
			Num: i,
			Input: make(chan *erebos.Transport,
				conf.Twister.HandlerQueueLength),
			Shutdown: make(chan struct{}),
			Death:    handlerDeath,
			Config:   &conf,
			Metrics:  &pfxRegistry,
		}
		twister.Handlers[i] = &h
		waitdelay.Use()
		go func() {
			defer waitdelay.Done()
			h.Start()
		}()
		logrus.Infof("Launched Twister handler #%d", i)
	}

	// start kafka consumer
	waitdelay.Use()
	go func() {
		defer waitdelay.Done()
		erebos.Consumer(
			&conf,
			twister.Dispatch,
			consumerShutdown,
			consumerExit,
			handlerDeath,
		)
	}()

	heartbeat := time.Tick(10 * time.Second)

	// the main loop
	fault := false
runloop:
	for {
		select {
		case err := <-ms.Errors:
			logrus.Errorf("Socket error: %s", err.Error())
		case <-c:
			logrus.Infoln(`Received shutdown signal`)
			break runloop
		case err := <-handlerDeath:
			logrus.Errorf("Handler died: %s", err.Error())
			fault = true
			break runloop
		case <-heartbeat:
			for i := range twister.Handlers {
				// do not block on heartbeats
				waitdelay.Use()
				go func(i int) {
					twister.Handlers[i].InputChannel() <- erebos.NewHeartbeat()
					waitdelay.Done()
				}(i)
			}
		}
	}

	// close all handlers
	close(ms.Shutdown)
	close(consumerShutdown)

	// not safe to close InputChannel before consumer is gone
	<-consumerExit
	for i := range twister.Handlers {
		close(twister.Handlers[i].ShutdownChannel())
		close(twister.Handlers[i].InputChannel())
	}

	// read all additional handler errors if required
drainloop:
	for {
		select {
		case err := <-ms.Errors:
			logrus.Errorf("Socket error: %s", err.Error())
		case err := <-handlerDeath:
			logrus.Errorf("Handler died: %s", err.Error())
		case <-time.After(time.Millisecond * 10):
			break drainloop
		}
	}

	// give goroutines that were blocked on handlerDeath channel
	// a chance to exit
	waitdelay.Wait()
	logrus.Infoln(`TWISTER shutdown complete`)
	if fault {
		os.Exit(1)
	}
}

// vim: ts=4 sw=4 sts=4 noet fenc=utf-8 ffs=unix
