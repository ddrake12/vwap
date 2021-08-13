package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/ddrake12/vwap"
	"github.com/ddrake12/vwap/coinbase"
)

const lastTradeIDCtxKey = "lastTradeIDKey"

var totalRetries = 10

func main() {

	logger := log.Default()
	file, err := os.OpenFile("vwap.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening file for log: %v", err)
	}
	logger.SetOutput(file)

	ctx := context.Background()
	wg := &sync.WaitGroup{}

	btcUSDCtx, btcUSDCancel, btcUSDTradeChan, btcUSDLastIDChan := initializeCalcAndReader(ctx, coinbase.IDBTCUSD, wg)

	ethUSDCtx, ethUSDCancel, ethUSDTradeChan, ethUSDLastIDChan := initializeCalcAndReader(ctx, coinbase.IDETHUSD, wg)

	ethBTCCtx, ethBTCCancel, ethBTCTradeChan, ethBTCLastIDChan := initializeCalcAndReader(ctx, coinbase.IDETHBTC, wg)

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	numRetries := 0
	for {
		select {
		case <-interrupt:
			shutdown(wg, btcUSDCancel, ethUSDCancel, ethBTCCancel)
			return

		case <-btcUSDCtx.Done():
			log.Printf("detected unknown shutdown of BTC-USD TradeReader")
			numRetries++
			exitIfLimitReached(numRetries, wg, btcUSDCancel, ethUSDCancel, ethBTCCancel)

			lastTradeID := <-btcUSDLastIDChan
			btcUSDCtx, btcUSDCancel = runCoinbaseTradeReader(ctx, btcUSDTradeChan, btcUSDLastIDChan, coinbase.IDBTCUSD, wg)
			btcUSDLastIDChan <- lastTradeID

		case <-ethUSDCtx.Done():
			log.Printf("detected unknown shutdown of ETH-USD TradeReader")
			numRetries++
			exitIfLimitReached(numRetries, wg, btcUSDCancel, ethUSDCancel, ethBTCCancel)

			lastTradeID := <-ethUSDLastIDChan
			ethUSDCtx, ethUSDCancel = runCoinbaseTradeReader(ctx, ethUSDTradeChan, ethUSDLastIDChan, coinbase.IDETHUSD, wg)
			ethUSDLastIDChan <- lastTradeID

		case <-ethBTCCtx.Done():
			log.Printf("detected unknown shutdown of ETH-BTC TradeReader")
			numRetries++
			exitIfLimitReached(numRetries, wg, btcUSDCancel, ethUSDCancel, ethBTCCancel)

			lastTradeID := <-ethBTCLastIDChan
			ethBTCCtx, ethBTCCancel = runCoinbaseTradeReader(ctx, ethBTCTradeChan, ethBTCLastIDChan, coinbase.IDETHBTC, wg)
			ethBTCLastIDChan <- lastTradeID
		}
	}

}

// initializeCalcAndReader initializes the vwap.Calc and coinbase.TradeReader for the given productID. The local context and cancel for the TradeReader are returned
// for monitoring and shutdown purposes. The tradeChan and lastIDChan are returned for restart purpose.
func initializeCalcAndReader(ctx context.Context, productID string, wg *sync.WaitGroup) (localCtx context.Context, cancel context.CancelFunc, tradeChan chan *vwap.Trade, lastIDChan chan int) {
	tradeChan = make(chan *vwap.Trade)
	runVWAPCalc(tradeChan, productID)
	lastIDChan = make(chan int, 1)
	localCtx, cancel = runCoinbaseTradeReader(ctx, tradeChan, lastIDChan, coinbase.IDBTCUSD, wg)
	lastIDChan <- 0

	return localCtx, cancel, tradeChan, lastIDChan
}

// exitIfLimitReached exits with an OS status code 1 if the totalRetries limit has been reached. It does shutdown other goroutines properly.
func exitIfLimitReached(numRetries int, wg *sync.WaitGroup, btcUSDCancel, ethUSDCancel, ethBTCCancel context.CancelFunc) {
	if numRetries > totalRetries {
		log.Print("FATAL, too many restarts of TradeReader services, investigate")
		shutdown(wg, btcUSDCancel, ethUSDCancel, ethBTCCancel)
		os.Exit(1)
	}

}

// shutdown calls all cancels and waits for their goroutines to return, timing out if they do not.
func shutdown(wg *sync.WaitGroup, btcUSDCancel, ethUSDCancel, ethBTCCancel context.CancelFunc) {

	btcUSDCancel()
	ethUSDCancel()
	ethBTCCancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		log.Printf("timed out waiting for shutdown")
	}
}

// runVWAPCalc starts up a vwap.Calc instance for the given productID that will receive trades and update the VWAP.
// The same tradeChan should be given to this and the corresponding runCoinbaseTradeReader for the same productID
func runVWAPCalc(tradeChan chan *vwap.Trade, productID string) {
	calc := vwap.NewCalc(tradeChan, productID)

	go calc.UpdateVWAP()
	go calc.ReceiveTrades()

}

// runCoinbaseTradeReader runs a TradeReader for the given productID. It runs in its own goroutine and returns its local context.Context
// and context.CancelFunc for monitoring and shutdown purposes. Upon exiting it will publish the LastTradeID (or 0 if none) to the lastTradeIDChan.
// The same tradeChan should be given to this and the corresponding runVWAPCalc for the same productID
func runCoinbaseTradeReader(ctx context.Context, tradeChan chan *vwap.Trade, lastTradeIDChan chan int, productID string, wg *sync.WaitGroup) (localCtx context.Context, cancel context.CancelFunc) {
	localCtx, cancel = context.WithCancel(ctx)

	wg.Add(1)

	go func() {
		defer cancel()
		defer wg.Done()

		lastTradeID := <-lastTradeIDChan
		tradeReader, err := coinbase.NewTradeReader(tradeChan, productID, lastTradeID)
		defer func() {
			if tradeReader != nil {
				lastTradeIDChan <- tradeReader.LastTradeID
			} else {
				lastTradeIDChan <- 0
			}
		}()
		if err != nil {
			log.Printf("could not initialize trade reader: %v", err)
			return
		}
		defer tradeReader.CloseConn()

		go tradeReader.ReadConn()

		tradeReader.Subscribe()
		tradeReader.WaitForEnd(localCtx)

	}()
	return localCtx, cancel
}
