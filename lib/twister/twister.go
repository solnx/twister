/*-
 * Copyright © 2017, Jörg Pernfuß <code.jpe@gmail.com>
 * All rights reserved.
 *
 * Use of this source code is governed by a 2-clause BSD license
 * that can be found in the LICENSE file.
 */

package twister // import "github.com/mjolnir42/twister/lib/twister"

import (
	"encoding/json"
	"strconv"

	"github.com/Shopify/sarama"
	"github.com/Sirupsen/logrus"
	"github.com/mjolnir42/erebos"
	"github.com/mjolnir42/legacy"
	metrics "github.com/rcrowley/go-metrics"
)

// Handlers is the registry of running application handlers
var Handlers map[int]erebos.Handler

func init() {
	Handlers = make(map[int]erebos.Handler)
}

// Twister splits up read metric batches and produces the result
type Twister struct {
	Num      int
	Input    chan *erebos.Transport
	Shutdown chan struct{}
	Death    chan error
	Config   *erebos.Config
	dispatch chan<- *sarama.ProducerMessage
	producer sarama.AsyncProducer
	Metrics  *metrics.Registry
}

// run is the event loop for Twister
func (t *Twister) run() {
	in := metrics.GetOrRegisterMeter(`/input/messages.per.second`, *t.Metrics)

	// required during shutdown
	inputEmpty := false
	errorEmpty := false
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
				// channel is closed
				inputEmpty = true

				if !producerClosed {
					t.producer.Close()
					producerClosed = true
				}
				if inputEmpty && errorEmpty {
					break drainloop
				}
				continue drainloop
			}
			t.process(msg)
		case e := <-t.producer.Errors():
			if e == nil {
				errorEmpty = true

				// channel is closed
				if inputEmpty && errorEmpty {
					break drainloop
				}
			}
		}
	}
}

// process is the handler for converting a MetricBatch
// and producing the result. Invalid data is marked as processed
// and skipped.
func (t *Twister) process(msg *erebos.Transport) {
	if msg == nil || msg.Value == nil {
		logrus.Warnf("Ignoring empty message from: %d", msg.HostID)
		if msg != nil {
			go t.commit(msg)
		}
		return
	}
	out := metrics.GetOrRegisterMeter(`/output/messages.per.second`, *t.Metrics)
	batch := legacy.MetricBatch{}
	if err := json.Unmarshal(msg.Value, &batch); err != nil {
		logrus.Warnf("Ignoring invalid data: %s", err.Error())
		go t.commit(msg)
		return
	}

	msgs := batch.Split()
	for i := range msgs {
		data, err := json.Marshal(&msgs[i])
		if err != nil {
			logrus.Warnf("Ignoring invalid data: %s", err.Error())
			go t.commit(msg)
			return
		}

		t.dispatch <- &sarama.ProducerMessage{
			Topic: t.Config.Kafka.ProducerTopic,
			Key: sarama.StringEncoder(
				strconv.Itoa(int(msgs[i].AssetID)),
			),
			Value: sarama.ByteEncoder(data),
		}
		out.Mark(1)
		go t.commit(msg)
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
