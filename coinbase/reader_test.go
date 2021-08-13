package coinbase

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ddrake12/vwap"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
)

var (
	testProductID            = "testProductID"
	testPrice                = "1.1"
	testSize                 = "2.2"
	testLastTradeID          = 42
	testInOrderTradeID       = 43
	testOutOfOrderTradeID    = 45
	testFirstMissingTradeID  = 43
	testSecondMissingTradeID = 44
	testFirstMissingPrice    = "42.42"
	testFirstMissingSize     = ".1234"
	testSecondMissingPrice   = "41.41"
	testSecondMissingSize    = "9876.5432"
)

func setUpTestWSServer(handlerFunc http.HandlerFunc) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(handlerFunc))
	webSocketURL = "ws" + strings.TrimPrefix(server.URL, "http")
	return server
}

func setUpTestRESTServer(handlerFunc http.HandlerFunc) *httptest.Server {
	server := httptest.NewServer(handlerFunc)
	restURLPrefix = server.URL + "/"
	return server
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

// newTestTradeReader creates a new Trade Reader for testing purposes. It ensures that a dial was successful and should be called after setupTestWsServer()
// It also creates a buffered tradeChan and returns it
func newTestTradeReader(t *testing.T) (tr *TradeReader, tradeChan chan *vwap.Trade) {
	tradeChan = make(chan *vwap.Trade)

	tr, err := NewTradeReader(tradeChan, testProductID, testLastTradeID)
	assert.NoError(t, err)
	return tr, tradeChan
}

func newTestMatchMsg(tradeID int) *MatchMsg {
	return &MatchMsg{

		TypeStr:      matchChanStr,
		TradeID:      tradeID,
		MakerOrderID: "testMaker",
		TakerOrderID: "testTaker",
		Side:         "testBuy",
		Size:         "5.23",
		Price:        "42.42",
		ProductID:    testProductID,
		Sequence:     99,
		Time:         time.Now().String(),
	}
}

func newRESTTradeMsg(tradeID int, price, size string) *Trade {
	return &Trade{
		Time:    time.Now().String(),
		TradeID: tradeID,
		Price:   price,
		Size:    size,
		Side:    "testSell",
	}
}

func convertToFloat(t *testing.T, str string) *big.Float {
	num, err := vwap.ParseBigFloat(str)
	assert.NoError(t, err)
	return num
}
func TestNewTradeReader(t *testing.T) {

	tests := []struct {
		name    string
		wantErr bool
	}{
		{"test valid dial", false},
		{"test invalid dial", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, reader, writer := mockLogger(t)
			defer resetLogger(reader, writer)

			server := setUpTestWSServer(handleDial(t, tt.wantErr))
			defer server.Close()

			tradeChan := make(chan *vwap.Trade)
			got, err := NewTradeReader(tradeChan, testProductID, testLastTradeID)
			if !tt.wantErr {
				defer got.CloseConn()
			}

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.Equal(t, testLastTradeID, got.LastTradeID)
				assert.Equal(t, testProductID, got.productID)
				assert.NotNil(t, got.done)
			}

		})
	}
}

func handleDial(t *testing.T, wantErr bool) func(w http.ResponseWriter, r *http.Request) {
	if wantErr {
		return dialFail(t)
	}

	return dialSuccess(t)

}

func dialSuccess(t *testing.T) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var upgrader = websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		assert.NoError(t, err)
		defer conn.Close()
	}
}

func dialFail(t *testing.T) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		// do nothing to force dial error
	}
}

func TestTradeReader_CloseConn(t *testing.T) {

	tests := []struct {
		name    string
		wantErr bool
	}{
		{"test valid connection close", false},
		{"test invalid connection close", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scanner, reader, writer := mockLogger(t)
			defer resetLogger(reader, writer)

			server := setUpTestWSServer(dialSuccess(t))
			defer server.Close()

			tr, _ := newTestTradeReader(t)
			defer tr.CloseConn()

			if tt.wantErr {
				err := tr.conn.Close()
				assert.NoError(t, err)
			}

			tr.CloseConn()

			if tt.wantErr {
				scanner.Scan()
				assert.Contains(t, scanner.Text(), "error closing TradeReader connection:")
			} else {
				assert.Empty(t, scanner.Text())
			}
		})
	}
}

func TestTradeReader_Subscribe(t *testing.T) {

	tests := []struct {
		name    string
		wantErr bool
	}{
		{"test valid subscribe", false},
		{"test error from writeJSON", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// _, reader, writer := mockLogger(t)
			// defer resetLogger(reader, writer)

			done := make(chan struct{})
			server := setUpTestWSServer(verifySubscribe(t, done, tt.wantErr))
			defer server.Close()

			tr, _ := newTestTradeReader(t)
			if tt.wantErr {
				tr.conn.Close()
			} else {
				defer tr.conn.Close()
			}

			err := tr.Subscribe()
			if tt.wantErr {
				assert.Error(t, err)
				<-done
			} else {
				<-done
				assert.NoError(t, err)
			}
		})
	}
}

func verifySubscribe(t *testing.T, done chan struct{}, wantErr bool) func(w http.ResponseWriter, r *http.Request) {

	return func(w http.ResponseWriter, r *http.Request) {
		var upgrader = websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		assert.NoError(t, err)
		defer conn.Close()

		if !wantErr {
			subMsg := SubscribeMsg{}
			err = conn.ReadJSON(&subMsg)
			assert.NoError(t, err)
			assert.Equal(t, subscribeTypeStr, subMsg.TypeStr)
			assert.Equal(t, []string{testProductID}, subMsg.ProductIDs)
			assert.Equal(t, []string{matchChanStr}, subMsg.Channels)
		}
		done <- struct{}{}

	}
}

// The meat of the tests, closer to integration tests, performs the full pipeline from reading into various actions
func TestTradeReader_ReadConn(t *testing.T) {

	inOrderMatch := newTestMatchMsg(testInOrderTradeID)
	outOfOrderMatch := newTestMatchMsg(testOutOfOrderTradeID)

	testSuccessReadAndSend := "test successful read and send"
	testOutOfOrderSuccess := "test match out of order with successful recovery"
	testOutOfOrderFail := "test match out of order with unsuccessful recovery"
	testErrorOnRead := "test error on read"
	testLastTradeIDZero := "test proper handling when last trade ID is 0"
	testEmptyMatch := "test empty match"

	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{testSuccessReadAndSend, readSuccessHandler(t, inOrderMatch)},
		{testErrorOnRead, readErrorHandler(t)},
		{testOutOfOrderSuccess, readSuccessHandler(t, outOfOrderMatch)},
		{testOutOfOrderFail, readSuccessHandler(t, outOfOrderMatch)},
		{testLastTradeIDZero, readSuccessHandler(t, outOfOrderMatch)},
		{testEmptyMatch, readSuccessHandler(t, &MatchMsg{})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scanner, reader, writer := mockLogger(t) // turn this off when debugging or developing as you will miss output!
			defer resetLogger(reader, writer)

			server := setUpTestWSServer(tt.handler)
			defer server.Close()

			tr, tradeChan := newTestTradeReader(t)
			defer tr.CloseConn()

			// do these settings before calling ReadConn to avoid data race
			if tt.name == testOutOfOrderSuccess {
				restServer := setUpTestRESTServer(getTradesSuccessHandler(t, outOfOrderMatch))
				defer restServer.Close()
			} else if tt.name == testLastTradeIDZero {
				tr.LastTradeID = 0
			}

			go tr.ReadConn()

			switch tt.name {

			case testSuccessReadAndSend:
				verifySuccessfullSend(t, tr, inOrderMatch, tradeChan)

			case testErrorOnRead:
				scanner.Scan()
				assert.Contains(t, scanner.Text(), "close error")
				_, ok := <-tr.done // verify the tradeReaders channel is closed on read error
				assert.False(t, ok)

			case testOutOfOrderSuccess:
				verifyMissingSends(t, tr, outOfOrderMatch, tradeChan)

			case testOutOfOrderFail:

				for i := 0; i < GETMissingTradesRetries; i++ {
					scanner.Scan()
					msg := fmt.Sprintf("found trades out of order, attempt #%d of %d to fix", i+1, GETMissingTradesRetries)
					assert.Contains(t, scanner.Text(), msg)
					scanner.Scan()
					msg = fmt.Sprintf("error attempting to fix missing trades: could not perform GET on coinbase productID: %s", testProductID)
					assert.Contains(t, scanner.Text(), msg)
				}

			case testLastTradeIDZero:
				verifySuccessfullSend(t, tr, outOfOrderMatch, tradeChan)

			case testEmptyMatch:

				scanner.Scan()
				msg := fmt.Sprintf("received empty message for tradeReader %s, skipping. Message: %v", tr.productID, &MatchMsg{})
				assert.Contains(t, scanner.Text(), msg)
			}

		})
	}
}

func getTradesSuccessHandler(t *testing.T, match *MatchMsg) func(w http.ResponseWriter, r *http.Request) {

	return func(w http.ResponseWriter, r *http.Request) {
		var data []*Trade
		// coinbase returns newest first
		data = append(data, newRESTTradeMsg(testOutOfOrderTradeID, match.Price, match.Size))
		data = append(data, newRESTTradeMsg(testSecondMissingTradeID, testSecondMissingPrice, testSecondMissingSize))
		data = append(data, newRESTTradeMsg(testFirstMissingTradeID, testFirstMissingPrice, testFirstMissingSize))
		err := json.NewEncoder(w).Encode(data)
		assert.NoError(t, err)
	}
}

func verifySuccessfullSend(t *testing.T, tr *TradeReader, match *MatchMsg, tradeChan chan *vwap.Trade) {

	trade := <-tradeChan
	verifyTrade(t, trade, testProductID, match.Price, match.Size)
	assert.Equal(t, match.TradeID, tr.LastTradeID)
}

func verifyMissingSends(t *testing.T, tr *TradeReader, outOfOrderMatch *MatchMsg, tradeChan chan *vwap.Trade) {
	trade := <-tradeChan
	verifyTrade(t, trade, testProductID, testFirstMissingPrice, testFirstMissingSize)

	trade = <-tradeChan
	verifyTrade(t, trade, testProductID, testSecondMissingPrice, testSecondMissingSize)

	verifySuccessfullSend(t, tr, outOfOrderMatch, tradeChan)
	assert.Equal(t, outOfOrderMatch.TradeID, tr.LastTradeID)
}

func verifyTrade(t *testing.T, trade *vwap.Trade, pair, priceStr, sizeStr string) {
	price := convertToFloat(t, priceStr)
	assert.Equal(t, price, trade.Price)

	size := convertToFloat(t, sizeStr)
	assert.Equal(t, size, trade.Quantity)

	assert.Equal(t, pair, trade.Pair)
}

func readSuccessHandler(t *testing.T, match *MatchMsg) func(w http.ResponseWriter, r *http.Request) {

	return func(w http.ResponseWriter, r *http.Request) {
		var upgrader = websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		assert.NoError(t, err)
		defer conn.Close()
		err = conn.WriteJSON(match)
		assert.NoError(t, err)

		for {
			_, _, err := conn.ReadMessage()
			if err != nil { // conn closed
				return
			}
		}
	}
}

func readErrorHandler(t *testing.T) func(w http.ResponseWriter, r *http.Request) {

	return func(w http.ResponseWriter, r *http.Request) {
		var upgrader = websocket.Upgrader{}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		err = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "test close error"), time.Now().Add(time.Second))
		if err != nil {
			fmt.Println("error in test setup, couldn't write close message: ", err)
		}
	}
}

func TestTradeReader_sendTrade(t *testing.T) {

	tests := []struct {
		name         string
		wantPriceErr bool
		wantSizeErr  bool
	}{
		{"test valid", false, false},
		{"test price convert error", true, false},
		{"test size convert error", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scanner, reader, writer := mockLogger(t) // turn this off when debugging or developing as you will miss output!
			defer resetLogger(reader, writer)

			server := setUpTestWSServer(dialSuccess(t))
			defer server.Close()

			tr, tradeChan := newTestTradeReader(t)
			defer tr.CloseConn()

			if tt.wantPriceErr {
				tr.sendTrade(testProductID, "notAPrice", testSize, testLastTradeID)
			} else if tt.wantSizeErr {
				tr.sendTrade(testProductID, testPrice, "notASize", testLastTradeID)
			} else {
				go tr.sendTrade(testProductID, testPrice, testSize, testLastTradeID)
			}

			if tt.wantPriceErr || tt.wantSizeErr {

				var msg string
				if tt.wantPriceErr {
					msg = "couldn't convert price string to *big.Float"
				} else {
					msg = "couldn't convert size string to *big.Float"
				}
				scanner.Scan()
				assert.Contains(t, scanner.Text(), msg)

			} else {
				trade := <-tradeChan
				verifyTrade(t, trade, testProductID, testPrice, testSize)
			}

		})
	}
}

func TestTradeReader_handleReadErr(t *testing.T) {

	testNormalCloseError := "test normal close error"
	testOtherCloseError := "test other close error"
	testNonCloseError := "test non close error"

	tests := []struct {
		name string
	}{
		{testNormalCloseError},
		{testOtherCloseError},
		{testNonCloseError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scanner, reader, writer := mockLogger(t)
			defer resetLogger(reader, writer)

			server := setUpTestWSServer(dialSuccess(t))
			defer server.Close()

			tr, _ := newTestTradeReader(t)
			defer tr.CloseConn()

			switch tt.name {
			case testNormalCloseError:
				err := &websocket.CloseError{Code: websocket.CloseNormalClosure, Text: "Test Normal Close"}
				tr.handleReadErr(err)
				scanner.Scan()
				assert.Contains(t, scanner.Text(), fmt.Sprintf("normal connection close for tradeReader %s", testProductID))

			case testOtherCloseError:
				err := &websocket.CloseError{Code: websocket.CloseInternalServerErr, Text: "Test Other Close"}
				tr.handleReadErr(err)
				scanner.Scan()
				assert.Contains(t, scanner.Text(), fmt.Sprintf("close error for tradeReader %s", testProductID))

			case testNonCloseError:
				err := errors.New("test non close error")
				tr.handleReadErr(err)
				scanner.Scan()
				assert.Contains(t, scanner.Text(), fmt.Sprintf("read error for tradeReader %s", testProductID))

			}

		})
	}
}

func TestTradeReader_WaitForEnd(t *testing.T) {
	testTradeReaderDone := "test tradeReader done"
	testContextCancel := "test context cancelled"

	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{testTradeReaderDone, closeResponse(t)},
		{testContextCancel, closeResponse(t)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, reader, writer := mockLogger(t)
			defer resetLogger(reader, writer)

			server := setUpTestWSServer(tt.handler)
			defer server.Close()

			tr, _ := newTestTradeReader(t)
			defer tr.CloseConn()

			switch tt.name {

			case testTradeReaderDone:
				close(tr.done)
				tr.WaitForEnd(context.Background())
			}
		})
	}
}

func closeResponse(t *testing.T) func(w http.ResponseWriter, r *http.Request) {

	return func(w http.ResponseWriter, r *http.Request) {

		var upgrader = websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		assert.NoError(t, err)
		defer conn.Close()

		// TODO: this code doesn't always work so commenting, would like to verify proper closure but I'm not sure why it doesn't work
		// would need more investigation into proper close/response websocket mechanisms
		// _, _, err = conn.ReadMessage()
		// if err != nil {
		// 	closeErr, ok := err.(*websocket.CloseError)
		// 	if !ok {
		// 		assert.Fail(t, fmt.Sprintf("received error was not a close error: %v", err))
		// 	}
		// 	if closeErr.Code != websocket.CloseNormalClosure {
		// 		assert.Fail(t, fmt.Sprintf("received error was not a normal close error: %v", err))
		// 	}
		// }

	}

}

// TODO: could implement a unit test here but this functionality is fully tested by TestTradeReader_ReadConn
// func TestTradeReader_fixMissingTrades(t *testing.T) {

// 	tests := []struct {
// 		name string
// 	}{
// 		{"test unsuccessful REST"},
// 	}
// 	for _, tt := range tests {
// 		t.Run(tt.name, func(t *testing.T) {
// 			server := setUpTestWSServer(dialSuccess(t))
// 			defer server.Close()

// 			tr, _ := newTestTradeReader(t)
// 			defer tr.CloseConn()

// 			restServer := setUpTestRESTServer(getTradesSuccessHandler(t, outOfOrderMatch))
// 			defer restServer.Close()
// 			go tr.ReadConn()
// 			verifyMissingSends(t, tr, outOfOrderMatch, tradeChan)
// 		})
// 	}
// }

func TestTradeReader_getMissingTrades(t *testing.T) {

	tests := []struct {
		name        string
		wantRESTErr bool
	}{
		{"test valid", false},
		{"test error on GET", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, reader, writer := mockLogger(t)
			defer resetLogger(reader, writer)

			tr := &TradeReader{}

			match := newTestMatchMsg(testLastTradeID)

			if !tt.wantRESTErr {
				restServer := setUpTestRESTServer(getTradesSuccessHandler(t, match))
				defer restServer.Close()
			}

			got, err := tr.getMissingTrades(testProductID)
			if tt.wantRESTErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, match.Price, got[0].Price)
				assert.Equal(t, match.Size, got[0].Size)
			}
		})
	}
}
