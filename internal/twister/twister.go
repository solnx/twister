/*-
 * Copyright © 2017, Jörg Pernfuß <code.jpe@gmail.com>
 * All rights reserved.
 *
 * Use of this source code is governed by a 2-clause BSD license
 * that can be found in the LICENSE file.
 */

// Package twister splits metric batches into individual metrics
package twister // import "github.com/mjolnir42/twister/internal/twister"

import (
	"github.com/Shopify/sarama"
	"github.com/Sirupsen/logrus"
	"github.com/mjolnir42/delay"
	"github.com/mjolnir42/erebos"
	metrics "github.com/rcrowley/go-metrics"
)

// Handlers is the registry of running application handlers
var Handlers map[int]erebos.Handler

// init function sets up package variables
func init() {
	// Handlers tracks all Twister instances and is used by
	// Dispatch() to find the correct instance to route the message to
	Handlers = make(map[int]erebos.Handler)
}

// Twister splits up read metric batches and produces the result
type Twister struct {
	Num      int
	Input    chan *erebos.Transport
	Shutdown chan struct{}
	Death    chan error
	Config   *erebos.Config
	Metrics  *metrics.Registry
	delay    *delay.Delay
	trackID  map[string]int
	trackACK map[string][]*erebos.Transport
	dispatch chan<- *sarama.ProducerMessage
	producer sarama.AsyncProducer
}

// updateOffset updates the consumer offsets in Kafka once all
// outstanding messages for trackingID have been processed
func (t *Twister) updateOffset(trackingID string) {
	if _, ok := t.trackID[trackingID]; !ok {
		logrus.Warnf("Unknown trackingID: %s", trackingID)
		return
	}
	// decrement outstanding successes for trackingID
	t.trackID[trackingID]--
	// check if trackingID has been fully processed
	if t.trackID[trackingID] == 0 {
		// commit processed offsets to Zookeeper
		acks := t.trackACK[trackingID]
		for i := range acks {
			t.delay.Use()
			go func(idx int) {
				t.commit(acks[idx])
				t.delay.Done()
			}(i)
		}
		// cleanup offset tracking
		delete(t.trackID, trackingID)
		delete(t.trackACK, trackingID)
	}
}

// commit marks a message as fully processed
func (t *Twister) commit(msg *erebos.Transport) {
	msg.Commit <- &erebos.Commit{
		Topic:     msg.Topic,
		Partition: msg.Partition,
		Offset:    msg.Offset,
	}
}

// vim: ts=4 sw=4 sts=4 noet fenc=utf-8 ffs=unix
