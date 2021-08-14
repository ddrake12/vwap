[![Go Reference](https://pkg.go.dev/badge/github.com/ddrake12/vwap.svg)](https://pkg.go.dev/github.com/ddrake12/vwap) ![Build Status](https://github.com/ddrake12/vwap/actions/workflows/go.yml/badge.svg) [![Go Report Card](https://goreportcard.com/badge/github.com/ddrake12/vwap)](https://goreportcard.com/report/github.com/ddrake12/vwap)

Package `vwap` is a package built to calculate the [volume-weighted average price]( https://en.wikipedia.org/wiki/Volume-weighted_average_price) for different trading pairs. It currently supports pulling data from [coinbase](https://docs.pro.coinbase.com/#introduction) for 3 different pairs:

 1. BTC-USD
 2. ETH-USD
 3. ETH-BTC

The implementation for pulling data from coinbase is separated in the `coinbase` sub-package. `vwap` is designed to allow further expansion and addition of new exchanges/data sources as needed using the modular design detailed below.

# Design Overview

Each trading pair should have its' own `vwap.Calc` object, initialized with a `Pair` for incorrect use detection and a `TradeChan` for receiving `Trade` objects from data gatherers like `coinbase.TradeReader`.  **`vwap.Calc` is agnostic and has no logic to determine order. The order of data used for a VWAP is the order data is received on a `TradeChan` for a given `vwap.Calc` instance, and order must be handled upstream.** The current implementation uses `200` as the default number of data points, but this is configurable with the package variable `vwap.VWAPQueueLen` . 

*Note: that the first VWAP calculations (up to `vwap.VWAPQueueLen`) do not use the full number of data points.*

Each implementation of a data gatherer is responsible for sending correct, in order data to the single `vwap.Calc` object that computes the VWAP. **Multiple data gatherers for different data sources should send all data to the single `vwap.Calc` instance responsible for that trading pair.** For the `coinbase.TradeReader` implementation, *each trading pair should have its own `coinbase.TradeReader` object*. 

So for our 3 currently supported trading pairs, the implementation looks like this at a high level:

 1. BTC-USD `coinbase.TradeReader`object sends `vwap.Trade` ---> to BTC-USD `vwap.Calc` object
 2. ETH-USD `coinbase.TradeReader`object sends `vwap.Trade` ---> to ETH-USD `vwap.Calc` object
 3. ETH-BTC `coinbase.TradeReader`object sends `vwap.Trade` ---> to ETH-BTC `vwap.Calc` object

## `coinbase.TradeReader` Design

Each `coinbase.TradeReader` object is initialized for 1 trading pair. There should only be one per trading pair, and multiple objects may result in duplicate data used in the VWAP. The `TradeReader` connects to the [coinbase websocket](https://docs.pro.coinbase.com/#websocket-feed) and subscribes for its' trading pair.  The object reads each message from the connection, checks for valid data, and then sends it using the `TradeChan` for the corresponding `vwap.Calc`. It keeps track of the `TradeID` for each message and if there is a non-consecutive trade ID then it attempts to fill in the missing data using the coinbase REST API on `GET /products/{productID}/trades`. *A few notes about this:*

 1. The coinbase docs recommend subscribing the `heartbeat` channel and watching heartbeats for missed trades on the `matches` channel. This design does not do that intentionally and for good reason. There is no need to clutter the data feed with heartbeat messages, it is easy to determine if any trades were missed by keeping track of the `lastTradeID`. This design performs better because no time is wasted on messages that don't contain price information. In testing, there were very few dropped messages and heartbeats would have been unnecessary clutter that would have also added complexity in tracking. 
 2. There is a retry limit for getting the missing data that can be configured using the `coinbase.GETMissingTradesRetries` package variable. The default is 3 retries. 
 3. If the missing trades can not be found, the `TradeReader` will continue sending the latest data from the websocket until another out of order TradeID is found. 
 4. Currently, the `GET` only retrieves the first/latest page of `trades` from the REST API and assumes that the missing trades can be quickly found. This worked in overnight testing. 

The `TradeReader` will continue to run until an error is encountered on the websocket or its' `context` is cancelled upstream. At this point the `ReadConn` method will return causing the `WaitForEnd` method to exit. 

### Using a`coinbase.TradeReader`

Follow these steps to run a `coinbase.TradeReader`. For an example see `cmd/main.go`

Run these steps in a separate goroutine and create a local `context` and `context.CancelFunc` to monitor and shutdown the `TradeReader`. 

 1. Initialize a `TradeReader` with a `tradeChan` (the same one used for the corresponding `vwap.Calc`, `productID`, and an optional `lastTradeID`. The `lastTradeID` can be used to fill in missing trades using the methodology described above if the `TradeReader` encountered a shutdown. 
 2. Start `TradeReader.ReadConn()` in a new goroutine to begin listening to the websocket. 
 3. Run `TradeReader.Subscribe()` to subscribe to the [`matches` channel](https://docs.pro.coinbase.com/#the-matches-channel) for the `TradeReader`'s productID. 
 4. Run `TradeReader.WaitForEnd()` and pass in a local context that can be used to cancel/monitor. This will block until an error is encountered on the websocket or until the `context` is cancelled.  

### `cmd/main.go`
The main entry point into the program is `cmd/main.go` this will run the `TradeReaders` and calculate the VWAP for the 3 supported trading pairs. It is fault tolerant and supports up to `totalRetries` (default 10) number of errors received by all the `TradeReaders` combined. It will restart a `TradeReader` if it fails and attempt to fill in all missing data from the last seen `TradeID`. It also has clean shutdown and can receive an `interrupt` signal and cleanly close the websocket. 

## Final Design Notes
Because the nature of price data is time sensitive and we do not want to miss queue any big spike events in price, a time ordered approach was taken for this project. For each trading pair the data is resolved from coinbase synchronously (although different pairs are resolved concurrently) and sent in order to the `vwap.Calc`. It is conceivable to speed up things if we don't care about order or duplicate data but this would give an inaccurate result. In this way, accuracy is preferred over pure performance. In testing, the performance seems to work well. 

# Installation
`go get -u github.com/ddrake12/vwap`

# Running 
Navigate to the `vwap/cmd` folder and run any of the following commands:

 - `go run main.go` to run it in the current terminal.
 - `go build main.go`  to create a binary in the current directory.
 - `go install main.go` to create a binary at the appropriate `bin` folder. See this [reference](https://pkg.go.dev/cmd/go#hdr-Compile_and_install_packages_and_dependencies_) for details. 

## Testing

All major code is unit/integration tested for both the `vwap` and `coinbase` packages. Just navigate to the base folder and run `go test ./... -v`.   

### Future Testing and Improvement
With more time, I would love to write proper integration tests for the `cmd/main.go` file. We could change the websocket URL to be a test one that we create, and then publish our own data to simulate different error conditions. We could also leave it running for a long period of times and inspect and investigate logs for any errors encountered (I did run it overnight and inspect logs). There are also a few unit tests I couldn't get to, but all this code was actually tested using the entire pipeline with integration type tests. See the `TestTradeReader_ReadConn` function in the `reader_test.go`  and the `TestCalc_UpdateVWAP` function in the `calc_test.go` for the more interesting pipeline tests. 

Other improvements would be adding more data sources, and testing some different implementations for performance. For example, I arbitrarily set the buffered channel length `ChanBuffLen` in `vwap` to 1000, but this could possibly be tuned for performance. 

# Enjoy!