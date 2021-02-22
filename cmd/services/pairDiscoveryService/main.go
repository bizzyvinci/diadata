package main

import (
	"os"
	"os/signal"
	"sync"
	"time"

	scrapers "github.com/diadata-org/diadata/internal/pkg/exchange-scrapers"
	"github.com/diadata-org/diadata/pkg/dia"
	"github.com/diadata-org/diadata/pkg/dia/helpers/configCollectors"
	models "github.com/diadata-org/diadata/pkg/model"
	"github.com/jackc/pgx"
	"github.com/sirupsen/logrus"
	"github.com/tkanos/gonfig"
)

var (
	log *logrus.Logger
)

const (
	postgresKey = "postgres_key.txt"
)

type Task struct {
	closed chan struct{}
	wg     sync.WaitGroup
	ticker *time.Ticker
}

func init() {
	log = logrus.New()
}

func main() {
	task := &Task{
		closed: make(chan struct{}),
		/// Retrieve every hour
		ticker: time.NewTicker(time.Second * 60 * 60),
	}

	relDB, err := models.NewRelDataStore()
	if err != nil {
		panic("Couldn't initialize relDB, error: " + err.Error())
	}

	updateExchangePairs(relDB)

	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt)
	task.wg.Add(1)
	go func() { defer task.wg.Done(); task.run(relDB) }()
	select {
	case <-c:
		log.Info("Received stop signal.")
		task.stop()
	}
}

// TO DO: Refactor toggle==true case.

// toggle == false: fetch all exchange's trading pairs from postgres and write them into redis caching layer
// toggle == true:  connect to all exchange's APIs and check for new pairs
func updateExchangePairs(relDB *models.RelDB) {
	toggle, err := getConfigTogglePairDiscovery()
	if err != nil {
		log.Errorf("updateExchangePairs GetConfigTogglePairDiscovery: %v", err)
		return
	}
	toggle = true
	if toggle == false {

		log.Info("GetConfigTogglePairDiscovery = false, using values from config files")
		for _, exchange := range dia.Exchanges() {
			if exchange == "Unknown" {
				continue
			}
			// Fetch all pairs available for @exchange from exchangepair table in postgres
			pairs, err := relDB.GetExchangePairs(exchange)
			if err != nil {
				log.Errorf("getting pairs from postgres for exchange %s: %v", exchange, err)
				continue
			}
			// Optional addition of pairs from config file
			pairs, err = addPairsFromConfig(exchange, pairs)
			if err != nil {
				log.Errorf("adding pairs from config file for exchange %s: %v", exchange, err)
			}
			// Set pairs in postgres and redis caching layer. The collector will fetch these
			// in order to build the corresponding pair scrapers.
			for _, pair := range pairs {
				err = relDB.SetExchangePair(exchange, pair, true)
				if err != nil {
					log.Errorf("setting exchangepair table for pair on exchange %s: %v", exchange, err)
				}
			}
			log.Infof("exchange %s updated\n", exchange)
		}
		log.Info("Update complete.")

	} else {

		log.Info("GetConfigTogglePairDiscovery = true, fetch new pairs from exchange's API")
		exchangeMap := scrapers.Exchanges
		for _, exchange := range dia.Exchanges() {

			// Make exchange API Scraper in order to fetch pairs
			log.Info("Updating exchange ", exchange)
			var scraper scrapers.APIScraper
			config, err := dia.GetConfig(exchange)
			if err == nil {
				scraper = scrapers.NewAPIScraper(exchange, config.ApiKey, config.SecretKey)
			} else {
				log.Info("No valid API config for exchange: ", exchange, " Error: ", err.Error())
				log.Info("Proceeding with no API secrets")
				scraper = scrapers.NewAPIScraper(exchange, "", "")
			}

			// If no error, fetch pairs by method implemented for each scraper resp.
			if scraper != nil {
				if exchangeMap[exchange].Centralized {

					// --------- 1. Step: collect pairs from Exchange API, DB and config file ---------
					// Fetch pairs using the API scraper.
					pairs, err := scraper.FetchAvailablePairs()
					if err != nil {
						log.Errorf("fetching pairs for exchange %s: %v", exchange, err)
					}
					// If not in postgres yet, add fetched pair
					pairs, err = addNewPairs(exchange, pairs, relDB)
					if err != nil {
						log.Errorf("adding pairs from asset DB for exchange %s: %v", exchange, err)
					}
					// Optional addition of pairs from config file.
					pairs, err = addPairsFromConfig(exchange, pairs)
					if err != nil {
						log.Errorf("adding pairs from config file for exchange %s: %v", exchange, err)
					}

					// --------- 2. Step: Try to verify all pairs collected above ---------

					// 2.a Get list of symbols available on exchange and try to match to assets.

					symbols, err := dia.GetAllSymbolsFromPairs(pairs)
					if err != nil {
						log.Error(err)
					}
					verificationCount := 0
					for _, symbol := range symbols {
						// signature for this part:
						// func matchExchangeSymbol(symbol string, exchange string, relDB *models.RelDB)

						time.Sleep(1 * time.Second)
						// First set all symbols traded on the exchange. These are subsequently
						// matched with assets from the asset table.

						// Continue if symbol is already in DB and verified.
						_, verified, err := relDB.GetExchangeSymbolAssetID(exchange, symbol)
						if err != nil {
							if err.Error() != pgx.ErrNoRows.Error() {
								log.Errorf("error getting exchange symbol %s: %v", symbol, err)
							}
						}
						if verified {
							verificationCount++
							continue
						}
						// Set exchange symbol if not in table yet.
						err = relDB.SetExchangeSymbol(exchange, symbol)
						if err != nil {
							log.Errorf("error setting exchange symbol %s: %v", symbol, err)
						}
						// Gather as much information on @symbol as available on the exchange's API.
						assetInfo, err := scraper.FetchTickerData(symbol)
						if err != nil {
							log.Errorf("error fetching ticker data for %s: %v", symbol, err)
							continue
						}
						// Using the gathered information, find matching assets in asset table.
						assetCandidates, err := relDB.IdentifyAsset(assetInfo)
						if err != nil {
							log.Errorf("error getting asset candidates for %s: %v", symbol, err)
							continue
						}
						if len(assetCandidates) != 1 {
							log.Errorf("could not uniquely identify token ticker %s on exchange %s. Please identify manually.", symbol, exchange)
							continue
						}
						// In case of a unique match, verify symbol in postgres and
						// assign it the corresponding foreign key from the asset table.
						if len(assetCandidates) == 1 {

							verificationCount++
							assetID, err := relDB.GetAssetID(assetCandidates[0])
							if err != nil {
								log.Error(err)
							}
							ok, err := relDB.VerifyExchangeSymbol(exchange, symbol, assetID)
							if err != nil {
								log.Error(err)
							}
							if ok {
								log.Infof("verified token ticker %s ", symbol)
							}
						}
					}
					log.Infof("verification of symbols on exchange %s done. Verified %d out of %d symbols.\n", exchange, verificationCount, len(symbols))

					// 2.b Verify/falsify exchange pairs using the exchangesymbol table in postgres.
					for _, pair := range pairs {
						log.Info("handle pair ", pair)
						time.Sleep(1 * time.Second)
						pairSymbols, err := dia.GetPairSymbols(pair)
						if err != nil {
							log.Errorf("error getting symbols from pair string for %s", pair.ForeignName)
							continue
						}
						quotetokenID, quotetokenVerified, err := relDB.GetExchangeSymbolAssetID(exchange, pairSymbols[0])
						basetokenID, basetokenVerified, err := relDB.GetExchangeSymbolAssetID(exchange, pairSymbols[1])

						if quotetokenVerified {
							quotetoken, err := relDB.GetAssetByID(quotetokenID)
							if err != nil {
								log.Error(err)
							}
							pair.UnderlyingPair.QuoteToken = quotetoken
						}
						if basetokenVerified {
							basetoken, err := relDB.GetAssetByID(basetokenID)
							if err != nil {
								log.Error(err)
							}
							pair.UnderlyingPair.BaseToken = basetoken
						}
						if quotetokenVerified && basetokenVerified {
							pair.Verified = true
						}
						// Set pair to postgres and redis cache.
						err = relDB.SetExchangePair(exchange, pair, true)
						if err != nil {
							log.Errorf("setting exchangepair table for pair on exchange %s: %v", exchange, err)
						}
					}

					go func(s scrapers.APIScraper, exchange string) {
						time.Sleep(5 * time.Second)
						log.Error("Closing scraper: ", exchange)
						scraper.Close()
					}(scraper, exchange)
				} else {
					// For DEXes, FetchAvailablePairs can retrieve unique information.
					// Pairs must contain base- and quotetoken addresses and blockchains.
					pairs, err := scraper.FetchAvailablePairs()
					if err != nil {
						log.Errorf("fetching pairs for exchange %s: %v", exchange, err)
					}
					for _, pair := range pairs {
						// Set pair to postgres and redis cache
						err = relDB.SetExchangePair(exchange, pair, true)
						if err != nil {
							log.Errorf("setting exchangepair table for pair on exchange %s: %v", exchange, err)
						}
					}
					// For the sake of completeness/statistics we could also write the symbols into exchangesymbol table.
					// symbols, err := dia.GetAllSymbolsFromPairs(pairs)
					// if err != nil {
					// 	log.Error(err)
					// }

					go func(s scrapers.APIScraper, exchange string) {
						time.Sleep(5 * time.Second)
						log.Error("Closing scraper: ", exchange)
						scraper.Close()
					}(scraper, exchange)
				}
			} else {
				log.Error("Error creating APIScraper for exchange: ", exchange)
			}
		}
		log.Info("Update complete.")

	}
}

func getConfigTogglePairDiscovery() (bool, error) {
	// Activates periodically
	return false, nil //TOFIX
}

// addNewPairsToPG adds pair from @pairs if it's not in our postgres DB yet.
// Equality refers to the unique identifier (exchange,foreignName).
func addNewPairs(exchange string, pairs []dia.ExchangePair, assetDB *models.RelDB) ([]dia.ExchangePair, error) {
	persistentPairs, err := assetDB.GetExchangePairs(exchange)
	if err != nil {
		return pairs, err
	}
	// The order counts here. persistentPairs have priority.
	return dia.MergeExchangePairs(persistentPairs, pairs), nil
}

// addPairsFromConfig adds pairs from the config file to @pairs, if not in there yet.
// Equality refers to the unique identifier (exchange,foreignName).
func addPairsFromConfig(exchange string, pairs []dia.ExchangePair) ([]dia.ExchangePair, error) {
	pairsFromConfig, err := getPairsFromConfig(exchange)
	if err != nil {
		return pairs, err
	}
	return dia.MergeExchangePairs(pairs, pairsFromConfig), nil
}

// getPairsFromConfig returns pairs from exchange's config file.
func getPairsFromConfig(exchange string) ([]dia.ExchangePair, error) {
	configFileAPI := configCollectors.ConfigFileConnectors(exchange)
	type Pairs struct {
		Coins []dia.ExchangePair
	}
	var coins Pairs
	err := gonfig.GetConf(configFileAPI, &coins)
	return coins.Coins, err
}

func (t *Task) run(relDB *models.RelDB) {
	for {
		select {
		case <-t.closed:
			return
		case <-t.ticker.C:
			updateExchangePairs(relDB)
		}
	}
}

func (t *Task) stop() {
	log.Println("Stoping exchange pair update thread...")
	close(t.closed)
	t.wg.Wait()
	log.Println("Thread stopped, cleaning...")
	// Clean if required
	log.Println("Done")
}
