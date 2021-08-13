package vwap

import (
	"bufio"
	"fmt"
	"log"
	"math/big"
	"math/rand"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

var (
	testPair     = "testPair"
	testPrice    = NewBigFloat(23.45)
	testQuantity = NewBigFloat(1.2)
	testTrade    = &Trade{
		Pair:     testPair,
		Price:    testPrice,
		Quantity: testQuantity,
	}
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

// newTestCalc initializes a new Calc instance for testing purposes and returns a non buffered channel to allow synchronous testing.
func newTestCalc() (*Calc, chan *Trade) {
	tradeChan := make(chan *Trade) // it is easier to test if this is non buffered (particularly verifying the update VWAP function  by reading the log buffer)
	return NewCalc(tradeChan, testPair), tradeChan
}

// mockLogger sets the log output to the returned scanner that can be used to block (with scanner.Scan()) until logs are written and verify content.
// The returned file descriptions should be immediately passed into defer resetLogger() to properly cleanup.
// Be careful to turn this off when debugging or developing tests as you will miss output!
func mockLogger(t *testing.T) (*bufio.Scanner, *os.File, *os.File) {
	reader, writer, err := os.Pipe()
	if err != nil {
		assert.Fail(t, "couldn't get os Pipe: %v", err)
	}
	log.SetOutput(writer)

	return bufio.NewScanner(reader), reader, writer
}

// resetLogger should be called in defer immediately after mockLogger and passed the returned readers and writers. It prints any close errors to StdOut
func resetLogger(reader *os.File, writer *os.File) {
	err := reader.Close()
	if err != nil {
		fmt.Println("error closing reader was ", err)
	}
	if err = writer.Close(); err != nil {
		fmt.Println("error closing writer was ", err)
	}
	log.SetOutput(os.Stderr)
}

func TestNewCalc(t *testing.T) {
	tests := []struct {
		name string
	}{
		{"test valid constructor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tradeChan := make(<-chan *Trade)
			got := NewCalc(tradeChan, testPair)
			assert.Equal(t, tradeChan, got.tradeChan)
			assert.Equal(t, testPair, got.pair)
			assert.NotNil(t, got.logger)
			assert.NotNil(t, got.queueChan)
			assert.Equal(t, newVWAPQueue(), got.queue)
		})
	}
}

func TestCalc_ReceiveTrades(t *testing.T) {

	tests := []struct {
		name    string
		pairErr bool
		nilErr  bool
	}{
		{"test correct pair sent", false, false},
		{"test incorrect pair sent", true, false},
		{"test nil quantity/price", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scanner, reader, writer := mockLogger(t)
			defer resetLogger(reader, writer)

			vc, tradeChan := newTestCalc()
			go vc.ReceiveTrades()

			if tt.pairErr {
				tradeChan <- &Trade{Pair: "other"}
				scanner.Scan()
				assert.Contains(t, scanner.Text(), "error, incorrect pair on tradeChan for this Calc instance")
			} else if tt.nilErr {
				tradeChan <- &Trade{Pair: testPair}
				scanner.Scan()
				msg := fmt.Sprintf("error, math.big representations of price or quantity for vc.Pair %s were nil! These have to be set before sending.", testPair)
				assert.Contains(t, scanner.Text(), msg)
			} else {
				tradeChan <- testTrade
				trade := <-vc.queueChan

				assert.Equal(t, testTrade, trade)
			}
		})
	}
}

// This is an important test, it tests the entire pipeline and independently verifies that the calculation is accurate and thus also tests that
// the underlying queue implementation works properly.
func TestCalc_UpdateVWAP(t *testing.T) {

	tests := []struct {
		name string
	}{
		{"test proper vwap calculation"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scanner, reader, writer := mockLogger(t)
			defer resetLogger(reader, writer)

			vc, tradeChan := newTestCalc()
			go vc.ReceiveTrades()
			go vc.UpdateVWAP()

			length := VWAPQueueLen + 100
			var prices []*big.Float
			var quantities []*big.Float

			for i := 0; i < length; i++ {

				prices, quantities = sendRandTrade(tradeChan, prices, quantities)

				scanner.Scan()
				got := scanner.Text()
				if err := scanner.Err(); err != nil {
					assert.FailNow(t, "error reading pipe: ", err)
				}

				testVWAP := calculateTestVWAP(prices, quantities)
				vwap := NewEmptyBigFloat()
				vwap.Quo(vc.sumPriceAndQuantity, vc.sumQuantity)

				assert.Equal(t, testVWAP, vwap)
				verifySentVWAP(t, got, testVWAP)
			}

		})
	}
}

func sendRandTrade(tradeChan chan *Trade, prices, quantities []*big.Float) ([]*big.Float, []*big.Float) {

	// TODO: discuss in code review, should we update the range here?
	max := 1000000000.0
	min := 0.0

	priceFloat := min + rand.Float64()*(max-min)
	quantityFloat := min + rand.Float64()*(max-min)
	price := NewBigFloat(priceFloat)
	quantity := NewBigFloat(quantityFloat)
	prices = append(prices, price)
	quantities = append(quantities, quantity)

	tradeChan <- &Trade{Pair: testPair, Price: price, Quantity: quantity}

	return prices, quantities
}

func calculateTestVWAP(prices, quantities []*big.Float) (testVWAP *big.Float) {
	sumPriceAndQuantity := NewEmptyBigFloat()
	sumQuantity := NewEmptyBigFloat()
	count := 0
	for j := len(prices) - 1; j >= 0; j-- {
		count++
		if count > VWAPQueueLen {
			break
		}
		priceTimesQuantity := NewEmptyBigFloat()
		priceTimesQuantity.Mul(prices[j], quantities[j])
		sumPriceAndQuantity.Add(sumPriceAndQuantity, priceTimesQuantity)

		sumQuantity.Add(sumQuantity, quantities[j])
	}

	testVWAP = NewEmptyBigFloat()
	testVWAP.Quo(sumPriceAndQuantity, sumQuantity)
	return testVWAP
}

func verifySentVWAP(t *testing.T, got string, testVWAP *big.Float) {
	vwapRe := regexp.MustCompile(`VWAP for pair\s+\S+\s+is (\d*\.\d*e\+\d+)\s*$`)
	matches := vwapRe.FindStringSubmatch(got)
	if matches == nil {
		assert.FailNow(t, fmt.Sprintf("could not match regex for VWAP number, update test. Got was: %s", got))
	}
	vwapStr := matches[1]
	vwapSent := NewEmptyBigFloat()
	vwapSent.Parse(vwapStr, 10)
	calcString := fmt.Sprintf("%v", testVWAP)
	assert.Equal(t, calcString, vwapStr)
}

func Test_newVWAPQueue(t *testing.T) {
	tests := []struct {
		name string
	}{
		{"test valid constructor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newVWAPQueue()
			assert.NotNil(t, got.queue)
			assert.Zero(t, got.curIndex)
			assert.Equal(t, VWAPQueueLen, got.maxLen)
		})
	}
}

func Test_vwapQueue_AddNew(t *testing.T) {

	tests := []struct {
		name string
	}{
		// TODO: Add test cases, but implementation tested by TestCalc_UpdateVWAP so good for now
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

		})
	}
}

func TestParseBigFloat(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"test valid float", false},
		{"test invalid float", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			if tt.wantErr {
				got, err := ParseBigFloat("notAFloat")
				assert.Error(t, err)
				assert.Nil(t, got)
			} else {
				got, err := ParseBigFloat(testPrice.String())
				assert.NoError(t, err)
				assert.NotNil(t, got)
			}
		})
	}
}
