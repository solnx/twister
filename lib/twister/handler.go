/*-
 * Copyright © 2017, Jörg Pernfuß <code.jpe@gmail.com>
 * All rights reserved.
 *
 * Use of this source code is governed by a 2-clause BSD license
 * that can be found in the LICENSE file.
 */

package twister // import "github.com/mjolnir42/twister/lib/twister"

import (
	"fmt"
	"os"
	"time"

	"github.com/Shopify/sarama"
	"github.com/mjolnir42/erebos"
	kazoo "github.com/wvanbergen/kazoo-go"
)

// Implementation of the erebos.Handler interface

// Start sets up the Twister application
func (t *Twister) Start() {
	kz, err := kazoo.NewKazooFromConnectionString(
		t.Config.Zookeeper.Connect, nil)
	if err != nil {
		t.Death <- err
		<-t.Shutdown
		return
	}
	brokers, err := kz.BrokerList()
	if err != nil {
		kz.Close()
		t.Death <- err
		<-t.Shutdown
		return
	}
	kz.Close()

	host, err := os.Hostname()
	if err != nil {
		t.Death <- err
		<-t.Shutdown
		return
	}

	config := sarama.NewConfig()
	// set transport keepalive
	switch t.Config.Kafka.Keepalive {
	case 0:
		config.Net.KeepAlive = 3 * time.Second
	default:
		config.Net.KeepAlive = time.Duration(
			t.Config.Kafka.Keepalive,
		) * time.Millisecond
	}
	// set our required persistence confidence for producing
	switch t.Config.Kafka.ProducerResponseStrategy {
	case `NoResponse`:
		config.Producer.RequiredAcks = sarama.NoResponse
	case `WaitForLocal`:
		config.Producer.RequiredAcks = sarama.WaitForLocal
	case `WaitForAll`:
		config.Producer.RequiredAcks = sarama.WaitForAll
	default:
		config.Producer.RequiredAcks = sarama.WaitForLocal
	}
	// set how often to retry producing
	switch t.Config.Kafka.ProducerRetry {
	case 0:
		config.Producer.Retry.Max = 3
	default:
		config.Producer.Retry.Max = t.Config.Kafka.ProducerRetry
	}
	config.Producer.Partitioner = sarama.NewHashPartitioner
	config.ClientID = fmt.Sprintf("twister.%s", host)

	t.producer, err = sarama.NewAsyncProducer(brokers, config)
	if err != nil {
		t.Death <- err
		<-t.Shutdown
		return
	}
	t.dispatch = t.producer.Input()
	t.run()
}

// InputChannel returns the data input channel
func (t *Twister) InputChannel() chan *erebos.Transport {
	return t.Input
}

// ShutdownChannel returns the shutdown signal channel
func (t *Twister) ShutdownChannel() chan struct{} {
	return t.Shutdown
}

// vim: ts=4 sw=4 sts=4 noet fenc=utf-8 ffs=unix
