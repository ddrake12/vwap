package vwap

import (
	"fmt"
	"log"
	"math/big"
)

// ChanBuffLen is used to set the channel buffer length for faster communication between the ReceiveTrades() and UpdateVWAP() goroutines
var ChanBuffLen = 1000

// VWAPQueueLen sets the number of data points used to calculate the VWAP.
var VWAPQueueLen = 200

// floatPrec is the precision used by the NewBigFloat, NewEmptyBigFloat, and ParseBigFloat canonical methods and ensures accurate calculations.
var floatPrec = uint(128)

// Trade contains the information needed to compute a VWAP for a given pair
type Trade struct {
	Pair     string
	Price    *big.Float
	Quantity *big.Float
}

// NewEmptyBigFloat returns an empty *big.Float using the canonical vwap representation. Useful for performing arithmetic on two *big.Float objects.
func NewEmptyBigFloat() *big.Float {
	float := new(big.Float)
	float.SetPrec(floatPrec)

	return float
}

// NewBigFloat takes a float64 and returns the canonical vwap float representation
func NewBigFloat(num float64) *big.Float {
	float := new(big.Float)
	float.SetPrec(floatPrec)
	float.SetFloat64(num)
	return float
}

// ParseBigFloat parses the string and returns the canonical vwap float representation, returning an error if it couldn't be parsed
func ParseBigFloat(str string) (*big.Float, error) {
	float := NewEmptyBigFloat()
	float, _, err := float.Parse(str, 10)
	if err != nil {
		return nil, fmt.Errorf("could not parse float for string: %s, error: %v", str, err)
	}
	return float, nil
}

// Calc is used to compute the VWAP for a given pair. Calc will consider the VWAP for trades on the tradeChan in the order received.
// Any ordering should be done on the client side.
type Calc struct {
	tradeChan           <-chan *Trade
	pair                string
	logger              *log.Logger //TODO: implement a real logging framework
	queueChan           chan *Trade
	queue               *vwapQueue
	sumPriceAndQuantity *big.Float
	sumQuantity         *big.Float
}

// vwapQueue is a simple, fast queue that is a circular fifo queue up to maxLen
type vwapQueue struct {
	queue    []*Trade
	curIndex int
	maxLen   int
}

// NewCalc initializes all internal fields needed to make the VWAP calc. Pass in a buffered *Trade channel for faster processing.
func NewCalc(tradeChan <-chan *Trade, pair string) *Calc {

	queueChan := make(chan *Trade, ChanBuffLen)

	logger := log.Default()

	return &Calc{
		tradeChan:           tradeChan,
		pair:                pair,
		logger:              logger,
		queueChan:           queueChan,
		queue:               newVWAPQueue(),
		sumPriceAndQuantity: NewEmptyBigFloat(),
		sumQuantity:         NewEmptyBigFloat(),
	}
}

// ReceiveTrades receives trades from the tradeChan and passes them to the queueChan. It also checks that the type of pair is
// correct and logs an error if not. It should be ran in a separate goroutine.
func (vc *Calc) ReceiveTrades() {
	for {
		select {
		case trade := <-vc.tradeChan:
			if trade.Pair != vc.pair {
				vc.logger.Printf("error, incorrect pair on tradeChan for this Calc instance. vc.pair: %s , Trade.pair: %s", vc.pair, trade.Pair)
				continue
			} else if trade.Price == nil || trade.Quantity == nil {
				vc.logger.Printf("error, math.big representations of price or quantity for vc.Pair %s were nil! These have to be set before sending.", vc.pair)
				continue
			}

			vc.queueChan <- trade
		}
	}
}

// UpdateVWAP receives trades from the queueChan, updates the queue, and calculates the new VWAP.
// It should be ran in a separate goroutine
func (vc *Calc) UpdateVWAP() {
	for {
		select {
		case trade := <-vc.queueChan:
			oldPrice, oldQuantity := vc.queue.AddNew(trade)

			mulNewPriceAndQuantity := NewEmptyBigFloat()
			mulNewPriceAndQuantity.Mul(trade.Price, trade.Quantity)

			if oldPrice != nil && oldQuantity != nil {
				mulOldPriceAndQuantity := NewEmptyBigFloat()
				mulOldPriceAndQuantity.Mul(oldPrice, oldQuantity)

				vc.sumPriceAndQuantity.Sub(vc.sumPriceAndQuantity, mulOldPriceAndQuantity)
				vc.sumPriceAndQuantity.Add(vc.sumPriceAndQuantity, mulNewPriceAndQuantity)

				vc.sumQuantity.Sub(vc.sumQuantity, oldQuantity).Add(vc.sumQuantity, trade.Quantity)

			} else {
				vc.sumPriceAndQuantity.Add(vc.sumPriceAndQuantity, mulNewPriceAndQuantity)
				vc.sumQuantity.Add(vc.sumQuantity, trade.Quantity)
			}

			vwap := NewEmptyBigFloat()
			vwap.Quo(vc.sumPriceAndQuantity, vc.sumQuantity)

			vc.logger.Printf("VWAP for pair %s is %v", vc.pair, vwap)

		}
	}
}

// newVWAPQueue initializes a new vwapQueue and sets the maxLen according to the VWAPQueueLen package variable.
func newVWAPQueue() *vwapQueue {
	queue := make([]*Trade, VWAPQueueLen)
	return &vwapQueue{
		queue:    queue,
		curIndex: 0,
		maxLen:   VWAPQueueLen,
	}
}

// AddNew adds a new trade into the queue and returns the oldPrice and oldQuantity that was pushed out of the queue or 0 if none
func (tq *vwapQueue) AddNew(trade *Trade) (oldPrice, oldQuantity *big.Float) {
	if tq.curIndex == tq.maxLen {
		tq.curIndex = 0
	}

	old := tq.queue[tq.curIndex]
	if old != nil {
		oldPrice, oldQuantity = old.Price, old.Quantity
	}

	tq.queue[tq.curIndex] = trade
	tq.curIndex++

	return oldPrice, oldQuantity
}
