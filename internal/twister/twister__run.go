/*-
 * Copyright © 2017, Jörg Pernfuß <code.jpe@gmail.com>
 * All rights reserved.
 *
 * Use of this source code is governed by a 2-clause BSD license
 * that can be found in the LICENSE file.
 */

package twister // import "github.com/mjolnir42/twister/internal/twister"

import (
	"github.com/Sirupsen/logrus"
	metrics "github.com/rcrowley/go-metrics"
)

// run is the event loop for Twister
func (t *Twister) run() {
	in := metrics.GetOrRegisterMeter(
		`/input/messages.per.second`,
		*t.Metrics,
	)
	out := metrics.GetOrRegisterMeter(
		`/output/messages.per.second`,
		*t.Metrics,
	)

	// required during shutdown
	inputEmpty := false
	errorEmpty := false
	successEmpty := false
	producerClosed := false

runloop:
	for {
		select {
		case <-t.Shutdown:
			// received shutdown, drain input channel which will be
			// closed by main
			goto drainloop
		case err := <-t.producer.Errors():
			t.Death <- err
			<-t.Shutdown
			break runloop
		case msg := <-t.producer.Successes():
			trackingID := msg.Metadata.(string)
			t.updateOffset(trackingID)
			out.Mark(1)
		case msg := <-t.Input:
			if msg == nil {
				// this can happen if we read the closed Input channel
				// before the closed Shutdown channel
				continue runloop
			}
			t.process(msg)
			in.Mark(1)
		}
	}
	// shutdown due to producer error
	t.producer.Close()
	return

drainloop:
	for {
		select {
		case msg := <-t.Input:
			if msg == nil {
				inputEmpty = true

				if !producerClosed {
					t.producer.Close()
					producerClosed = true
				}

				// channels are closed
				if inputEmpty && errorEmpty && successEmpty {
					break drainloop
				}
				continue drainloop
			}
			t.process(msg)
		case e := <-t.producer.Errors():
			if e == nil {
				errorEmpty = true

				// channels are closed
				if inputEmpty && errorEmpty && successEmpty {
					break drainloop
				}
				continue drainloop
			}
			logrus.Errorln(e)
		case msg := <-t.producer.Successes():
			if msg == nil {
				successEmpty = true

				// channels are closed
				if inputEmpty && errorEmpty && successEmpty {
					break drainloop
				}
				continue drainloop
			}
			trackingID := msg.Metadata.(string)
			t.updateOffset(trackingID)
			out.Mark(1)
		}
	}
	t.delay.Wait()
}

// vim: ts=4 sw=4 sts=4 noet fenc=utf-8 ffs=unix
