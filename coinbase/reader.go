package coinbase

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"time"

	"github.com/ddrake12/vwap"
	"github.com/gorilla/websocket"
)

const (
	subscribeTypeStr = "subscribe"
	matchChanStr     = "matches"
	// IDBTCUSD is the coinbase product ID for the BTC-USD pair
	IDBTCUSD = "BTC-USD"
	// IDETHUSD is the coinbase product ID for the ETH-USD pair
	IDETHUSD = "ETH-USD"
	// IDETHBTC is the coinbase prodcut ID for the ETH-BTC pair
	IDETHBTC = "ETH-BTC"
)

var (
	webSocketURL  = "wss://ws-feed.pro.coinbase.com"
	restURLPrefix = "https://api.pro.coinbase.com/products/"
	restURLSuffix = "/trades"
	// GETMissingTradesRetries is the retry limit for attempting to GET any missing trades from the matches channel for a given TradeReader's productID using the REST API for coinbase
	GETMissingTradesRetries = 3
)

// TradeReader is the
type TradeReader struct {
	conn      *websocket.Conn
	tradeChan chan<- *vwap.Trade
	productID string
	logger    *log.Logger   //TODO: implement a real logging framework
	done      chan struct{} // the TradeReader will close done once it cannot read from the websocket anymore
	// LastTradeID can be used to initialize a new instance after a failure and fill in any missing gaps in the VWAP
	LastTradeID int
}

// Trade is the JSON representation for trades used by the coinbase /prodcut/{product_id}/trades REST API
// TODO: maybe rename to RESTtrade
type Trade struct {
	Time    string `json:"time"`
	TradeID int    `json:"trade_id"`
	Price   string `json:"price"`
	Size    string `json:"size"`
	Side    string `json:"side"`
}

// SubscribeMsg maps the JSON subscribe message used by coinbase for different product IDs and channels
type SubscribeMsg struct {
	TypeStr    string   `json:"type"`
	ProductIDs []string `json:"product_ids"`
	Channels   []string `json:"channels"`
}

// MatchMsg maps the JSON received from coinbase for the matches channel
type MatchMsg struct {
	TypeStr      string `json:"type"`
	TradeID      int    `json:"trade_id"`
	MakerOrderID string `json:"maker_order_id"`
	TakerOrderID string `json:"take_order_id"`
	Side         string `json:"side"`
	Size         string `json:"size"`
	Price        string `json:"price"`
	ProductID    string `json:"product_id"`
	Sequence     int    `json:"sequence"`
	Time         string `json:"time"`
}

// NewTradeReader initializes a new coinbase.TradeReader object and dials the coinbase websocket. It takes a trade channel to send out vwap.Trade objects,
// and a productID to watch. If a connection cannot be dialed it returns an error. Use a buffered channel for better performance, vwap.ChanBuffLen is recommended.
func NewTradeReader(tradeChan chan<- *vwap.Trade, productID string, lastTradeID int) (*TradeReader, error) {

	conn, _, err := websocket.DefaultDialer.Dial(webSocketURL, nil)
	if err != nil {
		return nil, fmt.Errorf("error dialing websocket: %v", err)
	}

	done := make(chan struct{})

	return &TradeReader{
		conn:        conn,
		tradeChan:   tradeChan,
		productID:   productID,
		logger:      log.Default(),
		done:        done,
		LastTradeID: lastTradeID,
	}, nil

}

// CloseConn closes the websocket connection and logs any close error. Works well with defer
func (tr *TradeReader) CloseConn() {
	err := tr.conn.Close()
	if err != nil {
		tr.logger.Printf("error closing TradeReader connection: %v", err)
	}
}

// Subscribe sends the subscribe message to the websocket for the match channel and uses productID of the instance.
func (tr *TradeReader) Subscribe() error {

	sbsMsg := &SubscribeMsg{
		TypeStr:    subscribeTypeStr,
		ProductIDs: []string{tr.productID},
		Channels:   []string{matchChanStr},
	}

	if err := tr.conn.WriteJSON(sbsMsg); err != nil {
		return fmt.Errorf("error writing JSON for subscribe: %v", err)
	}

	return nil
}

// ReadConn reads the websocket connection and gets the information to send a trade on the trade channel for the instance. It checks
// to ensure message order was maintained and fills in any gaps if any messages were dropped. It should be ran in a separate goroutine and started
// before Subscribe is called
func (tr *TradeReader) ReadConn() {
	defer close(tr.done)

	for {
		match := &MatchMsg{}
		err := tr.conn.ReadJSON(match)
		if err != nil {
			tr.handleReadErr(err)
			return
		}

		if match.TradeID == 0 {
			// TODO: discuss in code review, could improve detection for empty messages here if we want but this seems to be working
			tr.logger.Printf("received empty message for tradeReader %s, skipping. Message: %v", tr.productID, match)
			continue
		}

		if tr.LastTradeID == 0 {
			tr.LastTradeID = match.TradeID
		} else {
			if match.TradeID != tr.LastTradeID+1 && match.TradeID > tr.LastTradeID {
				for i := 0; i < GETMissingTradesRetries; i++ {
					tr.logger.Printf("found trades out of order, attempt #%d of %d to fix", i+1, GETMissingTradesRetries)
					err = tr.fixMissingTrades(match.TradeID, match.ProductID)
					if err != nil {
						tr.logger.Printf("error attempting to fix missing trades: %v", err)
					} else {
						break
					}
				}

			}

			tr.LastTradeID = match.TradeID
		}

		tr.sendTrade(match.ProductID, match.Price, match.Size, match.TradeID)
	}
}

// sendTrade parses the price and size strings and sends then as a vwap.Trade on the trade channel for the instance.
func (tr *TradeReader) sendTrade(productID, priceStr, sizeStr string, tradeID int) {

	price, err := vwap.ParseBigFloat(priceStr)
	if err != nil {
		tr.logger.Printf("couldn't convert price string to *big.Float error: %v ProductID: %s, Price: %s, Size: %s", err, productID, priceStr, sizeStr)
		return
	}

	size, err := vwap.ParseBigFloat(sizeStr)
	if err != nil {
		tr.logger.Printf("couldn't convert size string to *big.Float: error: %v ProductID: %s, Price: %s, Size: %s", err, productID, priceStr, sizeStr)
		return
	}

	trade := &vwap.Trade{
		Pair:     productID,
		Price:    price,
		Quantity: size,
	}

	tr.tradeChan <- trade
}

// handleReadErr handles an error from reading the websocket. If detects a normal close error, and identifies and logs other errors
func (tr *TradeReader) handleReadErr(err error) {
	if closeErr, ok := err.(*websocket.CloseError); ok {
		if closeErr.Code == websocket.CloseNormalClosure {
			tr.logger.Printf("normal connection close for tradeReader %s: %v", tr.productID, closeErr)
		} else {
			tr.logger.Printf("close error for tradeReader %s: %v", tr.productID, closeErr)
		}
	} else {
		tr.logger.Printf("read error for tradeReader %s: %v", tr.productID, err)
	}
}

// WaitForEnd blocks until an end condition is met. This happens if the given context is cancelled and then the websocket is closed normally.
// This also happens if reading from the websocket encounters an error.
func (tr *TradeReader) WaitForEnd(ctx context.Context) {
	for {
		select {
		case <-tr.done:
			return
		case <-ctx.Done():
			tr.logger.Printf("received shutdown, closing websocket connection for tradereader: %s", tr.productID)
			err := tr.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			if err != nil {
				tr.logger.Printf("error writing close message for tradeReader %s: %v", tr.productID, err)
				return
			}
			select {
			case <-tr.done:
			case <-time.After(1 * time.Second):
				tr.logger.Print("timeout waiting for close message response")
			}
			return
		}
	}
}

// fixMissingTrades will go through the list of the latest trades (starting with the latest) and work until it sees the first missing trade ID
// and then send all trades until the nextSeenTrade into the trade channel for the instanace
func (tr *TradeReader) fixMissingTrades(nextSeenTrade int, productID string) error {

	trades, err := tr.getMissingTrades(productID)
	if err != nil {
		return err
	}

	count := 0
	// trades are returned with the latest first, so we only have to go back to the first one we are missing
	for _, trade := range trades {
		count++
		if trade.TradeID == tr.LastTradeID+1 {
			break
		}
	}

	// and then go backwards from there until the one we just saw on the websocket to ensure we get all trades in between
	for j := count; j > 0; j-- {
		curTrade := trades[j-1]
		if curTrade.TradeID >= nextSeenTrade {
			break
		}

		tr.logger.Printf("fixing missing trades, curTrade: %v", curTrade)

		tr.sendTrade(productID, curTrade.Price, curTrade.Size, curTrade.TradeID)

	}

	return nil
}

// getMissingTrades performs a GET /trades on the given product ID. It is assumed that any missing trades will be in the default/first
// page of the latest trades
func (tr *TradeReader) getMissingTrades(productID string) ([]Trade, error) {

	resp, err := http.Get(restURLPrefix + productID + restURLSuffix)
	if err != nil {
		return nil, fmt.Errorf("could not perform GET on coinbase productID: %s, error: %v", productID, err)
	}

	defer resp.Body.Close()
	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("couldn't read body bytes in getMissingTrades: %v ", err)
	}

	var trades []Trade
	if err := json.Unmarshal(bodyBytes, &trades); err != nil {
		return nil, fmt.Errorf("couldn't unmarshal in getMissingTrades: %v", err)
	}

	return trades, nil

}
