/*-
 * Copyright © 2017, Jörg Pernfuß <code.jpe@gmail.com>
 * All rights reserved.
 *
 * Use of this source code is governed by a 2-clause BSD license
 * that can be found in the LICENSE file.
 */

package twister

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/Shopify/sarama"
	"github.com/mjolnir42/erebos"
	"github.com/mjolnir42/legacy"
	kazoo "github.com/wvanbergen/kazoo-go"
)

// Twister splits up read metric batches and produces the result
type Twister struct {
	Num      int
	Input    chan []byte
	Shutdown chan struct{}
	Death    chan struct{}
	Config   *erebos.Config
	dispatch chan<- *sarama.ProducerMessage
	producer sarama.AsyncProducer
}

// Start sets up the Twister application
func (t *Twister) Start() {
	kz, err := kazoo.NewKazooFromConnectionString(
		t.Config.Zookeeper.Connect, nil)
	if err != nil {
		close(t.Death)
		<-t.Shutdown
		return
	}
	brokers, err := kz.BrokerList()
	if err != nil {
		kz.Close()
		close(t.Death)
		<-t.Shutdown
		return
	}
	kz.Close()

	host, err := os.Hostname()
	if err != nil {
		close(t.Death)
		<-t.Shutdown
		return
	}

	config := sarama.NewConfig()
	config.Net.KeepAlive = 5 * time.Second
	config.Producer.RequiredAcks = sarama.NoResponse
	config.Producer.Retry.Max = 5
	config.Producer.Partitioner = sarama.NewHashPartitioner
	config.ClientID = fmt.Sprintf("twister.%s", host)

	t.producer, err = sarama.NewAsyncProducer(brokers, config)
	if err != nil {
		close(t.Death)
		<-t.Shutdown
		return
	}
	t.dispatch = t.producer.Input()
	t.run()
}

// run is the event loop for Twister
func (t *Twister) run() {
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
		case <-t.producer.Errors():
			close(t.Death)
			<-t.Shutdown
			break runloop
		case msg := <-t.Input:
			if msg == nil {
				// this can happen if we read the closed Input channel
				// before the closed Shutdown channel
				continue runloop
			}
			t.process(msg)
		}
	}
	// shutdown due to producer error
	producerClosed = true
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
// and producing the result
func (t *Twister) process(msg []byte) {
	batch := legacy.MetricBatch{}
	if err := json.Unmarshal(msg, &batch); err != nil {
		close(t.Death)
		<-t.Shutdown
		return
	}

	msgs := batch.Split()
	for i := range msgs {
		data, err := json.Marshal(&msgs[i])
		if err != nil {
			close(t.Death)
			return
		}

		t.dispatch <- &sarama.ProducerMessage{
			Topic: t.Config.Kafka.ProducerTopic,
			Key: sarama.StringEncoder(
				strconv.Itoa(int(msgs[i].AssetID)),
			),
			Value: sarama.ByteEncoder(data),
		}
	}
}

// vim: ts=4 sw=4 sts=4 noet fenc=utf-8 ffs=unix
